package main

import (
	"context"
	"github.com/Microsoft/go-winio"
	"github.com/anmh1205/anyusb/agent/core"
	"net"
)

const defaultSocket = core.DefaultPipe

func dialLocal(ctx context.Context, socket string) (net.Conn, error) {
	return winio.DialPipeContext(ctx, socket)
}
