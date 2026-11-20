package main

import (
	"context"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"time"

	"github.com/Microsoft/go-winio"
	"github.com/anmh1205/anyusb/agent/core"
	"github.com/anmh1205/anyusb/agent/platform"
	backend "github.com/anmh1205/anyusb/agent/platform/windows"
	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/svc"
)

func defaultSocket() string                               { return core.DefaultPipe }
func defaultData() string                                 { return filepath.Join(os.Getenv("ProgramData"), "AnyUSB", "state") }
func nativeAdapter(opt options) (platform.Adapter, error) { return backend.New(opt.usbipd, opt.usbip) }

func listenLocal(path string) (net.Listener, io.Closer, error) {
	listener, err := winio.ListenPipe(path, &winio.PipeConfig{
		SecurityDescriptor: "D:P(A;;GA;;;SY)(A;;GA;;;BA)", InputBufferSize: 65536, OutputBufferSize: 65536,
	})
	return listener, nil, err
}

func nativeRun(run func(context.Context) error) error {
	service, err := svc.IsWindowsService()
	if err != nil {
		return err
	}
	if service {
		return svc.Run("anyusbd", serviceHandler{run: run})
	}
	if !windows.GetCurrentProcessToken().IsElevated() {
		return fmt.Errorf("run anyusbd from an elevated PowerShell or install the Windows service")
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	return run(ctx)
}

type serviceHandler struct{ run func(context.Context) error }

func (s serviceHandler) Execute(_ []string, requests <-chan svc.ChangeRequest, status chan<- svc.Status) (bool, uint32) {
	status <- svc.Status{State: svc.StartPending}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- s.run(ctx) }()
	current := svc.Status{State: svc.Running, Accepts: svc.AcceptStop | svc.AcceptShutdown}
	status <- current
	for {
		select {
		case err := <-done:
			if err != nil {
				log.Print(err)
				return false, 1
			}
			return false, 0
		case req, open := <-requests:
			if !open {
				return false, 0
			}
			switch req.Cmd {
			case svc.Interrogate:
				status <- current
			case svc.Stop, svc.Shutdown:
				status <- svc.Status{State: svc.StopPending, WaitHint: 15000}
				cancel()
				select {
				case <-done:
				case <-time.After(15 * time.Second):
				}
				return false, 0
			}
		}
	}
}
