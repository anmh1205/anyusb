package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/anmh1205/anyusb/agent/core"
	"github.com/anmh1205/anyusb/agent/platform/linux"
)

func main() {
	socket := flag.String("socket", core.DefaultSocket, "private Unix control socket")
	usbip := flag.String("usbip", "", "absolute path to usbip (defaults to PATH)")
	flag.Parse()
	if flag.NArg() != 0 {
		flag.Usage()
		os.Exit(2)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := run(ctx, *socket, *usbip); err != nil {
		log.Fatal(err)
	}
}

func run(ctx context.Context, socket, usbip string) error {
	if os.Geteuid() != 0 {
		return fmt.Errorf("anyusbd must run as root to manage USB/IP")
	}
	adapter, err := linux.New(usbip)
	if err != nil {
		return err
	}
	listener, lock, err := listenLocal(socket)
	if err != nil {
		return err
	}
	defer lock.Close()
	defer listener.Close()
	srv := &http.Server{
		Handler:           core.Handler(adapter),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       10 * time.Second,
		IdleTimeout:       30 * time.Second,
		BaseContext:       func(net.Listener) context.Context { return ctx },
	}
	serve := make(chan error, 1)
	go func() { serve <- srv.Serve(listener) }()
	log.Printf("anyusbd listening on %s", socket)
	select {
	case err := <-serve:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		defer srv.Close()
		return srv.Shutdown(shutdownCtx)
	}
}
