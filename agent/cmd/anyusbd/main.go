package main

import (
	"context"
	"crypto/tls"
	"errors"
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"strconv"
	"time"

	"github.com/anmh1205/anyusb/agent/core"
	"github.com/anmh1205/anyusb/agent/mesh"
	"github.com/anmh1205/anyusb/web"
)

type options struct {
	socket, data, usbip, usbipd, python string
	peer, ui, advertise, backend, name  string
	mdns                                bool
}

func main() {
	host, _ := os.Hostname()
	var opt options
	flag.StringVar(&opt.socket, "socket", defaultSocket(), "local control socket or named pipe")
	flag.StringVar(&opt.data, "data", defaultData(), "private state directory")
	flag.StringVar(&opt.usbip, "usbip", "", "absolute path to the USB/IP client CLI")
	flag.StringVar(&opt.usbipd, "usbipd", "", "absolute path to usbipd-win (Windows)")
	flag.StringVar(&opt.python, "python", "", "absolute path to Python with the pinned esptool")
	flag.StringVar(&opt.peer, "peer-listen", ":"+mesh.DefaultPeerPort, "HTTPS peer address; empty disables it")
	flag.StringVar(&opt.ui, "ui-listen", "127.0.0.1:47440", "local web address; empty disables it")
	flag.StringVar(&opt.advertise, "advertise", "", "peer address shared during pairing, such as a MagicDNS name")
	flag.StringVar(&opt.backend, "backend", "127.0.0.1:3240", "loopback address of the stock USB/IP host backend")
	flag.StringVar(&opt.name, "name", host, "name shown to other AnyUSB operators")
	flag.BoolVar(&opt.mdns, "mdns", true, "advertise on the local network")
	flag.Parse()
	if flag.NArg() != 0 || len(opt.name) > 128 {
		flag.Usage()
		os.Exit(2)
	}
	if err := nativeRun(func(ctx context.Context) error { return run(ctx, opt) }); err != nil {
		log.Fatal(err)
	}
}

type listenerService struct {
	listener net.Listener
	server   *http.Server
}

func run(parent context.Context, opt options) error {
	ctx, cancel := context.WithCancel(parent)
	defer cancel()
	adapter, err := nativeAdapter(opt)
	if err != nil {
		return err
	}
	local, lock, err := listenLocal(opt.socket)
	if err != nil {
		return err
	}
	defer local.Close()
	if lock != nil {
		defer lock.Close()
	}
	node, err := mesh.New(ctx, adapter, opt.data, opt.name, opt.advertise, opt.backend)
	if err != nil {
		return err
	}
	node.ConfigureESP(opt.python)
	if diagnostic := node.ESPCheck(); !diagnostic.OK {
		log.Print(diagnostic.Detail)
	}
	newServer := func(listener net.Listener, handler http.Handler) listenerService {
		return listenerService{listener, &http.Server{
			Handler: handler, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 3 * time.Minute, IdleTimeout: 30 * time.Second,
			MaxHeaderBytes: 16384, BaseContext: func(net.Listener) context.Context { return ctx },
		}}
	}
	var servers []listenerService
	var ui *web.UI
	if opt.ui != "" {
		host, _, err := net.SplitHostPort(opt.ui)
		if err != nil || host != "127.0.0.1" {
			return fmt.Errorf("web UI must listen on 127.0.0.1")
		}
		listener, err := net.Listen("tcp4", opt.ui)
		if err != nil {
			return err
		}
		defer listener.Close()
		ui = web.New(listener.Addr().String())
		log.Printf("AnyUSB web UI at http://%s (run anyusb ui for the authorized URL)", listener.Addr())
		servers = append(servers, newServer(listener, ui))
	}
	control := core.Options{Mesh: node.LocalHandler(), ESPCheck: node.ESPCheck, RawShare: node.RawShare}
	if ui != nil {
		control.UIURL = ui.URL()
	}
	handler := core.Handler(adapter, control)
	if ui != nil {
		ui.API = handler
	}
	servers = append(servers, newServer(local, handler))
	if opt.peer != "" {
		peer, err := net.Listen("tcp", opt.peer)
		if err != nil {
			return err
		}
		defer peer.Close()
		servers = append(servers, newServer(tls.NewListener(peer, node.TLSConfig()), node.PeerHandler()))
		log.Printf("AnyUSB peer HTTPS on %s", peer.Addr())
		if opt.mdns {
			_, rawPort, _ := net.SplitHostPort(peer.Addr().String())
			port, _ := strconv.Atoi(rawPort)
			server, err := node.Advertise(port)
			if err != nil {
				log.Printf("mDNS unavailable: %v", err)
			} else {
				defer server.Shutdown()
			}
		}
	}
	log.Printf("anyusbd listening on %s", opt.socket)
	results := make(chan error, len(servers))
	for _, svc := range servers {
		go func() { results <- svc.server.Serve(svc.listener) }()
	}
	var result error
	select {
	case <-ctx.Done():
	case result = <-results:
	}
	cancel()
	shutdown, stop := context.WithTimeout(context.Background(), 10*time.Second)
	defer stop()
	for _, svc := range servers {
		_ = svc.server.Shutdown(shutdown)
		_ = svc.server.Close()
	}
	node.Drain(shutdown)
	if errors.Is(result, http.ErrServerClosed) {
		return nil
	}
	return result
}
