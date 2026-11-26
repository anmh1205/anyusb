//go:build windows

package platform

import (
	"os/exec"
	"syscall"
)

func configureNativeCommand(command *exec.Cmd) {
	command.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
}
