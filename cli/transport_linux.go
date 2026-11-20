package main

import (
	"context"
	"github.com/anmh1205/anyusb/agent/core"
	"net"
)

const defaultSocket = core.DefaultSocket

func dialLocal(ctx context.Context, socket string) (net.Conn, error) {
	return (&net.Dialer{}).DialContext(ctx, "unix", socket)
}
