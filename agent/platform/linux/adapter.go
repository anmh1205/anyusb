// Package linux uses sysfs and the distro's USB/IP tools.
package linux

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/netip"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/anmh1205/anyusb/agent/platform"
)

const commandTimeout = 10 * time.Second

var (
	busIDPattern = regexp.MustCompile(`^[1-9][0-9]*-[1-9][0-9]*(\.[1-9][0-9]*)*$`)
	labelPattern = regexp.MustCompile(`^[a-zA-Z0-9]([a-zA-Z0-9-]{0,61}[a-zA-Z0-9])?$`)
	zonePattern  = regexp.MustCompile(`^[a-zA-Z0-9_.-]{1,64}$`)
)

type Adapter struct {
	usbip   string
	sysRoot string
	hostID  string
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
	return &Adapter{usbip: usbipPath, sysRoot: "/sys", hostID: identity}, nil
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
	ctx, cancel := context.WithTimeout(ctx, commandTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, binary, args...)
	cmd.Env = append(os.Environ(), "LC_ALL=C")
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	stdout, err := cmd.Output()
	if err == nil {
		return string(stdout), nil
	}
	detail := strings.TrimSpace(stderr.String())
	if detail == "" {
		detail = strings.TrimSpace(string(stdout))
	}
	command := tool + " " + args[0]
	if ctx.Err() != nil {
		return "", fmt.Errorf("%s: %w", command, ctx.Err())
	}
	if errors.Is(err, os.ErrNotExist) || errors.Is(err, os.ErrPermission) {
		return "", fmt.Errorf("%w: %s: %v", platform.ErrUnavailable, command, err)
	}
	if detail == "" {
		return "", fmt.Errorf("%s: %w", command, err)
	}
	return "", fmt.Errorf("%s: %w: %s", command, err, detail)
}

func normalizeHost(host string) (string, error) {
	if addr, err := netip.ParseAddr(host); err == nil {
		if zone := addr.Zone(); zone != "" && !zonePattern.MatchString(zone) {
			return "", fmt.Errorf("%w: invalid IPv6 scope", platform.ErrInvalid)
		}
		return addr.String(), nil
	}
	name := strings.TrimSuffix(host, ".")
	if len(name) > 253 {
		return "", fmt.Errorf("%w: hostname is too long", platform.ErrInvalid)
	}
	for _, label := range strings.Split(name, ".") {
		if !labelPattern.MatchString(label) {
			return "", fmt.Errorf("%w: provide a hostname or IP address without a port", platform.ErrInvalid)
		}
	}
	return strings.ToLower(host), nil
}
