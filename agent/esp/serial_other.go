//go:build !linux && !windows

package esp

import (
	"fmt"
	"runtime"
)

func openSerialPort(string, int) (SerialPort, error) {
	return nil, fmt.Errorf("serial port access is unsupported on %s", runtime.GOOS)
}
