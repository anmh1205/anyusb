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
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/anmh1205/anyusb/agent/core"
	"github.com/anmh1205/anyusb/agent/mesh"
	"github.com/anmh1205/anyusb/agent/platform"
	platformmock "github.com/anmh1205/anyusb/agent/platform/mock"
	"github.com/anmh1205/anyusb/web"
)

type options struct {
	socket, data, usbip, usbipd, python string
	peer, ui, advertise, backend, name  string
	devScenario                         string
	mdns, desktop, usbOnly              bool
}

func main() {
	host, _ := os.Hostname()
	var opt options
	flag.StringVar(&opt.socket, "socket", defaultSocket(), "local control socket or named pipe")
	flag.StringVar(&opt.data, "data", defaultData(), "private state directory")
	flag.StringVar(&opt.usbip, "usbip", "", "absolute path to the USB/IP client CLI")
	flag.StringVar(&opt.usbipd, "usbipd", "", "absolute path to usbipd-win (Windows)")
	flag.StringVar(&opt.python, "python", "", "deprecated; built-in ESP engine requires no Python runtime")
	flag.StringVar(&opt.peer, "peer-listen", ":"+mesh.DefaultPeerPort, "HTTPS peer address; empty disables it")
	flag.StringVar(&opt.ui, "ui-listen", "127.0.0.1:47440", "local web address; empty disables it")
	flag.StringVar(&opt.advertise, "advertise", "", "peer address shared during pairing, such as a MagicDNS name")
	flag.StringVar(&opt.backend, "backend", "127.0.0.1:3240", "loopback address of the stock USB/IP host backend")
	flag.StringVar(&opt.name, "name", host, "name shown to other AnyUSB operators")
	flag.StringVar(&opt.devScenario, "dev-scenario", "", "development-only mock USB scenario; never compatibility evidence")
	flag.BoolVar(&opt.mdns, "mdns", true, "advertise on the local network")
	flag.BoolVar(&opt.desktop, "desktop", false, "allow local interactive Windows users to open the desktop app")
	flag.BoolVar(&opt.usbOnly, "usb-only", false, "serve USB forwarding without host-side ESP operations")
	flag.Parse()
	if flag.NArg() != 0 || len(opt.name) > 128 {
		flag.Usage()
		os.Exit(2)
	}
	if opt.devScenario != "" {
		configured := make(map[string]bool)
		flag.Visit(func(value *flag.Flag) { configured[value.Name] = true })
		if err := opt.isolateDevelopment(configured); err != nil {
			log.Fatal(err)
		}
	}
	if err := nativeRun(opt.devScenario != "", func(ctx context.Context) error { return run(ctx, opt) }); err != nil {
		log.Fatal(err)
	}
}

func (opt *options) isolateDevelopment(configured map[string]bool) error {
	data, err := filepath.Abs(opt.data)
	if err != nil {
		return err
	}
	if !configured["data"] || !configured["socket"] || opt.socket == "" ||
		strings.EqualFold(filepath.Clean(opt.socket), filepath.Clean(defaultSocket())) ||
		strings.EqualFold(data, filepath.Clean(defaultData())) || opt.desktop {
		return fmt.Errorf("a development scenario requires a separate -data directory and -socket, without -desktop")
	}
	opt.data = data
	if !configured["peer-listen"] {
		opt.peer = "127.0.0.1:0"
	}
	if opt.peer != "" {
		host, _, err := net.SplitHostPort(opt.peer)
		if err != nil || !net.ParseIP(host).IsLoopback() {
			return fmt.Errorf("a development scenario requires a loopback -peer-listen address")
		}
	}
	if !configured["ui-listen"] {
		opt.ui = "127.0.0.1:0"
	}
	opt.mdns, opt.usbOnly = false, true
	return nil
}

type listenerService struct {
	listener net.Listener
	server   *http.Server
}

func run(parent context.Context, opt options) error {
	ctx, cancel := context.WithCancel(parent)
	defer cancel()
	var adapter platform.Adapter
	if opt.devScenario != "" {
		mockAdapter, err := platformmock.New(ctx, opt.devScenario)
		if err != nil {
			return err
		}
		adapter, opt.backend = mockAdapter, mockAdapter.BackendAddress()
		log.Printf("WARNING: using development mock USB scenario %q; results are not hardware compatibility evidence", opt.devScenario)
	} else {
		var err error
		adapter, err = nativeAdapter(opt)
		if err != nil {
			return err
		}
	}
	local, lock, err := listenLocal(opt.socket, opt.devScenario != "")
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
	control := core.Options{Mesh: node.LocalHandler(), RawShare: node.RawShare, Diagnostics: func() any { return node.Diagnostics() }}
	if !opt.usbOnly {
		if opt.python != "" {
			log.Print("note: -python is deprecated; AnyUSB uses its built-in ESP bootloader and serial engine")
		}
		node.ConfigureESP()
		control.ESPCheck = node.ESPCheck
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
	if ui != nil {
		control.UIURL = ui.URL()
		if opt.desktop {
			bootstrap, err := listenDesktop(control.UIURL)
			if err != nil {
				return err
			}
			defer bootstrap.Close()
		}
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
			go node.MaintainAdvertise(ctx, port)
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
