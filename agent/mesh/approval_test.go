package mesh

import (
	"context"
	"net/http"
	"strings"
	"testing"
)

func TestApprovalPairingAndRevocation(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	host, address := nodeForTest(t, ctx, &testAdapter{}, "127.0.0.1:3240")
	client, _ := nodeForTest(t, ctx, &testAdapter{}, "127.0.0.1:3240")
	candidate, ok := client.probePeer(ctx, Discovered{Endpoint: address, Network: "LAN"})
	if !ok || candidate.ID != host.id {
		t.Fatalf("discovery = %+v, %t", candidate, ok)
	}
	if _, ok := client.probePeer(ctx, Discovered{Endpoint: address, ID: client.id}); ok {
		t.Fatal("discovery accepted a mismatched TLS pin")
	}
	req, err := client.requestApproval(ctx, candidate.Endpoint, candidate.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(req.Code) != 4 || req.Status != "pending" {
		t.Fatalf("outgoing request = %+v", req)
	}
	incoming := host.approvalsForUI(ctx)
	if len(incoming) != 1 || incoming[0].Code != req.Code || !incoming[0].Incoming {
		t.Fatalf("incoming request = %+v, outgoing = %+v", incoming, req)
	}
	if err := host.revealApproval(client.id, req.ID, make([]byte, 32)); !isHTTPStatus(err, http.StatusForbidden) {
		t.Fatalf("wrong commitment = %v", err)
	}
	peerClient := client.peerClient(host.id)
	defer peerClient.CloseIdleConnections()
	if _, err := call[[]SharedDevice](ctx, peerClient, http.MethodGet, address, "/v1/shared", nil); !isHTTPStatus(err, http.StatusUnauthorized) {
		t.Fatalf("access before approval = %v", err)
	}
	if err := host.decideApproval(req.ID, true); err != nil {
		t.Fatal(err)
	}
	views := client.approvalsForUI(ctx)
	if len(views) != 1 || views[0].Status != "approved" || !host.trusted(client.id) || !client.trusted(host.id) {
		t.Fatalf("approvals did not establish trust: %+v", views)
	}
	if _, err := call[[]SharedDevice](ctx, peerClient, http.MethodGet, address, "/v1/shared", nil); err != nil {
		t.Fatal(err)
	}
	if err := host.revoke(client.id); err != nil {
		t.Fatal(err)
	}
	if _, err := call[[]SharedDevice](ctx, peerClient, http.MethodGet, address, "/v1/shared", nil); !isHTTPStatus(err, http.StatusUnauthorized) {
		t.Fatalf("access after revocation = %v", err)
	}
}

func TestTailnetDiscoveryCandidates(t *testing.T) {
	status := []byte(`{"BackendState":"Running","Peer":{
		"present":{"Online":true,"TailscaleIPs":["100.94.74.49","fd7a:115c:a1e0::1234","192.0.2.1"]},
		"absent":{"Online":false,"TailscaleIPs":["100.70.0.1"]}}}`)
	got := tailnetCandidates(status)
	if len(got) != 2 || got[0].Endpoint != "100.94.74.49:47443" || got[1].Endpoint != "[fd7a:115c:a1e0::1234]:47443" {
		t.Fatalf("candidates = %+v", got)
	}
}

func TestDiscoveryPrefersTailscaleIPv4AndSyncsPeerMetadata(t *testing.T) {
	peerID := strings.Repeat("b", 64)
	node := &Node{
		config: config{
			Peers: map[string]Peer{
				peerID: {ID: peerID, Name: "old-name", Endpoint: "192.168.1.99:47443"},
			},
			Shares:      make(map[string][]descriptor),
			Connections: make(map[string]connectionIntent),
			Fences:      make(map[string]LeaseFence),
		},
		dir: t.TempDir(),
	}
	results := node.devicesFromDiscovery([]Discovered{
		{ID: peerID, Name: "ubuntu-host", Endpoint: "192.168.1.50:47443", Network: "LAN"},
		{ID: peerID, Name: "ubuntu-host", Endpoint: "100.94.74.49:47443", Network: "Tailscale"},
		{ID: peerID, Name: "ubuntu-host", Endpoint: "[fd7a:115c:a1e0::1234]:47443", Network: "Tailscale"},
	})
	if len(results) != 1 || results[0].Endpoint != "100.94.74.49:47443" || results[0].Network != "Tailscale" {
		t.Fatalf("discovery priority = %+v", results)
	}
	saved := node.config.Peers[peerID]
	if saved.Endpoint != "100.94.74.49:47443" || saved.Name != "ubuntu-host" {
		t.Fatalf("synced peer = %+v", saved)
	}
}
