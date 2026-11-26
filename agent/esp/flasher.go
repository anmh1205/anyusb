package esp

import (
	"bytes"
	"context"
	"crypto/md5"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"runtime"
	"strings"
	"time"
)

const (
	cmdFlashBegin     = 0x02
	cmdFlashData      = 0x03
	cmdFlashEnd       = 0x04
	cmdSync           = 0x08
	cmdWriteReg       = 0x09
	cmdReadReg        = 0x0A
	cmdSPISetParams   = 0x0B
	cmdSPIAttach      = 0x0D
	cmdReadFlashSlow  = 0x0E
	cmdChangeBaudrate = 0x0F
	cmdSPIFlashMD5    = 0x13
	cmdGetSecInfo     = 0x14

	flashSectorSize = 0x1000
	flashWriteSize  = 0x400
	espImageMagic   = 0xE9

	defaultCmdTimeout = 3 * time.Second
	syncTimeout       = 150 * time.Millisecond
)

var flashModeCodes = map[string]byte{
	"qio": 0, "qout": 1, "dio": 2, "dout": 3,
}

var detectedFlashSizes = map[uint32]struct {
	label string
	bytes uint32
}{
	0x12: {"256KB", 256 << 10},
	0x13: {"512KB", 512 << 10},
	0x14: {"1MB", 1 << 20},
	0x15: {"2MB", 2 << 20},
	0x16: {"4MB", 4 << 20},
	0x17: {"8MB", 8 << 20},
	0x18: {"16MB", 16 << 20},
	0x19: {"32MB", 32 << 20},
	0x1A: {"64MB", 64 << 20},
	0x1B: {"128MB", 128 << 20},
	0x1C: {"256MB", 256 << 20},
	0x20: {"64MB", 64 << 20},
	0x21: {"128MB", 128 << 20},
	0x22: {"256MB", 256 << 20},
	0x32: {"256KB", 256 << 10},
	0x33: {"512KB", 512 << 10},
	0x34: {"1MB", 1 << 20},
	0x35: {"2MB", 2 << 20},
	0x36: {"4MB", 4 << 20},
	0x37: {"8MB", 8 << 20},
	0x38: {"16MB", 16 << 20},
	0x39: {"32MB", 32 << 20},
	0x3A: {"64MB", 64 << 20},
}

var adestoFlashSizes = map[uint32]struct {
	label string
	bytes uint32
}{
	0x04: {"512KB", 512 << 10},
	0x05: {"1MB", 1 << 20},
	0x06: {"2MB", 2 << 20},
	0x07: {"4MB", 4 << 20},
	0x08: {"8MB", 8 << 20},
	0x09: {"16MB", 16 << 20},
}

var explicitFlashSizeBytes = map[string]uint32{
	"256KB": 256 << 10, "512KB": 512 << 10,
	"1MB": 1 << 20, "2MB": 2 << 20, "4MB": 4 << 20, "8MB": 8 << 20,
	"16MB": 16 << 20, "32MB": 32 << 20, "64MB": 64 << 20, "128MB": 128 << 20,
	"2MB-c1": 2 << 20, "4MB-c1": 4 << 20,
}

type jobConfig struct {
	chip      string
	before    string
	after     string
	flashMode string
	flashFreq string
	flashSize string
}

type espLoader struct {
	port       SerialPort
	opener     PortOpener
	portName   string
	baud       int
	slip       *slipStream
	chip       chipDef
	secureMode bool
	usbMode    string
	out        io.Writer
	errOut     io.Writer
}

func executeESPOperation(ctx context.Context, opener PortOpener, portName string, baud int, op string, manifest Manifest, segments []segment, stdout, stderr io.Writer) error {
	cfg, err := parseJobConfig(manifest)
	if err != nil {
		return err
	}
	port, err := opener(portName, 115200)
	if err != nil {
		return err
	}
	loader := &espLoader{
		port:     port,
		opener:   opener,
		portName: portName,
		baud:     115200,
		slip:     newSLIPStream(port),
		out:      stdout,
		errOut:   stderr,
	}
	defer func() {
		if loader.port != nil {
			_ = loader.port.Close()
		}
	}()

	if cfg.before == "no-reset-no-sync" {
		if cfg.chip != "auto" {
			chip, ok := chipByKey(cfg.chip)
			if !ok {
				return fmt.Errorf("unsupported ESP chip %q", cfg.chip)
			}
			loader.chip = chip
		} else if err := loader.detectChip(cfg.chip); err != nil {
			return err
		}
	} else {
		if err := loader.connectAndDetect(ctx, cfg.before, cfg.chip); err != nil {
			return err
		}
	}

	switch op {
	case "chip-id":
		if err := loader.reportChipIdentity(); err != nil {
			return err
		}
		if err := loader.attachAndInspectFlash(); err != nil {
			fmt.Fprintf(loader.errOut, "Warning: SPI flash probe failed: %v\n", err)
		}
	case "reset":
		if err := loader.attachAndInspectFlash(); err != nil {
			return err
		}
	case "erase":
		if err := loader.attachFlash(); err != nil {
			return err
		}
		sizeBytes, label, err := loader.configureFlashSize("detect")
		if err != nil {
			return err
		}
		if loader.chip.IsESP8266 {
			return fmt.Errorf("full-chip erase is not supported in ESP8266 ROM bootloader mode")
		}
		fmt.Fprintf(loader.out, "Erasing %s flash (%d bytes)...\n", label, sizeBytes)
		start := time.Now()
		if err := loader.flashBegin(sizeBytes, 0, 0, timeoutPerMB(30*time.Second, int(sizeBytes))); err != nil {
			return err
		}
		fmt.Fprintf(loader.out, "Flash erased in %.2fs.\n", time.Since(start).Seconds())
	case "flash", "verify":
		if baud != 115200 && !loader.secureMode {
			if err := loader.changeBaud(baud); err != nil {
				return err
			}
		}
		if err := loader.attachFlash(); err != nil {
			return err
		}
		configuredSize, _, err := loader.configureFlashSize(cfg.flashSize)
		if err != nil {
			return err
		}
		prepared, err := loader.prepareSegments(segments, cfg, configuredSize)
		if err != nil {
			return err
		}
		if op == "flash" {
			if err := loader.writeSegments(ctx, prepared); err != nil {
				return err
			}
		}
		if err := loader.verifySegments(ctx, prepared); err != nil {
			return err
		}
	default:
		return fmt.Errorf("unknown ESP operation %q", op)
	}

	return loader.finishAfter(cfg.after)
}

func (l *espLoader) logf(format string, args ...any) {
	_, _ = fmt.Fprintf(l.out, format+"\n", args...)
}

func (l *espLoader) connectAndDetect(ctx context.Context, before, requestedChip string) error {
	strategies := l.resetSequence(before, requestedChip)
	l.logf("Connecting to %s...", l.portName)
	var lastErr error
	attempts := 7
	if before == "no-reset" {
		attempts = 2
	}
	for i := 0; i < attempts; i++ {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		strategy := strategies[i%len(strategies)]
		if err := l.attemptConnect(ctx, strategy, before); err == nil {
			lastErr = nil
			break
		} else {
			lastErr = err
		}
	}
	if lastErr != nil {
		return fmt.Errorf("failed to connect to ESP target on %s: %w", l.portName, lastErr)
	}
	if err := l.detectChip(requestedChip); err != nil {
		return err
	}
	l.postConnect()
	return nil
}

func (l *espLoader) resetSequence(before, requestedChip string) []string {
	switch before {
	case "no-reset", "no-reset-no-sync":
		return []string{"none"}
	case "usb-reset":
		return []string{"usb-jtag"}
	}
	if requestedChip == "esp32" || requestedChip == "esp8266" {
		if runtime.GOOS != "windows" {
			return []string{"unix-tight", "classic"}
		}
		return []string{"classic"}
	}
	if runtime.GOOS != "windows" {
		return []string{"unix-tight", "usb-jtag", "classic"}
	}
	return []string{"usb-jtag", "classic"}
}

func (l *espLoader) attemptConnect(ctx context.Context, strategy, before string) error {
	if before != "no-reset" {
		_ = l.port.Flush()
		if err := l.runReset(strategy); err != nil {
			_ = l.reopenPort()
			return err
		}
		_ = drainAvailable(l.port)
	}
	for i := 0; i < 5; i++ {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		_ = l.port.Flush()
		l.slip.reset()
		if err := l.sync(); err == nil {
			return nil
		}
		time.Sleep(50 * time.Millisecond)
	}
	return fmt.Errorf("no valid SLIP SYNC reply")
}

func (l *espLoader) reopenPort() error {
	if l.port != nil {
		_ = l.port.Close()
		l.port = nil
	}
	time.Sleep(250 * time.Millisecond)
	port, err := l.opener(l.portName, l.baud)
	if err != nil {
		return err
	}
	l.port = port
	l.slip = newSLIPStream(port)
	return nil
}

func (l *espLoader) runReset(strategy string) error {
	switch strategy {
	case "none":
		return nil
	case "classic":
		if err := l.port.SetDTR(false); err != nil {
			return err
		}
		if err := l.port.SetRTS(true); err != nil {
			return err
		}
		time.Sleep(100 * time.Millisecond)
		if err := l.port.SetDTR(true); err != nil {
			return err
		}
		if err := l.port.SetRTS(false); err != nil {
			return err
		}
		time.Sleep(50 * time.Millisecond)
		return l.port.SetDTR(false)
	case "unix-tight":
		if err := l.port.SetDTRAndRTS(false, false); err != nil {
			return err
		}
		if err := l.port.SetDTRAndRTS(true, true); err != nil {
			return err
		}
		if err := l.port.SetDTRAndRTS(false, true); err != nil {
			return err
		}
		time.Sleep(100 * time.Millisecond)
		if err := l.port.SetDTRAndRTS(true, false); err != nil {
			return err
		}
		time.Sleep(50 * time.Millisecond)
		if err := l.port.SetDTRAndRTS(false, false); err != nil {
			return err
		}
		return l.port.SetDTR(false)
	case "usb-jtag":
		if err := l.port.SetRTS(false); err != nil {
			return err
		}
		if err := l.port.SetDTR(false); err != nil {
			return err
		}
		time.Sleep(100 * time.Millisecond)
		if err := l.port.SetDTR(true); err != nil {
			return err
		}
		if err := l.port.SetRTS(false); err != nil {
			return err
		}
		time.Sleep(100 * time.Millisecond)
		if err := l.port.SetRTS(true); err != nil {
			return err
		}
		if err := l.port.SetDTR(false); err != nil {
			return err
		}
		if err := l.port.SetRTS(true); err != nil {
			return err
		}
		time.Sleep(100 * time.Millisecond)
		if err := l.port.SetDTR(false); err != nil {
			return err
		}
		return l.port.SetRTS(false)
	default:
		return nil
	}
}

func (l *espLoader) sync() error {
	payload := make([]byte, 36)
	payload[0], payload[1], payload[2], payload[3] = 0x07, 0x07, 0x12, 0x20
	for i := 4; i < len(payload); i++ {
		payload[i] = 0x55
	}
	if _, _, err := l.slip.checkCommand("sync", cmdSync, payload, 0, 0, syncTimeout); err != nil {
		return err
	}
	// ROM loaders emit 7 additional sync replies; drain any available ones without failing if fewer arrive.
	for i := 0; i < 7; i++ {
		if _, _, err := l.slip.command(0, nil, 0, true, 30*time.Millisecond); err != nil {
			break
		}
	}
	return nil
}

func (l *espLoader) readReg(addr uint32) (uint32, error) {
	var arg [4]byte
	binary.LittleEndian.PutUint32(arg[:], addr)
	val, _, err := l.slip.checkCommand("read register", cmdReadReg, arg[:], 0, 0, defaultCmdTimeout)
	return val, err
}

func (l *espLoader) writeReg(addr, value, mask, delayUS uint32) error {
	var arg [16]byte
	binary.LittleEndian.PutUint32(arg[0:4], addr)
	binary.LittleEndian.PutUint32(arg[4:8], value)
	binary.LittleEndian.PutUint32(arg[8:12], mask)
	binary.LittleEndian.PutUint32(arg[12:16], delayUS)
	_, _, err := l.slip.checkCommand("write register", cmdWriteReg, arg[:], 0, 0, defaultCmdTimeout)
	return err
}

func (l *espLoader) getSecurityInfo() (chipID int, secureDL bool, ok bool, err error) {
	_, data, err := l.slip.checkCommand("get security info", cmdGetSecInfo, nil, 0, 20, defaultCmdTimeout)
	if err == nil && len(data) == 20 {
		flags := binary.LittleEndian.Uint32(data[0:4])
		id := int(binary.LittleEndian.Uint32(data[12:16]))
		return id, (flags & (1 << 2)) != 0, true, nil
	}
	if errors.Is(err, errUnsupportedCommand) {
		return 0, false, false, nil
	}
	// ESP32-S2 returns a 12-byte security-info payload without chip_id.
	_, s2Data, s2Err := l.slip.checkCommand("get security info", cmdGetSecInfo, nil, 0, 12, defaultCmdTimeout)
	if s2Err == nil && len(s2Data) == 12 {
		flags := binary.LittleEndian.Uint32(s2Data[0:4])
		return 2, (flags & (1 << 2)) != 0, true, nil
	}
	if errors.Is(s2Err, errUnsupportedCommand) {
		return 0, false, false, nil
	}
	if err != nil {
		return 0, false, false, err
	}
	return 0, false, false, s2Err
}

func (l *espLoader) detectChip(requested string) error {
	if chipID, secureDL, hasInfo, err := l.getSecurityInfo(); err == nil && hasInfo {
		if chip, found := chipByID(chipID); found {
			l.chip, l.secureMode = chip, secureDL
			return l.validateRequestedChip(requested)
		}
	}
	const magicReg = 0x40001000
	magic, err := l.readReg(magicReg)
	if err != nil {
		if errors.Is(err, errUnsupportedCommand) {
			if chip, found := chipByKey("esp32s2"); found {
				l.chip, l.secureMode = chip, true
				return l.validateRequestedChip(requested)
			}
		}
		return fmt.Errorf("detect chip type: %w", err)
	}
	chip, found := chipByMagic(magic)
	if !found {
		return fmt.Errorf("unrecognized ESP chip magic 0x%08x", magic)
	}
	l.chip = chip
	return l.validateRequestedChip(requested)
}

func (l *espLoader) validateRequestedChip(requested string) error {
	l.logf("Chip: %s", l.chip.Name)
	if requested == "" || requested == "auto" {
		return nil
	}
	if !strings.EqualFold(requested, l.chip.Key) {
		return fmt.Errorf("detected %s, expected %s", l.chip.Name, requested)
	}
	return nil
}

func (l *espLoader) uartNo() (uint32, bool) {
	if l.secureMode || l.chip.UARTDevBufNoReg == 0 {
		return 0, false
	}
	reg := l.chip.UARTDevBufNoReg
	if l.chip.UARTDevBufNoRegRev != 0 && l.usesRevisedUARTBufReg() {
		reg = l.chip.UARTDevBufNoRegRev
	}
	val, err := l.readReg(reg)
	if err != nil {
		return 0, false
	}
	return val & 0xFF, true
}

func (l *espLoader) usesRevisedUARTBufReg() bool {
	if l.chip.EFuseBase == 0 {
		return false
	}
	switch l.chip.Key {
	case "esp32c3":
		block1 := l.chip.EFuseBase + 0x44
		w3, err1 := l.readReg(block1 + 12)
		w5, err2 := l.readReg(block1 + 20)
		if err1 != nil || err2 != nil {
			return false
		}
		major := (w5 >> 24) & 0x3
		minor := (((w5 >> 23) & 0x1) << 3) | ((w3 >> 18) & 0x7)
		return major*100+minor >= 101
	case "esp32p4":
		w2, err := l.readReg(l.chip.EFuseBase + 0x44 + 8)
		if err != nil {
			return false
		}
		major := (((w2 >> 23) & 1) << 2) | ((w2 >> 4) & 0x3)
		minor := w2 & 0xF
		return major*100+minor >= 300
	case "esp32c61":
		w2, err := l.readReg(l.chip.EFuseBase + 0x44 + 8)
		if err != nil {
			return false
		}
		major := (w2 >> 4) & 0x3
		minor := w2 & 0xF
		return major*100+minor > 2
	default:
		return false
	}
}

func (l *espLoader) postConnect() {
	uart, ok := l.uartNo()
	if ok {
		if l.chip.UARTDevBufNoUSBJTAG != 0 && uart == l.chip.UARTDevBufNoUSBJTAG {
			l.usbMode = "USB-Serial/JTAG"
			l.disableWatchdogs()
		} else if l.chip.UARTDevBufNoUSBOTG != 0 && uart == l.chip.UARTDevBufNoUSBOTG {
			l.usbMode = "USB-OTG"
		}
	}
	if l.usbMode != "" {
		l.logf("Transport: %s", l.usbMode)
	}
}

func (l *espLoader) disableWatchdogs() {
	if l.chip.WDTWProtectReg != 0 {
		_ = l.writeReg(l.chip.WDTWProtectReg, l.chip.WDTWKey, 0xFFFFFFFF, 0)
		_ = l.writeReg(l.chip.WDTConfig0Reg, 0, 0xFFFFFFFF, 0)
		_ = l.writeReg(l.chip.WDTWProtectReg, 0, 0xFFFFFFFF, 0)
	}
	if l.chip.SWDWProtectReg != 0 && l.chip.SWDConfReg != 0 {
		_ = l.writeReg(l.chip.SWDWProtectReg, l.chip.SWDWKey, 0xFFFFFFFF, 0)
		if val, err := l.readReg(l.chip.SWDConfReg); err == nil {
			_ = l.writeReg(l.chip.SWDConfReg, val|l.chip.SWDAutoFeedEn, 0xFFFFFFFF, 0)
		}
		_ = l.writeReg(l.chip.SWDWProtectReg, 0, 0xFFFFFFFF, 0)
	}
}

func (l *espLoader) readMAC() (string, error) {
	if l.chip.IsESP8266 {
		mac0, err0 := l.readReg(0x3FF00050)
		mac1, err1 := l.readReg(0x3FF00054)
		mac3, err3 := l.readReg(0x3FF0005C)
		if err0 != nil || err1 != nil || err3 != nil {
			return "", fmt.Errorf("read ESP8266 OTP MAC failed")
		}
		var b [6]byte
		if mac3 != 0 {
			b[0], b[1], b[2] = byte(mac3>>16), byte(mac3>>8), byte(mac3)
		} else if ((mac1 >> 16) & 0xFF) == 0 {
			b[0], b[1], b[2] = 0x18, 0xFE, 0x34
		} else {
			b[0], b[1], b[2] = 0xAC, 0xD0, 0x74
		}
		b[3], b[4], b[5] = byte(mac1>>8), byte(mac1), byte(mac0>>24)
		return formatMAC(b), nil
	}
	if l.chip.Key == "esp32" {
		w2, err2 := l.readReg(l.chip.EFuseBlock0Base + 8)
		w1, err1 := l.readReg(l.chip.EFuseBlock0Base + 4)
		if err1 != nil || err2 != nil {
			return "", fmt.Errorf("read ESP32 eFuse MAC failed")
		}
		b := [6]byte{byte(w2 >> 8), byte(w2), byte(w1 >> 24), byte(w1 >> 16), byte(w1 >> 8), byte(w1)}
		return formatMAC(b), nil
	}
	mac0, err0 := l.readReg(l.chip.MACEFuseReg)
	mac1, err1 := l.readReg(l.chip.MACEFuseReg + 4)
	if err0 != nil || err1 != nil {
		return "", fmt.Errorf("read eFuse MAC failed")
	}
	b := [6]byte{byte(mac1 >> 8), byte(mac1), byte(mac0 >> 24), byte(mac0 >> 16), byte(mac0 >> 8), byte(mac0)}
	return formatMAC(b), nil
}

func formatMAC(b [6]byte) string {
	return fmt.Sprintf("%02x:%02x:%02x:%02x:%02x:%02x", b[0], b[1], b[2], b[3], b[4], b[5])
}

func (l *espLoader) reportChipIdentity() error {
	mac, err := l.readMAC()
	if err != nil {
		return err
	}
	l.logf("MAC: %s", mac)
	return nil
}

func (l *espLoader) attachFlash() error {
	if l.chip.IsESP8266 {
		return l.flashBegin(0, 0, 0, defaultCmdTimeout)
	}
	var hspi uint32
	if l.chip.HasLegacySPIPads && !l.secureMode {
		r5, err5 := l.readReg(l.chip.EFuseBase + 0x14)
		r3, err3 := l.readReg(l.chip.EFuseBase + 0x0C)
		if err5 == nil && err3 == nil {
			clk := r5 & 0x1F
			q := (r5 >> 5) & 0x1F
			d := (r5 >> 10) & 0x1F
			cs := (r5 >> 15) & 0x1F
			hd := (r3 >> 4) & 0x1F
			hspi = (hd << 24) | (cs << 18) | (d << 12) | (q << 6) | clk
		}
	}
	var arg [8]byte
	binary.LittleEndian.PutUint32(arg[0:4], hspi)
	_, _, err := l.slip.checkCommand("attach SPI flash", cmdSPIAttach, arg[:], 0, 0, defaultCmdTimeout)
	return err
}

func (l *espLoader) runSPIFlashCommand(cmd byte, readBits uint32) (uint32, error) {
	if !l.chip.HasSPIDlenRegs {
		return 0, fmt.Errorf("SPI user command is unsupported on %s", l.chip.Name)
	}
	const (
		spiUsrCommand = 1 << 31
		spiUsrMiso    = 1 << 28
		spiCmdUsr     = 1 << 18
		spiCmdLenPos  = 28
	)
	base := l.chip.SPIRegBase
	cmdReg := base + 0x00
	usrReg := base + l.chip.SPIUsrOffs
	usr2Reg := base + l.chip.SPIUsr2Offs
	w0Reg := base + l.chip.SPIW0Offs
	misoDlenReg := base + l.chip.SPIMisoDlenOffs

	oldUsr, err := l.readReg(usrReg)
	if err != nil {
		return 0, err
	}
	oldUsr2, err := l.readReg(usr2Reg)
	if err != nil {
		return 0, err
	}
	flags := uint32(spiUsrCommand)
	if readBits > 0 {
		flags |= spiUsrMiso
		if err := l.writeReg(misoDlenReg, readBits-1, 0xFFFFFFFF, 0); err != nil {
			return 0, err
		}
	}
	if err := l.writeReg(usrReg, flags, 0xFFFFFFFF, 0); err != nil {
		return 0, err
	}
	if err := l.writeReg(usr2Reg, (7<<spiCmdLenPos)|uint32(cmd), 0xFFFFFFFF, 0); err != nil {
		return 0, err
	}
	_ = l.writeReg(w0Reg, 0, 0xFFFFFFFF, 0)
	if err := l.writeReg(cmdReg, spiCmdUsr, 0xFFFFFFFF, 0); err != nil {
		return 0, err
	}
	done := false
	for i := 0; i < 10; i++ {
		val, err := l.readReg(cmdReg)
		if err != nil {
			return 0, err
		}
		if val&spiCmdUsr == 0 {
			done = true
			break
		}
	}
	if !done {
		return 0, fmt.Errorf("SPI flash command 0x%02x timed out", cmd)
	}
	result, err := l.readReg(w0Reg)
	_ = l.writeReg(usrReg, oldUsr, 0xFFFFFFFF, 0)
	_ = l.writeReg(usr2Reg, oldUsr2, 0xFFFFFFFF, 0)
	return result, err
}

func (l *espLoader) readFlashInfo() (vendorID, deviceID uint32, sizeLabel string, sizeBytes uint32, err error) {
	raw, err := l.runSPIFlashCommand(0x9F, 24)
	if err != nil {
		return 0, 0, "", 0, err
	}
	vendorID = raw & 0xFF
	deviceID = ((raw >> 16) & 0xFF) | (((raw >> 8) & 0xFF) << 8)
	if vendorID == 0x1F {
		if entry, ok := adestoFlashSizes[(raw>>8)&0x1F]; ok {
			return vendorID, deviceID, entry.label, entry.bytes, nil
		}
		return vendorID, deviceID, "", 0, nil
	}
	if entry, ok := detectedFlashSizes[(raw>>16)&0xFF]; ok {
		return vendorID, deviceID, entry.label, entry.bytes, nil
	}
	return vendorID, deviceID, "", 0, nil
}

func (l *espLoader) attachAndInspectFlash() error {
	if err := l.attachFlash(); err != nil {
		return err
	}
	if !l.chip.HasSPIDlenRegs || l.secureMode {
		return nil
	}
	vendor, dev, sizeLabel, _, err := l.readFlashInfo()
	if err != nil {
		return err
	}
	if sizeLabel == "" {
		sizeLabel = "Unknown"
	}
	l.logf("Flash: manufacturer=%02x device=%04x size=%s", vendor, dev, sizeLabel)
	return nil
}

func (l *espLoader) configureFlashSize(requested string) (uint32, string, error) {
	if l.chip.IsESP8266 {
		if requested != "" && requested != "keep" && requested != "detect" {
			return explicitFlashSizeBytes[requested], requested, nil
		}
		return 4 << 20, "4MB", nil
	}
	var sizeBytes uint32
	label := requested
	if requested == "" || requested == "keep" || requested == "detect" {
		if !l.secureMode && l.chip.HasSPIDlenRegs {
			_, _, detectedLabel, detectedBytes, err := l.readFlashInfo()
			if err == nil && detectedBytes > 0 {
				sizeBytes, label = detectedBytes, detectedLabel
			}
		}
		if sizeBytes == 0 {
			sizeBytes, label = 4<<20, "4MB"
		}
	} else {
		sizeBytes = explicitFlashSizeBytes[requested]
	}
	if sizeBytes > 0 {
		var arg [24]byte
		binary.LittleEndian.PutUint32(arg[0:4], 0)
		binary.LittleEndian.PutUint32(arg[4:8], sizeBytes)
		binary.LittleEndian.PutUint32(arg[8:12], 64*1024)
		binary.LittleEndian.PutUint32(arg[12:16], 4*1024)
		binary.LittleEndian.PutUint32(arg[16:20], 256)
		binary.LittleEndian.PutUint32(arg[20:24], 0xFFFF)
		if _, _, err := l.slip.checkCommand("set SPI flash parameters", cmdSPISetParams, arg[:], 0, 0, defaultCmdTimeout); err != nil {
			return 0, "", err
		}
		l.logf("Configured flash size: %s", label)
	}
	return sizeBytes, label, nil
}

func (l *espLoader) changeBaud(baud int) error {
	if baud <= 0 || baud == l.baud || l.chip.IsESP8266 {
		return nil
	}
	targetBaud := uint32(baud)
	if l.chip.Key == "esp32" {
		if adjusted, ok := l.esp32AdjustedBaud(baud); ok {
			targetBaud = adjusted
		}
	}
	l.logf("Changing baud rate to %d...", baud)
	var arg [8]byte
	binary.LittleEndian.PutUint32(arg[0:4], targetBaud)
	binary.LittleEndian.PutUint32(arg[4:8], 0)
	if _, _, err := l.slip.command(cmdChangeBaudrate, arg[:], 0, true, defaultCmdTimeout); err != nil {
		return fmt.Errorf("change baud rate to %d: %w", baud, err)
	}
	if err := l.port.SetBaud(baud); err != nil {
		return fmt.Errorf("apply host baud rate %d: %w", baud, err)
	}
	l.baud = baud
	time.Sleep(50 * time.Millisecond)
	_ = l.port.Flush()
	l.slip.reset()
	return nil
}

func (l *espLoader) esp32AdjustedBaud(baud int) (uint32, bool) {
	const rtcCaliCfg1 = 0x3FF5F06C
	rawCali, err1 := l.readReg(rtcCaliCfg1)
	clk8M, err2 := l.readReg(l.chip.EFuseBlock0Base + 16)
	if err1 != nil || err2 != nil {
		return 0, false
	}
	caliVal := (rawCali >> 7) & 0x01FFFFFF
	romFreq := float64(caliVal) * 15625 * float64(clk8M&0xFF) / 40.0
	if romFreq <= 0 {
		return 0, false
	}
	validFreq := 26_000_000.0
	if romFreq > 33_000_000.0 {
		validFreq = 40_000_000.0
	}
	return uint32(float64(baud) * romFreq / validFreq), true
}

type preparedSegment struct {
	name   string
	offset uint32
	data   []byte
	md5Hex string
}

func (l *espLoader) prepareSegments(segments []segment, cfg jobConfig, configuredFlashSize uint32) ([]preparedSegment, error) {
	out := make([]preparedSegment, 0, len(segments))
	for _, seg := range segments {
		raw, err := readSegmentData(seg.Path)
		if err != nil {
			return nil, err
		}
		if len(raw) == 0 {
			continue
		}
		if err := l.validateAndPatchBootloader(uint32(seg.Offset), &raw, cfg); err != nil {
			return nil, err
		}
		if pad := len(raw) % 4; pad != 0 {
			raw = append(raw, bytes.Repeat([]byte{0xFF}, 4-pad)...)
		}
		offset := uint32(seg.Offset)
		if over := offset % flashSectorSize; over != 0 {
			offset -= over
			raw = append(bytes.Repeat([]byte{0xFF}, int(over)), raw...)
		}
		if configuredFlashSize > 0 && uint64(offset)+uint64(len(raw)) > uint64(configuredFlashSize) {
			return nil, fmt.Errorf("%s at 0x%x (%d bytes) exceeds flash size (%d bytes)", seg.Name, offset, len(raw), configuredFlashSize)
		}
		sum := md5.Sum(raw)
		out = append(out, preparedSegment{
			name:   seg.Name,
			offset: offset,
			data:   raw,
			md5Hex: hex.EncodeToString(sum[:]),
		})
	}
	return out, nil
}

func readSegmentData(path string) ([]byte, error) {
	file, err := openReadOnly(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	return io.ReadAll(file)
}

func (l *espLoader) validateAndPatchBootloader(address uint32, raw *[]byte, cfg jobConfig) error {
	data := *raw
	if len(data) < 24 || data[0] != espImageMagic {
		return nil
	}
	if !l.chip.IsESP8266 && l.chip.ImageChipID >= 0 {
		imageChip := int(binary.LittleEndian.Uint16(data[12:14]))
		if imageChip != l.chip.ImageChipID {
			if matched, ok := chipByID(imageChip); ok {
				return fmt.Errorf("image is for %s, connected target is %s", matched.Name, l.chip.Name)
			}
		}
	}
	if address != l.chip.BootloaderOffset || l.secureMode {
		return nil
	}
	if (cfg.flashMode == "" || cfg.flashMode == "keep") &&
		(cfg.flashFreq == "" || cfg.flashFreq == "keep") &&
		(cfg.flashSize == "" || cfg.flashSize == "keep" || cfg.flashSize == "detect") {
		return nil
	}
	patched := append([]byte(nil), data...)
	if cfg.flashMode != "" && cfg.flashMode != "keep" {
		if code, ok := flashModeCodes[cfg.flashMode]; ok {
			patched[2] = code
		}
	}
	if cfg.flashFreq != "" && cfg.flashFreq != "keep" {
		if code, ok := l.chip.FlashFreqs[cfg.flashFreq]; ok {
			patched[3] = (patched[3] & 0xF0) | (code & 0x0F)
		}
	}
	if cfg.flashSize != "" && cfg.flashSize != "keep" && cfg.flashSize != "detect" {
		if code, ok := l.chip.FlashSizes[cfg.flashSize]; ok {
			patched[3] = (patched[3] & 0x0F) | (code & 0xF0)
		}
	}
	if bytes.Equal(patched[2:4], data[2:4]) {
		return nil
	}
	if !l.chip.IsESP8266 && data[23] == 1 {
		if dataLen, ok := espImageDataLength(data); ok && dataLen+32 <= len(patched) {
			origSHA := sha256.Sum256(data[:dataLen])
			if bytes.Equal(origSHA[:], data[dataLen:dataLen+32]) {
				updatedSHA := sha256.Sum256(patched[:dataLen])
				copy(patched[dataLen:dataLen+32], updatedSHA[:])
			}
		}
	}
	*raw = patched
	return nil
}

func espImageDataLength(data []byte) (int, bool) {
	if len(data) < 24 || data[0] != espImageMagic {
		return 0, false
	}
	segCount := int(data[1])
	pos := 24
	for i := 0; i < segCount; i++ {
		if pos+8 > len(data) {
			return 0, false
		}
		segLen := int(binary.LittleEndian.Uint32(data[pos+4 : pos+8]))
		pos += 8
		if segLen < 0 || pos+segLen > len(data) {
			return 0, false
		}
		pos += segLen
	}
	pos += 15 - (pos % 16)
	pos++
	if pos > len(data) {
		return 0, false
	}
	return pos, true
}

func (l *espLoader) writeSegments(ctx context.Context, segments []preparedSegment) error {
	for _, seg := range segments {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		l.logf("Writing %s (%d bytes) at 0x%08x...", seg.name, len(seg.data), seg.offset)
		start := time.Now()
		blocks := (len(seg.data) + flashWriteSize - 1) / flashWriteSize
		eraseSize := uint32(len(seg.data))
		if l.chip.IsESP8266 {
			eraseSize = esp8266EraseSize(seg.offset, uint32(len(seg.data)))
		}
		if err := l.flashBegin(eraseSize, uint32(blocks), seg.offset, timeoutPerMB(30*time.Second, len(seg.data))); err != nil {
			return err
		}
		lastLoggedPct := -1
		for seq := 0; seq < blocks; seq++ {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			begin := seq * flashWriteSize
			end := min(begin+flashWriteSize, len(seg.data))
			block := make([]byte, flashWriteSize)
			copy(block, seg.data[begin:end])
			for i := end - begin; i < len(block); i++ {
				block[i] = 0xFF
			}
			if err := l.flashDataBlock(block, uint32(seq)); err != nil {
				return err
			}
			pct := ((seq + 1) * 100) / blocks
			if pct == 100 || pct-lastLoggedPct >= 10 {
				l.logf("Writing at 0x%08x... (%d%%)", seg.offset+uint32(begin), pct)
				lastLoggedPct = pct
			}
		}
		l.logf("Wrote %d bytes at 0x%08x in %.2fs.", len(seg.data), seg.offset, time.Since(start).Seconds())
	}
	return nil
}

func (l *espLoader) flashBegin(eraseSize, blocks, offset uint32, timeout time.Duration) error {
	size := 16
	if l.chip.ExtendedFlashBegin {
		size = 20
	}
	arg := make([]byte, size)
	binary.LittleEndian.PutUint32(arg[0:4], eraseSize)
	binary.LittleEndian.PutUint32(arg[4:8], blocks)
	binary.LittleEndian.PutUint32(arg[8:12], flashWriteSize)
	binary.LittleEndian.PutUint32(arg[12:16], offset)
	if l.chip.ExtendedFlashBegin {
		binary.LittleEndian.PutUint32(arg[16:20], 0)
	}
	_, _, err := l.slip.checkCommand("enter flash download mode", cmdFlashBegin, arg, 0, 0, timeout)
	return err
}

func (l *espLoader) flashDataBlock(block []byte, seq uint32) error {
	arg := make([]byte, 16+len(block))
	binary.LittleEndian.PutUint32(arg[0:4], uint32(len(block)))
	binary.LittleEndian.PutUint32(arg[4:8], seq)
	copy(arg[16:], block)
	chk := xorChecksum(block)
	var lastErr error
	for attempt := 0; attempt < 3; attempt++ {
		if _, _, err := l.slip.checkCommand(fmt.Sprintf("write flash block %d", seq), cmdFlashData, arg, chk, 0, defaultCmdTimeout); err == nil {
			return nil
		} else {
			lastErr = err
		}
	}
	return lastErr
}

func (l *espLoader) verifySegments(ctx context.Context, segments []preparedSegment) error {
	for _, seg := range segments {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if l.chip.IsESP8266 {
			return fmt.Errorf("flash verification is unsupported in ESP8266 ROM mode")
		}
		var actual string
		var err error
		if l.chip.SupportsFlashMD5 {
			actual, err = l.flashMD5(seg.offset, uint32(len(seg.data)))
		} else {
			actual, err = l.readFlashMD5Slow(ctx, seg.offset, uint32(len(seg.data)))
		}
		if err != nil {
			return err
		}
		if !strings.EqualFold(actual, seg.md5Hex) {
			return fmt.Errorf("verification failed at 0x%08x (%s): expected %s, flash reported %s", seg.offset, seg.name, seg.md5Hex, actual)
		}
		l.logf("Hash of data verified for %s (0x%08x).", seg.name, seg.offset)
	}
	l.logf("Verification successful (digest matched).")
	return nil
}

func (l *espLoader) flashMD5(offset, size uint32) (string, error) {
	var arg [16]byte
	binary.LittleEndian.PutUint32(arg[0:4], offset)
	binary.LittleEndian.PutUint32(arg[4:8], size)
	timeout := timeoutPerMB(8*time.Second, int(size))
	_, resp, err := l.slip.checkCommand("calculate flash MD5", cmdSPIFlashMD5, arg[:], 0, 32, timeout)
	if err != nil {
		return "", err
	}
	return strings.ToLower(string(resp)), nil
}

func (l *espLoader) readFlashMD5Slow(ctx context.Context, offset, size uint32) (string, error) {
	const blockLen = 64
	h := md5.New()
	var read uint32
	for read < size {
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
		chunk := min(uint32(blockLen), size-read)
		var arg [8]byte
		binary.LittleEndian.PutUint32(arg[0:4], offset+read)
		binary.LittleEndian.PutUint32(arg[4:8], chunk)
		_, data, err := l.slip.checkCommand("read flash block", cmdReadFlashSlow, arg[:], 0, blockLen, defaultCmdTimeout)
		if err != nil {
			return "", err
		}
		_, _ = h.Write(data[:chunk])
		read += chunk
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func (l *espLoader) finishAfter(after string) error {
	switch after {
	case "no-reset", "no-reset-stub":
		return nil
	case "soft-reset":
		l.logf("Soft resetting...")
		if err := l.flashBegin(0, 0, 0, defaultCmdTimeout); err != nil {
			return err
		}
		var arg [4]byte
		binary.LittleEndian.PutUint32(arg[:], 0)
		_, _, err := l.slip.checkCommand("soft reset", cmdFlashEnd, arg[:], 0, 0, defaultCmdTimeout)
		return err
	case "watchdog-reset":
		return l.watchdogReset()
	case "hard-reset", "":
		if l.chip.RTCOption1Reg != 0 && l.chip.RTCForceDownloadMask != 0 {
			_ = l.writeReg(l.chip.RTCOption1Reg, 0, l.chip.RTCForceDownloadMask, 0)
		}
		if l.usbMode == "USB-OTG" && l.chip.GPIOStrapReg != 0 && l.chip.RTCOption1Reg != 0 {
			strap, err1 := l.readReg(l.chip.GPIOStrapReg)
			force, err2 := l.readReg(l.chip.RTCOption1Reg)
			if err1 == nil && err2 == nil &&
				(strap&l.chip.GPIOStrapSPIBootMask) == 0 &&
				(force&l.chip.RTCForceDownloadMask) == 0 {
				return l.watchdogReset()
			}
		}
		l.logf("Hard resetting via RTS pin...")
		if err := l.port.SetRTS(true); err != nil {
			return err
		}
		if l.usbMode == "USB-OTG" {
			time.Sleep(200 * time.Millisecond)
			if err := l.port.SetRTS(false); err != nil {
				return err
			}
			time.Sleep(200 * time.Millisecond)
			return nil
		}
		time.Sleep(100 * time.Millisecond)
		return l.port.SetRTS(false)
	default:
		return fmt.Errorf("unsupported after-reset action %q", after)
	}
}

func (l *espLoader) watchdogReset() error {
	if l.chip.WDTWProtectReg == 0 {
		return l.finishAfter("hard-reset")
	}
	l.logf("Hard resetting with watchdog...")
	_ = l.writeReg(l.chip.WDTWProtectReg, l.chip.WDTWKey, 0xFFFFFFFF, 0)
	_ = l.writeReg(l.chip.WDTConfig1Reg, 2000, 0xFFFFFFFF, 0)
	_ = l.writeReg(l.chip.WDTConfig0Reg, (1<<31)|(5<<28)|(1<<8)|2, 0xFFFFFFFF, 0)
	_ = l.writeReg(l.chip.WDTWProtectReg, 0, 0xFFFFFFFF, 0)
	time.Sleep(300 * time.Millisecond)
	return nil
}

func esp8266EraseSize(offset, size uint32) uint32 {
	const sectorsPerBlock = 16
	numSectors := (size + flashSectorSize - 1) / flashSectorSize
	startSector := offset / flashSectorSize
	headSectors := sectorsPerBlock - (startSector % sectorsPerBlock)
	if numSectors < headSectors {
		headSectors = numSectors
	}
	if numSectors < 2*headSectors {
		return ((numSectors + 1) / 2) * flashSectorSize
	}
	return (numSectors - headSectors) * flashSectorSize
}

func timeoutPerMB(perMB time.Duration, sizeBytes int) time.Duration {
	if sizeBytes <= 0 {
		return defaultCmdTimeout
	}
	scaled := time.Duration(float64(perMB) * float64(sizeBytes) / 1_000_000)
	if scaled < defaultCmdTimeout {
		return defaultCmdTimeout
	}
	return scaled
}
