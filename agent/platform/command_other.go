//go:build !windows

package platform

import "os/exec"

func configureNativeCommand(*exec.Cmd) {}
