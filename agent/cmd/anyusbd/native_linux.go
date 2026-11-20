package main

import (
	"context"
	"fmt"
	"github.com/anmh1205/anyusb/agent/core"
	"github.com/anmh1205/anyusb/agent/platform"
	"github.com/anmh1205/anyusb/agent/platform/linux"
	"os"
	"os/signal"
	"syscall"
)

func defaultSocket() string                               { return core.DefaultSocket }
func defaultData() string                                 { return "/var/lib/anyusb" }
func nativeAdapter(opt options) (platform.Adapter, error) { return linux.New(opt.usbip) }

func nativeRun(run func(context.Context) error) error {
	if os.Geteuid() != 0 {
		return fmt.Errorf("anyusbd must run as root to manage USB/IP")
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	return run(ctx)
}
