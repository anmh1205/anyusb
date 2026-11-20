package mesh

import (
	"context"
	"fmt"
	"net"
	"strconv"
	"strings"
	"time"

	"github.com/hashicorp/mdns"
)

const mdnsService = "_anyusb._tcp"

type Discovered struct {
	Name     string `json:"name"`
	ID       string `json:"id"`
	Endpoint string `json:"endpoint"`
}

func (n *Node) Advertise(port int) (*mdns.Server, error) {
	if port < 1 || port > 65535 {
		return nil, fmt.Errorf("invalid mDNS port")
	}
	interfaces, err := net.Interfaces()
	if err != nil {
		return nil, err
	}
	var ips []net.IP
	for _, iface := range interfaces {
		if iface.Flags&net.FlagUp == 0 || iface.Flags&net.FlagLoopback != 0 {
			continue
		}
		addresses, err := iface.Addrs()
		if err != nil {
			continue
		}
		for _, addr := range addresses {
			ip, _, err := net.ParseCIDR(addr.String())
			if err == nil && ip.IsGlobalUnicast() {
				ips = append(ips, ip)
			}
		}
	}
	if len(ips) == 0 {
		return nil, fmt.Errorf("no LAN addresses available for mDNS")
	}
	service, err := mdns.NewMDNSService(n.id[:12], mdnsService, "local.", n.id[:12]+".local.", port, ips, []string{"id=" + n.id, "name=" + n.name})
	if err != nil {
		return nil, err
	}
	return mdns.NewServer(&mdns.Config{Zone: service})
}

func Discover(ctx context.Context) ([]Discovered, error) {
	entries := make(chan *mdns.ServiceEntry, 32)
	params := mdns.DefaultParams(mdnsService)
	params.Entries = entries
	params.Timeout = 1500 * time.Millisecond
	params.WantUnicastResponse = true
	done := make(chan error, 1)
	go func() { done <- mdns.Query(params); close(entries) }()
	result := make([]Discovered, 0)
	seen := make(map[string]bool)
	for {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case item, open := <-entries:
			if !open {
				return result, <-done
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
			entry := Discovered{Endpoint: net.JoinHostPort(addr.String(), strconv.Itoa(item.Port)), Name: item.Name}
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
