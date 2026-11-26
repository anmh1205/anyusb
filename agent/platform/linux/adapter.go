// Package linux uses sysfs and the distro's USB/IP tools.
package linux

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/anmh1205/anyusb/agent/platform"
)

const commandTimeout = 10 * time.Second

type Adapter struct {
	usbip   string
	sysRoot string
	devRoot string
	hostID  string
	runner  platform.NativeRunner
}

var _ platform.Adapter = (*Adapter)(nil)

func New(usbipPath string) (*Adapter, error) {
	if usbipPath != "" && !filepath.IsAbs(usbipPath) {
		return nil, fmt.Errorf("-usbip requires an absolute path")
	}
	hostID, err := os.ReadFile("/etc/machine-id")
	if err != nil {
		return nil, fmt.Errorf("read Linux machine identity: %w", err)
	}
	identity := strings.TrimSpace(string(hostID))
	if identity == "" || identity == "uninitialized" {
		return nil, fmt.Errorf("/etc/machine-id is not initialized")
	}
	return &Adapter{usbip: usbipPath, sysRoot: "/sys", devRoot: "/dev", hostID: identity, runner: platform.OSNativeRunner{}}, nil
}

func (a *Adapter) binary(name string) (string, error) {
	if name == "usbip" && a.usbip != "" {
		return a.usbip, nil
	}
	path, err := exec.LookPath(name)
	if err != nil {
		return "", fmt.Errorf("%w: %s is missing from PATH; install the distro USB/IP tools", platform.ErrUnavailable, name)
	}
	return path, nil
}

func (a *Adapter) run(ctx context.Context, tool string, args ...string) (string, error) {
	binary, err := a.binary(tool)
	if err != nil {
		return "", err
	}
	runner := a.runner
	if runner == nil {
		runner = platform.OSNativeRunner{}
	}
	result, err := runner.Run(ctx, platform.CommandRequest{
		Tool: tool, Path: binary, Args: args, Environment: []string{"LC_ALL=C"}, Timeout: commandTimeout,
		SensitiveOutput: len(args) != 1 || (args[0] != "version" && args[0] != "--version"),
	})
	if err != nil {
		return "", err
	}
	return result.Stdout, nil
}
