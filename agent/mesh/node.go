// Package mesh implements AnyUSB's authenticated peer control plane.
package mesh

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"math/big"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/anmh1205/anyusb/agent/esp"
	"github.com/anmh1205/anyusb/agent/platform"
)

const DefaultPeerPort = "47443"

type Peer struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Endpoint string `json:"endpoint"`
}

type descriptor struct{ VendorID, ProductID, Serial string }

type connectionIntent struct {
	ID           string `json:"id"`
	Peer         string `json:"peer"`
	Device       string `json:"device"`
	RiskAccepted bool   `json:"risk_accepted,omitempty"`
}

type config struct {
	Peers       map[string]Peer             `json:"peers"`
	Shares      map[string][]descriptor     `json:"shares"`
	Connections map[string]connectionIntent `json:"connections,omitempty"`
}

type Node struct {
	adapter platform.Adapter
	ctx     context.Context
	dir     string
	name    string
	address string
	backend string
	cert    tls.Certificate
	id      string
	esp     *esp.Service

	mu          sync.Mutex
	clientMu    sync.Mutex
	config      config
	invite      invite
	pending     map[string]pairSession
	approvals   map[string]approval
	leases      map[string]*lease
	connections map[string]*connection
	subscribers map[chan Event]struct{}
	revision    uint64
	epoch       string
	inventory   map[string]platform.Device
}

func New(ctx context.Context, adapter platform.Adapter, dir, name, advertised, backend string) (*Node, error) {
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, err
	}
	cert, err := identity(filepath.Join(dir, "identity.pem"), name)
	if err != nil {
		return nil, err
	}
	if advertised != "" {
		advertised, err = PeerEndpoint(advertised)
		if err != nil {
			return nil, err
		}
	}
	host, _, err := platform.USBIPEndpoint(backend)
	if err != nil || (host != "127.0.0.1" && host != "::1") {
		return nil, fmt.Errorf("USB/IP backend must use a loopback address")
	}
	n := &Node{
		ctx: ctx, adapter: adapter, dir: dir, name: name, address: advertised, backend: backend,
		cert: cert, id: fingerprint(cert.Certificate[0]), epoch: randomID(),
		pending: make(map[string]pairSession), approvals: make(map[string]approval), leases: make(map[string]*lease),
		connections: make(map[string]*connection), subscribers: make(map[chan Event]struct{}),
		inventory: make(map[string]platform.Device),
	}
	data, err := os.ReadFile(filepath.Join(dir, "state.json"))
	if err == nil {
		if err := json.Unmarshal(data, &n.config); err != nil {
			return nil, fmt.Errorf("read AnyUSB peer store: %w", err)
		}
	} else if !os.IsNotExist(err) {
		return nil, err
	}
	if n.config.Peers == nil {
		n.config.Peers = make(map[string]Peer)
	}
	if n.config.Shares == nil {
		n.config.Shares = make(map[string][]descriptor)
	}
	if n.config.Connections == nil {
		n.config.Connections = make(map[string]connectionIntent)
	}
	go n.reconcileLoop()
	n.restoreConnections()
	return n, nil
}

func (n *Node) TLSConfig() *tls.Config {
	return &tls.Config{MinVersion: tls.VersionTLS13, Certificates: []tls.Certificate{n.cert}, ClientAuth: tls.RequestClientCert}
}

func (n *Node) saveLocked() error {
	data, err := json.MarshalIndent(n.config, "", "  ")
	if err != nil {
		return err
	}
	return atomicWrite(filepath.Join(n.dir, "state.json"), data)
}

func atomicWrite(path string, data []byte) error {
	f, err := os.CreateTemp(filepath.Dir(path), ".anyusb-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	_, err = f.Write(data)
	if err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	return os.Rename(f.Name(), path)
}

func identity(path, name string) (tls.Certificate, error) {
	if data, err := os.ReadFile(path); err == nil {
		return tls.X509KeyPair(data, data)
	} else if !os.IsNotExist(err) {
		return tls.Certificate{}, err
	}
	public, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return tls.Certificate{}, err
	}
	now := time.Now()
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return tls.Certificate{}, err
	}
	template := &x509.Certificate{
		SerialNumber: serial, Subject: pkix.Name{CommonName: name}, NotBefore: now.Add(-time.Hour), NotAfter: now.AddDate(10, 0, 0),
		KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth, x509.ExtKeyUsageClientAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, public, private)
	if err != nil {
		return tls.Certificate{}, err
	}
	key, err := x509.MarshalPKCS8PrivateKey(private)
	if err != nil {
		return tls.Certificate{}, err
	}
	certificate := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	privatePEM := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: key})
	if err := atomicWrite(path, append(certificate, privatePEM...)); err != nil {
		return tls.Certificate{}, err
	}
	return tls.X509KeyPair(certificate, privatePEM)
}

func fingerprint(cert []byte) string {
	hash := sha256.Sum256(cert)
	return hex.EncodeToString(hash[:])
}

func randomID() string {
	var random [16]byte
	if _, err := rand.Read(random[:]); err != nil {
		panic(err)
	}
	return hex.EncodeToString(random[:])
}

func PeerEndpoint(endpoint string) (string, error) {
	if _, _, err := net.SplitHostPort(endpoint); err != nil {
		endpoint = net.JoinHostPort(strings.Trim(endpoint, "[]"), DefaultPeerPort)
	}
	host, port, err := platform.USBIPEndpoint(endpoint)
	if err != nil {
		return "", err
	}
	return net.JoinHostPort(host, port), nil
}

func peerURL(endpoint, path string) string {
	base := url.URL{Scheme: "https", Host: endpoint}
	return base.String() + path
}

func (n *Node) peerClient(id string) *http.Client {
	tlsConfig := &tls.Config{
		MinVersion: tls.VersionTLS13, Certificates: []tls.Certificate{n.cert},
		// Pairing initially pins the certificate through PAKE. Subsequent connections compare the stored pin.
		InsecureSkipVerify: true,
		VerifyConnection: func(state tls.ConnectionState) error {
			if len(state.PeerCertificates) == 0 {
				return fmt.Errorf("peer has no certificate")
			}
			cert := state.PeerCertificates[0]
			if time.Now().Before(cert.NotBefore) || time.Now().After(cert.NotAfter) || (id != "" && fingerprint(cert.Raw) != id) {
				return fmt.Errorf("peer TLS identity does not match the pairing")
			}
			return nil
		},
	}
	return &http.Client{
		Transport: &http.Transport{TLSClientConfig: tlsConfig, DialContext: (&net.Dialer{Timeout: 5 * time.Second}).DialContext,
			TLSHandshakeTimeout: 5 * time.Second, ResponseHeaderTimeout: 20 * time.Second, IdleConnTimeout: 30 * time.Second},
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
}

func peerID(r *http.Request) string {
	if r.TLS == nil || len(r.TLS.PeerCertificates) == 0 {
		return ""
	}
	cert := r.TLS.PeerCertificates[0]
	now := time.Now()
	if now.Before(cert.NotBefore) || now.After(cert.NotAfter) {
		return ""
	}
	return fingerprint(cert.Raw)
}
