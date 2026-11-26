package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"

	"github.com/anmh1205/anyusb/agent/core"
	"github.com/anmh1205/anyusb/agent/platform"
	"github.com/anmh1205/anyusb/agent/platform/linux"
)

func defaultSocket() string                               { return core.DefaultSocket }
func defaultData() string                                 { return "/var/lib/anyusb" }
func nativeAdapter(opt options) (platform.Adapter, error) { return linux.New(opt.usbip) }
func listenDesktop(string) (io.Closer, error) {
	return nil, fmt.Errorf("Linux desktop startup uses polkit")
}

func nativeRun(development bool, run func(context.Context) error) error {
	if !development && os.Geteuid() != 0 {
		return fmt.Errorf("anyusbd must run as root to manage USB/IP")
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	return run(ctx)
}
