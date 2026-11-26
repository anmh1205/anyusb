//go:build linux

package esp

import (
	"errors"
	"fmt"
	"io"
	"sync"
	"time"

	"golang.org/x/sys/unix"
)

type linuxSerial struct {
	mu      sync.Mutex
	fd      int
	closed  bool
	timeout time.Duration
}

func openSerialPort(port string, baud int) (SerialPort, error) {
	fd, err := unix.Open(port, unix.O_RDWR|unix.O_NOCTTY|unix.O_NONBLOCK, 0)
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", port, err)
	}
	sp := &linuxSerial{fd: fd, timeout: 100 * time.Millisecond}
	if err := sp.configure(baud); err != nil {
		_ = sp.Close()
		return nil, fmt.Errorf("configure %s: %w", port, err)
	}
	_ = sp.SetDTRAndRTS(false, false)
	return sp, nil
}

func (s *linuxSerial) configure(baud int) error {
	termios, err := unix.IoctlGetTermios(s.fd, unix.TCGETS2)
	if err != nil {
		return err
	}
	termios.Iflag &^= unix.IGNBRK | unix.BRKINT | unix.PARMRK | unix.ISTRIP | unix.INLCR | unix.IGNCR | unix.ICRNL | unix.IXON | unix.IXOFF | unix.IXANY
	termios.Oflag &^= unix.OPOST
	termios.Lflag &^= unix.ECHO | unix.ECHONL | unix.ICANON | unix.ISIG | unix.IEXTEN
	termios.Cflag &^= unix.CSIZE | unix.PARENB | unix.PARODD | unix.CSTOPB | unix.CRTSCTS | unix.CBAUD | unix.HUPCL
	termios.Cflag |= unix.CS8 | unix.CREAD | unix.CLOCAL | unix.BOTHER
	termios.Ispeed = uint32(baud)
	termios.Ospeed = uint32(baud)
	termios.Cc[unix.VMIN] = 0
	termios.Cc[unix.VTIME] = 0
	return unix.IoctlSetTermios(s.fd, unix.TCSETS2, termios)
}

func (s *linuxSerial) Read(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	s.mu.Lock()
	fd := s.fd
	closed := s.closed
	timeout := s.timeout
	s.mu.Unlock()
	if closed {
		return 0, io.EOF
	}
	waitMs := 0
	if timeout > 0 {
		waitMs = int(timeout.Milliseconds())
		if waitMs == 0 {
			waitMs = 1
		}
	}
	fds := []unix.PollFd{{Fd: int32(fd), Events: unix.POLLIN}}
	for {
		n, err := unix.Poll(fds, waitMs)
		if errors.Is(err, unix.EINTR) {
			continue
		}
		if err != nil {
			return 0, err
		}
		if n == 0 {
			return 0, nil
		}
		count, err := unix.Read(fd, p)
		if errors.Is(err, unix.EAGAIN) || errors.Is(err, unix.EINTR) {
			if timeout <= 0 {
				return 0, nil
			}
			continue
		}
		if err != nil {
			return 0, err
		}
		if count == 0 && (fds[0].Revents&(unix.POLLERR|unix.POLLHUP|unix.POLLNVAL)) != 0 {
			return 0, io.EOF
		}
		return count, nil
	}
}

func (s *linuxSerial) Write(p []byte) (int, error) {
	s.mu.Lock()
	fd := s.fd
	closed := s.closed
	s.mu.Unlock()
	if closed {
		return 0, io.ErrClosedPipe
	}
	written := 0
	for written < len(p) {
		count, err := unix.Write(fd, p[written:])
		if errors.Is(err, unix.EINTR) {
			continue
		}
		if errors.Is(err, unix.EAGAIN) {
			fds := []unix.PollFd{{Fd: int32(fd), Events: unix.POLLOUT}}
			if _, pollErr := unix.Poll(fds, 10000); pollErr != nil && !errors.Is(pollErr, unix.EINTR) {
				return written, pollErr
			}
			continue
		}
		if err != nil {
			return written, err
		}
		if count == 0 {
			return written, io.ErrShortWrite
		}
		written += count
	}
	return written, nil
}

func (s *linuxSerial) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil
	}
	s.closed = true
	return unix.Close(s.fd)
}

func (s *linuxSerial) SetBaud(baud int) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return fmt.Errorf("serial port is closed")
	}
	return s.configure(baud)
}

func (s *linuxSerial) SetDTR(state bool) error {
	return s.updateModemBits(unix.TIOCM_DTR, state)
}

func (s *linuxSerial) SetRTS(state bool) error {
	return s.updateModemBits(unix.TIOCM_RTS, state)
}

func (s *linuxSerial) SetDTRAndRTS(dtr, rts bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return fmt.Errorf("serial port is closed")
	}
	status, err := unix.IoctlGetInt(s.fd, unix.TIOCMGET)
	if err != nil {
		return err
	}
	if dtr {
		status |= unix.TIOCM_DTR
	} else {
		status &^= unix.TIOCM_DTR
	}
	if rts {
		status |= unix.TIOCM_RTS
	} else {
		status &^= unix.TIOCM_RTS
	}
	return unix.IoctlSetPointerInt(s.fd, unix.TIOCMSET, status)
}

func (s *linuxSerial) updateModemBits(bit int, enabled bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return fmt.Errorf("serial port is closed")
	}
	req := uint(unix.TIOCMBIC)
	if enabled {
		req = unix.TIOCMBIS
	}
	return unix.IoctlSetPointerInt(s.fd, req, bit)
}

func (s *linuxSerial) SetReadTimeout(timeout time.Duration) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.timeout = timeout
	return nil
}

func (s *linuxSerial) Flush() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return fmt.Errorf("serial port is closed")
	}
	return unix.IoctlSetInt(s.fd, unix.TCFLSH, unix.TCIOFLUSH)
}
