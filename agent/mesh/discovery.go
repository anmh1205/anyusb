package mesh

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/netip"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/anmh1205/anyusb/agent/platform"
	"github.com/hashicorp/mdns"
)

const mdnsService = "_anyusb._tcp"

type Discovered struct {
	Name     string `json:"name"`
	ID       string `json:"id"`
	Endpoint string `json:"endpoint"`
	Network  string `json:"network"`
}

type lanInterface struct {
	device    net.Interface
	addresses []net.IP
}

func lanInterfaces() ([]lanInterface, error) {
	interfaces, err := net.Interfaces()
	if err != nil {
		return nil, err
	}
	var result []lanInterface
	for _, iface := range interfaces {
		const required = net.FlagUp | net.FlagMulticast
		if iface.Flags&required != required || iface.Flags&net.FlagLoopback != 0 {
			continue
		}
		addresses, err := iface.Addrs()
		if err != nil {
			continue
		}
		var ips []net.IP
		for _, addr := range addresses {
			ip, _, err := net.ParseCIDR(addr.String())
			native, ok := netip.AddrFromSlice(ip)
			if err == nil && ok && ip.IsGlobalUnicast() && !tailnetV4.Contains(native.Unmap()) && !tailnetV6.Contains(native) {
				ips = append(ips, ip)
			}
		}
		if len(ips) != 0 {
			result = append(result, lanInterface{device: iface, addresses: ips})
		}
	}
	return result, nil
}

func lanInterfaceSignature(interfaces []lanInterface) string {
	parts := make([]string, 0, len(interfaces))
	for _, iface := range interfaces {
		ips := make([]string, 0, len(iface.addresses))
		for _, ip := range iface.addresses {
			ips = append(ips, ip.String())
		}
		sort.Strings(ips)
		parts = append(parts, fmt.Sprintf("%d:%s=%s", iface.device.Index, iface.device.Name, strings.Join(ips, ",")))
	}
	sort.Strings(parts)
	return strings.Join(parts, ";")
}

// MaintainAdvertise keeps mDNS advertisements synced with active LAN interfaces
// across boot races, Wi-Fi changes, and sleep/resume cycles.
func (n *Node) MaintainAdvertise(ctx context.Context, port int) {
	var stop func()
	lastSig := ""
	announcedUnavailable := false
	refresh := func() {
		interfaces, err := lanInterfaces()
		sig := ""
		if err == nil && len(interfaces) > 0 {
			sig = lanInterfaceSignature(interfaces)
		}
		if sig == lastSig && (sig == "" || stop != nil) {
			return
		}
		if stop != nil {
			stop()
			stop = nil
		}
		lastSig = sig
		if sig == "" {
			if !announcedUnavailable {
				log.Printf("mDNS unavailable: waiting for a multicast LAN interface")
				announcedUnavailable = true
			}
			return
		}
		nextStop, err := n.Advertise(port)
		if err != nil {
			if !announcedUnavailable {
				log.Printf("mDNS unavailable: %v", err)
				announcedUnavailable = true
			}
			lastSig = ""
			return
		}
		stop = nextStop
		if announcedUnavailable {
			log.Printf("mDNS advertising restored")
			announcedUnavailable = false
		}
	}
	refresh()
	ticker := time.NewTicker(15 * time.Second)
	defer ticker.Stop()
	defer func() {
		if stop != nil {
			stop()
		}
	}()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			refresh()
		}
	}
}

func (n *Node) Advertise(port int) (func(), error) {
	if port < 1 || port > 65535 {
		return nil, fmt.Errorf("invalid mDNS port")
	}
	interfaces, err := lanInterfaces()
	if err != nil {
		return nil, err
	}
	var servers []*mdns.Server
	for _, iface := range interfaces {
		service, err := mdns.NewMDNSService(n.id[:12], mdnsService, "local.", n.id[:12]+".local.", port, iface.addresses, []string{"id=" + n.id, "name=" + n.name})
		if err != nil {
			continue
		}
		if server, err := mdns.NewServer(&mdns.Config{Zone: service, Iface: &iface.device}); err == nil {
			servers = append(servers, server)
		}
	}
	if len(servers) == 0 {
		return nil, fmt.Errorf("no multicast LAN interface available for mDNS")
	}
	return func() {
		for _, server := range servers {
			_ = server.Shutdown()
		}
	}, nil
}

func Discover(ctx context.Context) ([]Discovered, error) {
	interfaces, err := lanInterfaces()
	if err != nil {
		return nil, err
	}
	entries := make(chan *mdns.ServiceEntry, 32)
	logger := log.New(io.Discard, "", 0)
	var queries sync.WaitGroup
	for _, iface := range interfaces {
		queries.Add(1)
		go func() {
			defer queries.Done()
			params := mdns.DefaultParams(mdnsService)
			params.Interface = &iface.device
			params.Entries = entries
			params.Logger = logger
			params.Timeout = 1500 * time.Millisecond
			params.WantUnicastResponse = true
			params.DisableIPv4, params.DisableIPv6 = true, true
			for _, ip := range iface.addresses {
				if ip.To4() != nil {
					params.DisableIPv4 = false
				} else {
					params.DisableIPv6 = false
				}
			}
			_ = mdns.QueryContext(ctx, params)
		}()
	}
	go func() { queries.Wait(); close(entries) }()
	result := make([]Discovered, 0)
	seen := make(map[string]bool)
	for {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case item, open := <-entries:
			if !open {
				return result, nil
			}
			if item == nil {
				continue
			}
			addr := item.AddrV4
			if addr == nil {
				addr = item.AddrV6
			}
			if addr == nil || addr.IsLinkLocalUnicast() {
				continue
			}
			entry := Discovered{Endpoint: net.JoinHostPort(addr.String(), strconv.Itoa(item.Port)), Name: item.Name, Network: "LAN"}
			for _, txt := range item.InfoFields {
				key, value, _ := strings.Cut(txt, "=")
				switch key {
				case "id":
					entry.ID = value
				case "name":
					entry.Name = value
				}
			}
			key := entry.ID + "/" + entry.Endpoint
			if !seen[key] {
				seen[key] = true
				result = append(result, entry)
			}
		}
	}
}

var tailnetV4 = netip.MustParsePrefix("100.64.0.0/10")
var tailnetV6 = netip.MustParsePrefix("fd7a:115c:a1e0::/48")

func tailnetCandidates(data []byte) []Discovered {
	var status struct {
		BackendState string
		Peer         map[string]struct {
			Online       bool
			TailscaleIPs []netip.Addr
		}
	}
	if json.Unmarshal(data, &status) != nil || status.BackendState != "Running" {
		return nil
	}
	var endpoints []Discovered
	for _, peer := range status.Peer {
		if !peer.Online {
			continue
		}
		for _, ip := range peer.TailscaleIPs {
			if tailnetV4.Contains(ip) || tailnetV6.Contains(ip) {
				endpoints = append(endpoints, Discovered{Endpoint: net.JoinHostPort(ip.String(), DefaultPeerPort), Network: "Tailscale"})
			}
		}
	}
	sort.Slice(endpoints, func(i, j int) bool { return endpoints[i].Endpoint < endpoints[j].Endpoint })
	return endpoints[:min(len(endpoints), 128)]
}

func tailscaleBinary() string {
	path, _ := exec.LookPath("tailscale")
	if path == "" && runtime.GOOS == "windows" {
		for _, root := range []string{os.Getenv("ProgramW6432"), os.Getenv("ProgramFiles")} {
			candidate := filepath.Join(root, "Tailscale", "tailscale.exe")
			if filepath.IsAbs(candidate) {
				if info, err := os.Stat(candidate); err == nil && !info.IsDir() {
					return candidate
				}
			}
		}
	}
	return path
}

func discoverTailnet(ctx context.Context) []Discovered {
	path := tailscaleBinary()
	if !filepath.IsAbs(path) {
		return nil
	}
	result, err := (platform.OSNativeRunner{}).Run(ctx, platform.CommandRequest{
		Tool: "tailscale status", Path: path, Args: []string{"status", "--json"}, Timeout: 1700 * time.Millisecond,
		SensitiveOutput: true, SkipDiagnosticOnOK: true,
	})
	if err != nil {
		return nil
	}
	return tailnetCandidates([]byte(result.Stdout))
}

func (n *Node) LocalNetworkInfo(ctx context.Context) (addresses []string, tailscaleState string) {
	seen := make(map[string]bool)
	add := func(addr string) {
		addr = strings.TrimSpace(addr)
		if addr == "" || seen[addr] {
			return
		}
		seen[addr] = true
		addresses = append(addresses, addr)
	}
	if n.address != "" {
		host, _, err := net.SplitHostPort(n.address)
		if err == nil && host != "" {
			add(host)
		} else {
			add(n.address)
		}
	}
	tailscaleState = "missing"
	if path := tailscaleBinary(); filepath.IsAbs(path) {
		tailscaleState = "unavailable"
		tctx, cancel := context.WithTimeout(ctx, 1200*time.Millisecond)
		res, err := (platform.OSNativeRunner{}).Run(tctx, platform.CommandRequest{
			Tool: "tailscale status", Path: path, Args: []string{"status", "--json"}, Timeout: 1200 * time.Millisecond,
			SensitiveOutput: true, SkipDiagnosticOnOK: true,
		})
		cancel()
		if err == nil {
			var status struct {
				BackendState string
				Self         struct {
					DNSName      string
					TailscaleIPs []netip.Addr
				}
			}
			if json.Unmarshal([]byte(res.Stdout), &status) == nil {
				if status.BackendState != "" {
					tailscaleState = strings.ToLower(status.BackendState)
				}
				if status.BackendState == "Running" {
					for _, ip := range status.Self.TailscaleIPs {
						if tailnetV4.Contains(ip) {
							add(ip.String())
						}
					}
					if dns := strings.TrimSuffix(status.Self.DNSName, "."); dns != "" {
						add(dns)
					}
					for _, ip := range status.Self.TailscaleIPs {
						if tailnetV6.Contains(ip) {
							add(ip.String())
						}
					}
				}
			}
		}
	}
	if ifaces, err := lanInterfaces(); err == nil {
		for _, iface := range ifaces {
			for _, ip := range iface.addresses {
				if ip.To4() != nil {
					add(ip.String())
				}
			}
		}
	}
	return addresses, tailscaleState
}

func onLocalSubnet(endpoint string, addresses []net.Addr) bool {
	host, _, _ := net.SplitHostPort(endpoint)
	ip := net.ParseIP(host)
	if ip == nil || !ip.IsGlobalUnicast() {
		return false
	}
	for _, addr := range addresses {
		if subnet, ok := addr.(*net.IPNet); ok && subnet.Contains(ip) {
			return true
		}
	}
	return false
}

func (n *Node) probePeer(ctx context.Context, hint Discovered) (Discovered, bool) {
	ctx, cancel := context.WithTimeout(ctx, 1200*time.Millisecond)
	defer cancel()
	client := n.peerClient(hint.ID)
	defer client.CloseIdleConnections()
	res, err := send(ctx, client, http.MethodGet, peerURL(hint.Endpoint, "/v1/pair/identity"), nil, "")
	if err != nil {
		return Discovered{}, false
	}
	defer res.Body.Close()
	var peer Peer
	id := fingerprint(res.TLS.PeerCertificates[0].Raw)
	if json.NewDecoder(io.LimitReader(res.Body, 4096)).Decode(&peer) != nil || peer.ID != id || id == n.id || len(peer.Name) > 128 {
		return Discovered{}, false
	}
	hint.ID, hint.Name = peer.ID, peer.Name
	return hint, true
}

func (n *Node) discover(ctx context.Context) []Discovered {
	ctx, cancel := context.WithTimeout(ctx, 6*time.Second)
	defer cancel()
	lan := make(chan []Discovered, 1)
	go func() {
		hints, _ := Discover(ctx)
		lan <- hints
	}()
	hints := discoverTailnet(ctx)
	addresses, _ := net.InterfaceAddrs()
	for _, hint := range <-lan {
		if hint.ID != n.id && onLocalSubnet(hint.Endpoint, addresses) {
			hints = append(hints, hint)
		}
	}
	for _, peer := range n.peers() {
		if peer.Endpoint != "" {
			hints = append(hints, Discovered{ID: peer.ID, Endpoint: peer.Endpoint, Network: "Saved"})
		}
	}

	result := make([]Discovered, len(hints))
	var workers sync.WaitGroup
	for group := 0; group < min(16, len(hints)); group++ {
		workers.Add(1)
		go func(first int) {
			defer workers.Done()
			for i := first; i < len(hints) && ctx.Err() == nil; i += min(16, len(hints)) {
				result[i], _ = n.probePeer(ctx, hints[i])
			}
		}(group)
	}
	workers.Wait()
	return n.devicesFromDiscovery(result)
}

func discoveryPriority(item Discovered) int {
	host, _, err := net.SplitHostPort(item.Endpoint)
	if err != nil {
		host = item.Endpoint
	}
	ip, parseErr := netip.ParseAddr(strings.Trim(host, "[]"))
	switch item.Network {
	case "Tailscale":
		if parseErr == nil && ip.Is4() {
			return 4
		}
		return 3
	case "LAN":
		return 2
	default:
		return 1
	}
}

func classifyEndpointNetwork(endpoint string, localAddresses []net.Addr) string {
	host, _, err := net.SplitHostPort(endpoint)
	if err != nil {
		host = endpoint
	}
	if ip, parseErr := netip.ParseAddr(strings.Trim(host, "[]")); parseErr == nil {
		unmapped := ip.Unmap()
		if tailnetV4.Contains(unmapped) || tailnetV6.Contains(unmapped) {
			return "Tailscale"
		}
	}
	if onLocalSubnet(endpoint, localAddresses) {
		return "LAN"
	}
	return "Saved"
}

func (n *Node) devicesFromDiscovery(results []Discovered) []Discovered {
	addresses, _ := net.InterfaceAddrs()
	unique := make(map[string]Discovered)
	reachable := make(map[string]bool)
	for _, item := range results {
		if item.ID != "" {
			reachable[item.ID+"/"+item.Endpoint] = true
			if item.Network == "Saved" {
				item.Network = classifyEndpointNetwork(item.Endpoint, addresses)
			}
			current, exists := unique[item.ID]
			if !exists || discoveryPriority(item) > discoveryPriority(current) {
				unique[item.ID] = item
			}
		}
	}
	devices := make([]Discovered, 0, len(unique))
	n.mu.Lock()
	defer n.mu.Unlock()
	for _, item := range unique {
		devices = append(devices, item)
		if peer, ok := n.config.Peers[item.ID]; ok {
			changed := false
			if !reachable[peer.ID+"/"+peer.Endpoint] && item.Endpoint != "" {
				peer.Endpoint = item.Endpoint
				changed = true
			}
			if item.Name != "" && peer.Name != item.Name {
				peer.Name = item.Name
				changed = true
			}
			if changed {
				_ = n.addPeerLocked(peer)
			}
		}
	}
	sort.Slice(devices, func(i, j int) bool { return devices[i].Name < devices[j].Name })
	return devices
}
