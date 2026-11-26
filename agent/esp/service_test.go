package esp

import (
	"bytes"
	"context"
	"crypto/md5"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/anmh1205/anyusb/agent/platform"
	"github.com/coder/websocket"
)

type mockESPROMPort struct {
	mu          sync.Mutex
	baud        int
	dtr, rts    bool
	closed      bool
	flash       map[uint32]byte
	flashBase   uint32
	flashBlocks uint32
	rxBuf       bytes.Buffer
	txBuf       bytes.Buffer
	inEscape    bool
	inFrame     bool
	notify      chan struct{}
	regs        map[uint32]uint32
	spiCmd      byte
	forceVerify string
}

func newMockESP32S3Port(baud int) *mockESPROMPort {
	return &mockESPROMPort{
		baud:   baud,
		flash:  make(map[uint32]byte),
		notify: make(chan struct{}, 16),
		regs: map[uint32]uint32{
			0x3FCEF14C: 4,          // UARTDEV_BUF_NO_USB_JTAG_SERIAL
			0x60007044: 0xC78CC424, // MAC0
			0x60007048: 0x0000CC68, // MAC1 -> cc:68:c7:8c:c4:24
			0x600080B4: 0,          // RTC_CNTL_SWD_CONF_REG
			0x60002018: 0,          // SPI_USR
			0x60002020: 0,          // SPI_USR2
			0x60002000: 0,          // SPI_CMD
		},
	}
}

func (m *mockESPROMPort) signal() {
	select {
	case m.notify <- struct{}{}:
	default:
	}
}

func (m *mockESPROMPort) Read(p []byte) (int, error) {
	for {
		m.mu.Lock()
		if m.closed {
			m.mu.Unlock()
			return 0, io.EOF
		}
		if m.rxBuf.Len() > 0 {
			n, err := m.rxBuf.Read(p)
			m.mu.Unlock()
			return n, err
		}
		m.mu.Unlock()
		select {
		case <-m.notify:
		case <-time.After(20 * time.Millisecond):
			return 0, nil
		}
	}
}

func (m *mockESPROMPort) Write(p []byte) (int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return 0, io.ErrClosedPipe
	}
	for _, b := range p {
		if !m.inFrame {
			if b == slipFrameEnd {
				m.inFrame = true
				m.txBuf.Reset()
			}
			continue
		}
		if m.inEscape {
			m.inEscape = false
			switch b {
			case slipEscEnd:
				m.txBuf.WriteByte(slipFrameEnd)
			case slipEscEsc:
				m.txBuf.WriteByte(slipEscape)
			}
			continue
		}
		switch b {
		case slipEscape:
			m.inEscape = true
		case slipFrameEnd:
			if m.txBuf.Len() > 0 {
				pkt := append([]byte(nil), m.txBuf.Bytes()...)
				m.handlePacketLocked(pkt)
				m.txBuf.Reset()
			}
			m.inFrame = false
		default:
			m.txBuf.WriteByte(b)
		}
	}
	return len(p), nil
}

func (m *mockESPROMPort) handlePacketLocked(pkt []byte) {
	if len(pkt) < 8 || pkt[0] != romDirectionRequest {
		return
	}
	op := pkt[1]
	dataLen := int(binary.LittleEndian.Uint16(pkt[2:4]))
	chk := binary.LittleEndian.Uint32(pkt[4:8])
	if len(pkt) < 8+dataLen {
		return
	}
	data := pkt[8 : 8+dataLen]
	reply := func(val uint32, body ...byte) {
		out := make([]byte, 8+len(body)+4)
		out[0] = romDirectionResponse
		out[1] = op
		binary.LittleEndian.PutUint16(out[2:4], uint16(len(body)+4))
		binary.LittleEndian.PutUint32(out[4:8], val)
		copy(out[8:], body)
		m.rxBuf.Write(encodeSLIP(out))
		m.signal()
	}

	switch op {
	case cmdSync:
		for i := 0; i < 8; i++ {
			reply(0x07071220)
		}
	case cmdGetSecInfo:
		var info [20]byte
		binary.LittleEndian.PutUint32(info[12:16], 9) // ESP32-S3
		reply(0, info[:]...)
	case cmdReadReg:
		addr := binary.LittleEndian.Uint32(data[:4])
		reply(m.regs[addr])
	case cmdWriteReg:
		addr := binary.LittleEndian.Uint32(data[0:4])
		val := binary.LittleEndian.Uint32(data[4:8])
		mask := binary.LittleEndian.Uint32(data[8:12])
		m.regs[addr] = (m.regs[addr] &^ mask) | (val & mask)
		if addr == 0x60002020 { // SPI_USR2
			m.spiCmd = byte(val & 0xFF)
		}
		if addr == 0x60002000 && m.spiCmd == 0x9F { // SPI_CMD_USR RDID
			m.regs[0x60002058] = 0x001840C8 // GigaDevice 16MB (0x18)
			m.regs[0x60002000] = 0
		}
		reply(0)
	case cmdSPIAttach, cmdSPISetParams, cmdChangeBaudrate, cmdFlashEnd:
		reply(0)
	case cmdFlashBegin:
		eraseSize := binary.LittleEndian.Uint32(data[0:4])
		m.flashBlocks = binary.LittleEndian.Uint32(data[4:8])
		m.flashBase = binary.LittleEndian.Uint32(data[12:16])
		for i := uint32(0); i < eraseSize; i++ {
			delete(m.flash, m.flashBase+i)
		}
		reply(0)
	case cmdFlashData:
		size := binary.LittleEndian.Uint32(data[0:4])
		seq := binary.LittleEndian.Uint32(data[4:8])
		payload := data[16 : 16+size]
		if xorChecksum(payload) != chk {
			out := make([]byte, 10)
			out[0], out[1] = romDirectionResponse, op
			binary.LittleEndian.PutUint16(out[2:4], 2)
			out[8], out[9] = 1, 0x07
			m.rxBuf.Write(encodeSLIP(out))
			m.signal()
			return
		}
		offset := m.flashBase + seq*flashWriteSize
		for i, b := range payload {
			m.flash[offset+uint32(i)] = b
		}
		reply(0)
	case cmdSPIFlashMD5:
		addr := binary.LittleEndian.Uint32(data[0:4])
		size := binary.LittleEndian.Uint32(data[4:8])
		if m.forceVerify != "" {
			reply(0, []byte(m.forceVerify)...)
			return
		}
		h := md5.New()
		buf := make([]byte, size)
		for i := range buf {
			if b, ok := m.flash[addr+uint32(i)]; ok {
				buf[i] = b
			} else {
				buf[i] = 0xFF
			}
		}
		_, _ = h.Write(buf)
		reply(0, []byte(hex.EncodeToString(h.Sum(nil)))...)
	}
}

func (m *mockESPROMPort) Close() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.closed = true
	m.signal()
	return nil
}

func (m *mockESPROMPort) reopen(baud int) SerialPort {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.closed = false
	m.inFrame = false
	m.inEscape = false
	m.rxBuf.Reset()
	m.txBuf.Reset()
	m.baud = baud
	return m
}
func (m *mockESPROMPort) SetBaud(baud int) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.baud = baud
	return nil
}
func (m *mockESPROMPort) SetDTR(state bool) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.dtr = state
	return nil
}
func (m *mockESPROMPort) SetRTS(state bool) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.rts = state
	return nil
}
func (m *mockESPROMPort) SetDTRAndRTS(dtr, rts bool) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.dtr, m.rts = dtr, rts
	return nil
}
func (m *mockESPROMPort) SetReadTimeout(time.Duration) error { return nil }
func (m *mockESPROMPort) Flush() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.rxBuf.Reset()
	return nil
}

func buildBootloaderImage(chipID uint16, payload []byte) []byte {
	var img bytes.Buffer
	img.Write([]byte{espImageMagic, 1, 2, 0x20, 0x00, 0x10, 0x3C, 0x40}) // 8-byte common header (DIO, 4MB, 40MHz)
	var ext [16]byte
	binary.LittleEndian.PutUint16(ext[4:6], chipID)
	ext[15] = 1 // append_digest
	img.Write(ext[:])
	var segHdr [8]byte
	binary.LittleEndian.PutUint32(segHdr[0:4], 0x3FC90000)
	binary.LittleEndian.PutUint32(segHdr[4:8], uint32(len(payload)))
	img.Write(segHdr[:])
	img.Write(payload)
	pad := 15 - (img.Len() % 16)
	img.Write(bytes.Repeat([]byte{0}, pad))
	img.WriteByte(0) // checksum byte
	sum := sha256.Sum256(img.Bytes())
	img.Write(sum[:])
	return img.Bytes()
}

func waitJob(t *testing.T, service *Service, owner, id string) Frame {
	t.Helper()
	active, err := service.Job(owner, id)
	if err != nil {
		t.Fatal(err)
	}
	timeout := time.After(8 * time.Second)
	for {
		frame, updated := active.Snapshot(0)
		if frame.Job.Status == "succeeded" || frame.Job.Status == "failed" {
			return frame
		}
		select {
		case <-updated:
		case <-timeout:
			t.Fatalf("job %s did not finish", id)
		}
	}
}

func joinLogs(frame Frame) string {
	var out strings.Builder
	for _, item := range frame.Logs {
		out.WriteString(item.Text)
	}
	return out.String()
}

func TestHostJobAndUploadReplay(t *testing.T) {
	rom := newMockESP32S3Port(115200)
	var opens, releases atomic.Int32
	service := NewWithOpener(t.Context(), t.TempDir(), func(_ string, baud int) (SerialPort, error) {
		return rom.reopen(baud), nil
	}, func(_ context.Context, owner, device, requested string) (string, func(), error) {
		if owner != "trusted-client" || device != "board" {
			return "", nil, errors.New("incorrect owner or device")
		}
		opens.Add(1)
		return "test-host-port", func() { releases.Add(1) }, nil
	})
	if diagnostic := service.Doctor(); !diagnostic.OK || !diagnostic.Optional {
		t.Fatalf("unexpected doctor check: %+v", diagnostic)
	}
	bootImage := buildBootloaderImage(9, bytes.Repeat([]byte{0xA5}, 1500))
	start := func(content []byte) (JobState, error) {
		var dst bytes.Buffer
		multipart := multipart.NewWriter(&dst)
		if err := multipart.WriteField("manifest", `{"flash_files":{"0x0":"bootloader.bin"},"flash_settings":{"flash_mode":"qio","flash_size":"16MB"},"extra_esptool_args":{"chip":"esp32s3","after":"hard_reset"}}`); err != nil {
			t.Fatal(err)
		}
		part, err := multipart.CreateFormFile("files", "bootloader.bin")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := part.Write(content); err != nil {
			t.Fatal(err)
		}
		if err := multipart.Close(); err != nil {
			t.Fatal(err)
		}
		req := httptest.NewRequest(http.MethodPost, "https://anyusb/esp?op=flash&baud=460800", &dst)
		req.Header.Set("Content-Type", multipart.FormDataContentType())
		return service.Start(httptest.NewRecorder(), req, "trusted-client", "board", "same-request-id")
	}
	job, err := start(bootImage)
	if err != nil {
		t.Fatal(err)
	}
	if replay, err := start(bootImage); err != nil || replay.ID != job.ID {
		t.Fatalf("replay = %+v, %v", replay, err)
	}
	if _, err := start([]byte("different binary")); !errors.Is(err, platform.ErrConflict) {
		t.Fatalf("payload conflict = %v", err)
	}
	if _, err := service.Job("other-client", job.ID); !errors.Is(err, platform.ErrNotFound) {
		t.Fatalf("log ownership = %v", err)
	}
	frame := waitJob(t, service, "trusted-client", job.ID)
	logs := joinLogs(frame)
	if frame.Job.Status != "succeeded" || !strings.Contains(logs, "Chip: ESP32-S3") ||
		!strings.Contains(logs, "Verification successful (digest matched).") || opens.Load() != 1 || releases.Load() != 1 {
		t.Fatalf("unexpected flash job outcome: %+v\nlogs:\n%s", frame.Job, logs)
	}
	rom.mu.Lock()
	modeByte, sizeFreqByte := rom.flash[2], rom.flash[3]
	rom.mu.Unlock()
	if modeByte != 0 || (sizeFreqByte&0xF0) != 0x40 {
		t.Fatalf("patched bootloader flash header = (%02x, %02x), want (00, 4x)", modeByte, sizeFreqByte)
	}
}

func TestESPChipIDAndEraseAndVerifyMismatch(t *testing.T) {
	rom := newMockESP32S3Port(115200)
	service := NewWithOpener(t.Context(), t.TempDir(), func(_ string, baud int) (SerialPort, error) {
		return rom.reopen(baud), nil
	}, func(context.Context, string, string, string) (string, func(), error) {
		return "test-host-port", func() {}, nil
	})
	req := httptest.NewRequest(http.MethodPost, "https://anyusb/esp?op=chip-id", nil)
	job, err := service.Start(httptest.NewRecorder(), req, "trusted", "board", "chip-id-1")
	if err != nil {
		t.Fatal(err)
	}
	frame := waitJob(t, service, "trusted", job.ID)
	logs := joinLogs(frame)
	if frame.Job.Status != "succeeded" || !strings.Contains(logs, "MAC: cc:68:c7:8c:c4:24") || !strings.Contains(logs, "size=16MB") {
		t.Fatalf("chip-id = %+v\nlogs:\n%s", frame.Job, logs)
	}

	rom.mu.Lock()
	rom.flash[0x10000] = 0x42
	rom.mu.Unlock()
	eraseReq := httptest.NewRequest(http.MethodPost, "https://anyusb/esp?op=erase", nil)
	eraseJob, err := service.Start(httptest.NewRecorder(), eraseReq, "trusted", "board", "erase-1")
	if err != nil {
		t.Fatal(err)
	}
	if eraseFrame := waitJob(t, service, "trusted", eraseJob.ID); eraseFrame.Job.Status != "succeeded" {
		t.Fatalf("erase failed: %+v", eraseFrame.Job)
	}
	rom.mu.Lock()
	_, remaining := rom.flash[0x10000]
	rom.mu.Unlock()
	if remaining {
		t.Fatal("erase did not clear flash content")
	}

	rom.mu.Lock()
	rom.forceVerify = "00000000000000000000000000000000"
	rom.mu.Unlock()
	var dst bytes.Buffer
	mw := multipart.NewWriter(&dst)
	_ = mw.WriteField("manifest", `{"flash_files":{"0x10000":"app.bin"}}`)
	part, _ := mw.CreateFormFile("files", "app.bin")
	_, _ = part.Write([]byte("app payload"))
	_ = mw.Close()
	verifyReq := httptest.NewRequest(http.MethodPost, "https://anyusb/esp?op=verify", &dst)
	verifyReq.Header.Set("Content-Type", mw.FormDataContentType())
	verifyJob, err := service.Start(httptest.NewRecorder(), verifyReq, "trusted", "board", "verify-1")
	if err != nil {
		t.Fatal(err)
	}
	verifyFrame := waitJob(t, service, "trusted", verifyJob.ID)
	if verifyFrame.Job.Status != "failed" || !strings.Contains(verifyFrame.Job.Error, "verification failed") {
		t.Fatalf("expected verification mismatch failure, got %+v", verifyFrame.Job)
	}
}

type mockMonitorSerial struct {
	mu      sync.Mutex
	baud    int
	dtr     bool
	rts     bool
	closed  bool
	pending chan []byte
}

func newMockMonitorSerial(baud int) *mockMonitorSerial {
	ch := make(chan []byte, 8)
	ch <- []byte("BOOT")
	return &mockMonitorSerial{baud: baud, pending: ch}
}

func (m *mockMonitorSerial) Read(p []byte) (int, error) {
	select {
	case data, ok := <-m.pending:
		if !ok {
			return 0, io.EOF
		}
		n := copy(p, data)
		return n, nil
	case <-time.After(30 * time.Millisecond):
		return 0, nil
	}
}

func (m *mockMonitorSerial) Write(p []byte) (int, error) {
	m.mu.Lock()
	dtrByte, rtsByte := byte(0), byte(0)
	if m.dtr {
		dtrByte = 1
	}
	if m.rts {
		rtsByte = 1
	}
	baud := m.baud
	m.mu.Unlock()
	reply := append([]byte(nil), []byte(string(rune('0'))[:0])...)
	reply = append(reply, []byte(strings.Join([]string{strconvI(baud)}, ""))...)
	reply = append(reply, ':', dtrByte, rtsByte)
	reply = append(reply, p...)
	m.pending <- reply
	return len(p), nil
}

func strconvI(v int) string {
	var buf [20]byte
	i := len(buf)
	for v > 0 {
		i--
		buf[i] = byte('0' + v%10)
		v /= 10
	}
	return string(buf[i:])
}

func (m *mockMonitorSerial) Close() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if !m.closed {
		m.closed = true
		close(m.pending)
	}
	return nil
}
func (m *mockMonitorSerial) SetBaud(baud int) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.baud = baud
	return nil
}
func (m *mockMonitorSerial) SetDTR(state bool) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.dtr = state
	return nil
}
func (m *mockMonitorSerial) SetRTS(state bool) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.rts = state
	return nil
}
func (m *mockMonitorSerial) SetDTRAndRTS(dtr, rts bool) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.dtr, m.rts = dtr, rts
	return nil
}
func (m *mockMonitorSerial) SetReadTimeout(time.Duration) error { return nil }
func (m *mockMonitorSerial) Flush() error                       { return nil }

func TestSerialMonitorProtocol(t *testing.T) {
	released := make(chan struct{})
	service := NewWithOpener(t.Context(), t.TempDir(), func(_ string, baud int) (SerialPort, error) {
		return newMockMonitorSerial(baud), nil
	}, func(context.Context, string, string, string) (string, func(), error) {
		return "test-host-port", func() { close(released) }, nil
	})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := service.Monitor(w, r, "trusted", "board"); err != nil {
			http.Error(w, err.Error(), 500)
		}
	}))
	defer server.Close()
	ctx, cancel := context.WithTimeout(t.Context(), 8*time.Second)
	defer cancel()
	ws, _, err := websocket.Dial(ctx, server.URL+"?baud=115200", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer ws.CloseNow()
	read := func() MonitorMessage {
		_, raw, err := ws.Read(ctx)
		if err != nil {
			t.Fatal(err)
		}
		var message MonitorMessage
		if err := json.Unmarshal(raw, &message); err != nil || message.Error != "" {
			t.Fatalf("serial frame %s: %v", raw, err)
		}
		return message
	}
	if msg := read(); string(msg.Data) != "BOOT" {
		t.Fatalf("monitor output = %q", msg.Data)
	}
	on, off := true, false
	payload, _ := json.Marshal(MonitorMessage{Baud: 921600, DTR: &on, RTS: &off, Data: []byte("input")})
	if err := ws.Write(ctx, websocket.MessageText, payload); err != nil {
		t.Fatal(err)
	}
	if msg := read(); string(msg.Data) != "921600:\x01\x00input" {
		t.Fatalf("monitor settings or input = %q", msg.Data)
	}
	_ = ws.Close(websocket.StatusNormalClosure, "done")
	select {
	case <-released:
	case <-ctx.Done():
		t.Fatal("monitor did not release its lease")
	}
}
