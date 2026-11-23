package main

import (
	"context"
	"fmt"
	"io"
	"net"
	"strings"
	"time"

	"github.com/Microsoft/go-winio"
	"github.com/anmh1205/anyusb/agent/core"
	"golang.org/x/sys/windows"
)

const defaultSocket = core.DefaultPipe

func dialLocal(ctx context.Context, socket string) (net.Conn, error) {
	return winio.DialPipeContext(ctx, socket)
}

func desktopURL(ctx context.Context) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	conn, err := winio.DialPipeAccess(ctx, core.DesktopPipe, windows.GENERIC_READ)
	if err != nil {
		return "", err
	}
	defer conn.Close()
	pipe, ok := conn.(interface{ Fd() uintptr })
	if !ok {
		return "", fmt.Errorf("could not verify the AnyUSB desktop pipe")
	}
	security, err := windows.GetSecurityInfo(windows.Handle(pipe.Fd()), windows.SE_KERNEL_OBJECT, windows.OWNER_SECURITY_INFORMATION)
	if err != nil {
		return "", fmt.Errorf("verify AnyUSB desktop pipe owner: %w", err)
	}
	owner, _, err := security.Owner()
	if err != nil || owner == nil || (!owner.IsWellKnown(windows.WinLocalSystemSid) && !owner.IsWellKnown(windows.WinBuiltinAdministratorsSid)) {
		return "", fmt.Errorf("unexpected owner of the AnyUSB desktop pipe")
	}
	_ = conn.SetReadDeadline(time.Now().Add(3 * time.Second))
	url, err := io.ReadAll(io.LimitReader(conn, 1024))
	return strings.TrimSpace(string(url)), err
}
