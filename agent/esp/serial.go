package esp

import (
	"io"
	"time"
)

// SerialPort abstracts an OS serial device for host-local flashing and monitoring.
type SerialPort interface {
	io.ReadWriteCloser
	SetBaud(baud int) error
	SetDTR(state bool) error
	SetRTS(state bool) error
	SetDTRAndRTS(dtr, rts bool) error
	SetReadTimeout(timeout time.Duration) error
	Flush() error
}

// PortOpener opens a host serial port at the requested baud rate with DTR and RTS deasserted.
type PortOpener func(port string, baud int) (SerialPort, error)
