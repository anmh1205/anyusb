//go:build windows

package esp

import (
	"fmt"
	"math"
	"strings"
	"sync"
	"time"

	"golang.org/x/sys/windows"
)

const (
	dcbFlagBinary = 1

	winSetRTS = 3
	winClrRTS = 4
	winSetDTR = 5
	winClrDTR = 6

	purgeFlags = 0x000F // PURGE_TXABORT | PURGE_RXABORT | PURGE_TXCLEAR | PURGE_RXCLEAR
)

type windowsSerial struct {
	mu     sync.Mutex
	handle windows.Handle
	closed bool
	dtr    bool
	rts    bool
}

func openSerialPort(port string, baud int) (SerialPort, error) {
	path := port
	if !strings.HasPrefix(path, `\\.\`) {
		path = `\\.\` + path
	}
	wide, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return nil, fmt.Errorf("invalid serial port %q: %w", port, err)
	}
	handle, err := windows.CreateFile(
		wide,
		windows.GENERIC_READ|windows.GENERIC_WRITE,
		0,
		nil,
		windows.OPEN_EXISTING,
		windows.FILE_ATTRIBUTE_NORMAL,
		0,
	)
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", port, err)
	}
	sp := &windowsSerial{handle: handle}
	if err := sp.configure(baud); err != nil {
		_ = sp.Close()
		return nil, fmt.Errorf("configure %s: %w", port, err)
	}
	if err := sp.SetReadTimeout(100 * time.Millisecond); err != nil {
		_ = sp.Close()
		return nil, fmt.Errorf("set timeouts on %s: %w", port, err)
	}
	_ = sp.SetDTRAndRTS(false, false)
	return sp, nil
}

func (s *windowsSerial) configure(baud int) error {
	var dcb windows.DCB
	dcb.DCBlength = uint32(unsafeSizeofDCB)
	if err := windows.GetCommState(s.handle, &dcb); err != nil {
		return err
	}
	dcb.BaudRate = uint32(baud)
	dcb.Flags = dcbFlagBinary
	dcb.ByteSize = 8
	dcb.Parity = 0
	dcb.StopBits = 0
	return windows.SetCommState(s.handle, &dcb)
}

func (s *windowsSerial) Read(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	var count uint32
	if err := windows.ReadFile(s.handle, p, &count, nil); err != nil {
		return 0, err
	}
	return int(count), nil
}

func (s *windowsSerial) Write(p []byte) (int, error) {
	written := 0
	for written < len(p) {
		var count uint32
		if err := windows.WriteFile(s.handle, p[written:], &count, nil); err != nil {
			return written, err
		}
		if count == 0 {
			return written, fmt.Errorf("serial write made no progress")
		}
		written += int(count)
	}
	return written, nil
}

func (s *windowsSerial) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil
	}
	s.closed = true
	return windows.CloseHandle(s.handle)
}

func (s *windowsSerial) SetBaud(baud int) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return fmt.Errorf("serial port is closed")
	}
	return s.configure(baud)
}

func (s *windowsSerial) SetDTR(state bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return fmt.Errorf("serial port is closed")
	}
	return s.setDTRLocked(state)
}

func (s *windowsSerial) setDTRLocked(state bool) error {
	fn := uint32(winClrDTR)
	if state {
		fn = winSetDTR
	}
	if err := windows.EscapeCommFunction(s.handle, fn); err != nil {
		return err
	}
	s.dtr = state
	return nil
}

func (s *windowsSerial) SetRTS(state bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return fmt.Errorf("serial port is closed")
	}
	return s.setRTSLocked(state)
}

func (s *windowsSerial) setRTSLocked(state bool) error {
	fn := uint32(winClrRTS)
	if state {
		fn = winSetRTS
	}
	if err := windows.EscapeCommFunction(s.handle, fn); err != nil {
		return err
	}
	s.rts = state
	// Windows usbser.sys propagates RTS/DTR together on a follow-up DTR command.
	return s.setDTRLocked(s.dtr)
}

func (s *windowsSerial) SetDTRAndRTS(dtr, rts bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return fmt.Errorf("serial port is closed")
	}
	if err := s.setDTRLocked(dtr); err != nil {
		return err
	}
	return s.setRTSLocked(rts)
}

func (s *windowsSerial) SetReadTimeout(timeout time.Duration) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return fmt.Errorf("serial port is closed")
	}
	timeouts := windows.CommTimeouts{
		ReadIntervalTimeout:       math.MaxUint32,
		WriteTotalTimeoutConstant: 10000,
	}
	if timeout > 0 {
		ms := uint32(timeout.Milliseconds())
		if ms == 0 {
			ms = 1
		}
		if ms == math.MaxUint32 {
			ms = math.MaxUint32 - 1
		}
		timeouts.ReadTotalTimeoutMultiplier = math.MaxUint32
		timeouts.ReadTotalTimeoutConstant = ms
	}
	return windows.SetCommTimeouts(s.handle, &timeouts)
}

func (s *windowsSerial) Flush() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return fmt.Errorf("serial port is closed")
	}
	return windows.PurgeComm(s.handle, purgeFlags)
}

// unsafeSizeofDCB matches Win32 sizeof(DCB) without importing unsafe in hot paths.
const unsafeSizeofDCB = 28
