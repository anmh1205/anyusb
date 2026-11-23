package main

import (
	"context"
	"fmt"
	"github.com/anmh1205/anyusb/agent/core"
	"net"
)

const defaultSocket = core.DefaultSocket

func dialLocal(ctx context.Context, socket string) (net.Conn, error) {
	return (&net.Dialer{}).DialContext(ctx, "unix", socket)
}

func desktopURL(context.Context) (string, error) {
	return "", fmt.Errorf("open AnyUSB from the application menu")
}
