package esp

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"
)

const (
	slipFrameEnd = 0xC0
	slipEscape   = 0xDB
	slipEscEnd   = 0xDC
	slipEscEsc   = 0xDD

	romDirectionRequest  = 0x00
	romDirectionResponse = 0x01

	romInvalidRecvMsg = 0x05
	checksumSeed      = 0xEF
)

var errUnsupportedCommand = errors.New("unsupported bootloader command")

type slipError struct {
	op     byte
	status byte
	code   byte
	desc   string
}

func (e *slipError) Error() string {
	return fmt.Sprintf("failed to %s (op=0x%02x status=0x%02x error=0x%02x: %s)", e.desc, e.op, e.status, e.code, romErrorText(e.code))
}

func romErrorText(code byte) string {
	switch code {
	case 0x05:
		return "received message is invalid"
	case 0x06:
		return "failed to act on received message"
	case 0x07:
		return "invalid CRC in message"
	case 0x08:
		return "flash write error"
	case 0x09:
		return "flash read error"
	case 0x0a:
		return "flash read length error"
	case 0x0b:
		return "deflate error"
	default:
		return "bootloader error"
	}
}

func encodeSLIP(payload []byte) []byte {
	out := make([]byte, 0, len(payload)+8)
	out = append(out, slipFrameEnd)
	for _, b := range payload {
		switch b {
		case slipFrameEnd:
			out = append(out, slipEscape, slipEscEnd)
		case slipEscape:
			out = append(out, slipEscape, slipEscEsc)
		default:
			out = append(out, b)
		}
	}
	out = append(out, slipFrameEnd)
	return out
}

func xorChecksum(data []byte) uint32 {
	state := byte(checksumSeed)
	for _, b := range data {
		state ^= b
	}
	return uint32(state)
}

type slipStream struct {
	port    SerialPort
	buf     []byte
	scratch [4096]byte
}

func newSLIPStream(port SerialPort) *slipStream {
	return &slipStream{port: port}
}

func (s *slipStream) reset() {
	s.buf = nil
}

func (s *slipStream) readPacket(timeout time.Duration) ([]byte, error) {
	deadline := time.Now().Add(timeout)
	_ = s.port.SetReadTimeout(minDuration(timeout, 50*time.Millisecond))
	var packet []byte
	started := false
	inEscape := false
	for {
		if len(s.buf) == 0 {
			rem := time.Until(deadline)
			if rem <= 0 {
				if started {
					return nil, fmt.Errorf("incomplete SLIP packet after %d bytes", len(packet))
				}
				return nil, fmt.Errorf("timed out waiting for bootloader packet")
			}
			_ = s.port.SetReadTimeout(minDuration(rem, 50*time.Millisecond))
			n, err := s.port.Read(s.scratch[:])
			if err != nil {
				return nil, err
			}
			if n == 0 {
				continue
			}
			s.buf = append(s.buf[:0], s.scratch[:n]...)
		}
		for len(s.buf) > 0 {
			b := s.buf[0]
			s.buf = s.buf[1:]
			if !started {
				if b == slipFrameEnd {
					started = true
					packet = packet[:0]
				} else if looksLikePanic(b, s.buf) {
					return nil, fmt.Errorf("target reported panic/boot error: %s", sanitizeASCII(append([]byte{b}, s.buf...)))
				}
				continue
			}
			if inEscape {
				inEscape = false
				switch b {
				case slipEscEnd:
					packet = append(packet, slipFrameEnd)
				case slipEscEsc:
					packet = append(packet, slipEscape)
				default:
					return nil, fmt.Errorf("invalid SLIP escape sequence 0xdb 0x%02x", b)
				}
				continue
			}
			switch b {
			case slipEscape:
				inEscape = true
			case slipFrameEnd:
				if len(packet) == 0 {
					// Back-to-back 0xC0 delimiter before the actual frame.
					continue
				}
				return packet, nil
			default:
				packet = append(packet, b)
			}
		}
	}
}

func looksLikePanic(first byte, rest []byte) bool {
	if first != 'G' && first != 'F' {
		return false
	}
	sample := string(append([]byte{first}, rest[:min(len(rest), 64)]...))
	return strings.Contains(sample, "Guru Meditation") || strings.Contains(sample, "Fatal exception")
}

func sanitizeASCII(data []byte) string {
	data = bytes.TrimSpace(data)
	if len(data) > 96 {
		data = data[:96]
	}
	return string(bytes.Map(func(r rune) rune {
		if r < 32 || r > 126 {
			return ' '
		}
		return r
	}, data))
}

func (s *slipStream) command(op byte, data []byte, chk uint32, waitResponse bool, timeout time.Duration) (uint32, []byte, error) {
	if op != 0 {
		pkt := make([]byte, 8+len(data))
		pkt[0] = romDirectionRequest
		pkt[1] = op
		binary.LittleEndian.PutUint16(pkt[2:4], uint16(len(data)))
		binary.LittleEndian.PutUint32(pkt[4:8], chk)
		copy(pkt[8:], data)
		if _, err := s.port.Write(encodeSLIP(pkt)); err != nil {
			return 0, nil, err
		}
	}
	if !waitResponse {
		return 0, nil, nil
	}
	for retry := 0; retry < 100; retry++ {
		pkt, err := s.readPacket(timeout)
		if err != nil {
			return 0, nil, err
		}
		if len(pkt) < 8 || pkt[0] != romDirectionResponse {
			continue
		}
		opRet := pkt[1]
		payload := pkt[8:]
		val := binary.LittleEndian.Uint32(pkt[4:8])
		if op == 0 || opRet == op {
			return val, payload, nil
		}
		if len(payload) >= 2 && payload[0] != 0 && payload[1] == romInvalidRecvMsg {
			s.drainBrief()
			return 0, nil, errUnsupportedCommand
		}
	}
	return 0, nil, fmt.Errorf("bootloader response did not match command 0x%02x", op)
}

func (s *slipStream) checkCommand(desc string, op byte, data []byte, chk uint32, respDataLen int, timeout time.Duration) (uint32, []byte, error) {
	val, payload, err := s.command(op, data, chk, true, timeout)
	if err != nil {
		return 0, nil, err
	}
	const statusBytesLen = 2
	if len(payload) < respDataLen+statusBytesLen {
		if len(payload) >= 2 && payload[0] != 0 {
			return 0, nil, &slipError{op: op, status: payload[0], code: payload[1], desc: desc}
		}
		return 0, nil, fmt.Errorf("failed to %s: short response (%d bytes)", desc, len(payload))
	}
	status := payload[respDataLen : respDataLen+statusBytesLen]
	if status[0] != 0 {
		return 0, nil, &slipError{op: op, status: status[0], code: status[1], desc: desc}
	}
	if respDataLen > 0 {
		return val, payload[:respDataLen], nil
	}
	return val, nil, nil
}

func (s *slipStream) drainBrief() {
	time.Sleep(120 * time.Millisecond)
	_ = s.port.Flush()
	s.reset()
	_ = s.port.SetReadTimeout(5 * time.Millisecond)
	buf := make([]byte, 256)
	for i := 0; i < 4; i++ {
		n, err := s.port.Read(buf)
		if n == 0 || err != nil {
			break
		}
	}
}

func minDuration(a, b time.Duration) time.Duration {
	if a < b {
		return a
	}
	return b
}

// drainAvailable discards any pending bytes from the serial port without failing on EOF/empty.
func drainAvailable(port SerialPort) []byte {
	_ = port.SetReadTimeout(10 * time.Millisecond)
	var out []byte
	buf := make([]byte, 1024)
	for i := 0; i < 4; i++ {
		n, err := port.Read(buf)
		if n > 0 {
			out = append(out, buf[:n]...)
		}
		if n == 0 || err != nil && !errors.Is(err, io.EOF) {
			break
		}
	}
	return out
}
