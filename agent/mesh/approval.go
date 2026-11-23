package mesh

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"net"
	"net/http"
	"sort"
	"time"
)

const (
	approvalTTL  = 2 * time.Minute
	maxApprovals = 24
	approvalPath = "/v1/pair/approvals"
)

type approval struct {
	ID       string    `json:"id"`
	Peer     Peer      `json:"peer"`
	Code     string    `json:"code"`
	Status   string    `json:"status"`
	Expires  time.Time `json:"expires"`
	Incoming bool      `json:"incoming"`
	commit   []byte
	nonce    []byte
}

type approvalStart struct {
	Name     string `json:"name"`
	Endpoint string `json:"endpoint"`
	Commit   []byte `json:"commit"`
}

type approvalReply struct {
	ID      string    `json:"id"`
	Name    string    `json:"name"`
	Nonce   []byte    `json:"nonce"`
	Expires time.Time `json:"expires"`
}

func approvalCommit(client, host string, nonce []byte) []byte {
	digest := sha256.New()
	digest.Write([]byte("AnyUSB approval commitment v1\x00" + client + host))
	digest.Write(nonce)
	return digest.Sum(nil)
}

func approvalCode(client, host, id string, clientNonce, hostNonce []byte) string {
	digest := sha256.New()
	digest.Write([]byte("AnyUSB approval code v1\x00" + client + host + id))
	digest.Write(clientNonce)
	digest.Write(hostNonce)
	return fmt.Sprintf("%08d", binary.BigEndian.Uint32(digest.Sum(nil))%100000000)
}

func (n *Node) approvalLocked(id string) (approval, bool) {
	for key, req := range n.approvals {
		if time.Now().After(req.Expires) {
			delete(n.approvals, key)
		}
	}
	req, found := n.approvals[id]
	return req, found
}

func (n *Node) beginApproval(id, address string, input approvalStart) (approvalReply, error) {
	if id == "" || id == n.id || len(input.Name) > 128 || len(input.Commit) != 32 {
		return approvalReply{}, failure(http.StatusBadRequest, "invalid pairing request")
	}
	if input.Endpoint == "" {
		host, _, err := net.SplitHostPort(address)
		if err != nil {
			return approvalReply{}, failure(http.StatusBadRequest, "missing peer endpoint")
		}
		input.Endpoint = net.JoinHostPort(host, DefaultPeerPort)
	}
	endpoint, err := PeerEndpoint(input.Endpoint)
	if err != nil {
		return approvalReply{}, err
	}
	n.mu.Lock()
	defer n.mu.Unlock()
	n.approvalLocked("")
	for _, previous := range n.approvals {
		if previous.Incoming && previous.Peer.ID == id && previous.Status == "pending" {
			return approvalReply{}, failure(http.StatusConflict, "a pairing request is already waiting")
		}
	}
	if len(n.approvals) >= maxApprovals {
		return approvalReply{}, failure(http.StatusTooManyRequests, "try pairing again in a minute")
	}
	reply := approvalReply{ID: randomID(), Name: n.name, Nonce: make([]byte, 32), Expires: time.Now().Add(approvalTTL)}
	if _, err := rand.Read(reply.Nonce); err != nil {
		return approvalReply{}, err
	}
	n.approvals[reply.ID] = approval{ID: reply.ID, Peer: Peer{ID: id, Name: input.Name, Endpoint: endpoint},
		Status: "pending", Expires: reply.Expires, Incoming: true, commit: input.Commit, nonce: reply.Nonce}
	return reply, nil
}

func (n *Node) revealApproval(peer, id string, nonce []byte) error {
	n.mu.Lock()
	defer n.mu.Unlock()
	req, ok := n.approvalLocked(id)
	if !ok || !req.Incoming || req.Peer.ID != peer || len(nonce) != 32 || !bytes.Equal(req.commit, approvalCommit(peer, n.id, nonce)) {
		return failure(http.StatusForbidden, "invalid or expired pairing request")
	}
	req.Code = approvalCode(peer, n.id, id, nonce, req.nonce)
	n.approvals[id] = req
	return nil
}

func (n *Node) remoteApproval(peer, id string) (string, error) {
	n.mu.Lock()
	defer n.mu.Unlock()
	req, ok := n.approvalLocked(id)
	if !ok || !req.Incoming || req.Peer.ID != peer {
		return "", failure(http.StatusNotFound, "pairing request expired")
	}
	return req.Status, nil
}

func (n *Node) decideApproval(id string, approve bool) error {
	n.mu.Lock()
	defer n.mu.Unlock()
	req, ok := n.approvalLocked(id)
	if !ok || !req.Incoming || req.Code == "" || req.Status != "pending" {
		return failure(http.StatusNotFound, "pairing request is no longer waiting")
	}
	if approve {
		if err := n.addPeerLocked(req.Peer); err != nil {
			return err
		}
		req.Status = "approved"
	} else {
		req.Status = "declined"
	}
	n.approvals[id] = req
	return nil
}

func (n *Node) requestApproval(ctx context.Context, endpoint, fingerprint string) (approval, error) {
	endpoint, err := PeerEndpoint(endpoint)
	if err != nil {
		return approval{}, err
	}
	if len(fingerprint) != 64 || fingerprint == n.id {
		return approval{}, failure(http.StatusBadRequest, "select a different AnyUSB device")
	}
	client := n.peerClient(fingerprint)
	defer client.CloseIdleConnections()
	nonce := make([]byte, 32)
	if _, err := rand.Read(nonce); err != nil {
		return approval{}, err
	}
	start := approvalStart{Name: n.name, Endpoint: n.address, Commit: approvalCommit(n.id, fingerprint, nonce)}
	reply, err := call[approvalReply](ctx, client, http.MethodPost, endpoint, approvalPath, start)
	if err != nil {
		return approval{}, err
	}
	if !requestIDPattern.MatchString(reply.ID) || len(reply.Nonce) != 32 || len(reply.Name) > 128 {
		return approval{}, failure(http.StatusBadGateway, "invalid pairing response")
	}
	if _, err := call[struct{}](ctx, client, http.MethodPost, endpoint, approvalPath+"/"+reply.ID, struct{ Nonce []byte }{nonce}); err != nil {
		return approval{}, err
	}
	req := approval{ID: reply.ID, Peer: Peer{ID: fingerprint, Name: reply.Name, Endpoint: endpoint},
		Status: "pending", Expires: time.Now().Add(approvalTTL), Code: approvalCode(n.id, fingerprint, reply.ID, nonce, reply.Nonce)}
	n.mu.Lock()
	defer n.mu.Unlock()
	n.approvalLocked("")
	if len(n.approvals) >= maxApprovals {
		return approval{}, failure(http.StatusTooManyRequests, "try pairing again in a minute")
	}
	n.approvals[req.ID] = req
	return req, nil
}

func (n *Node) approvalsForUI(ctx context.Context) []approval {
	n.mu.Lock()
	n.approvalLocked("")
	views := make([]approval, 0, len(n.approvals))
	for _, req := range n.approvals {
		if req.Code != "" && (!req.Incoming || req.Status == "pending") {
			views = append(views, req)
		}
	}
	n.mu.Unlock()
	for i, req := range views {
		if ctx.Err() != nil {
			break
		}
		if !req.Incoming && req.Status == "pending" {
			views[i] = n.pollApproval(ctx, req)
		}
	}
	sort.Slice(views, func(i, j int) bool { return views[i].Expires.Before(views[j].Expires) })
	return views
}

func (n *Node) pollApproval(ctx context.Context, req approval) approval {
	ctx, cancel := context.WithTimeout(ctx, 1500*time.Millisecond)
	defer cancel()
	client := n.peerClient(req.Peer.ID)
	defer client.CloseIdleConnections()
	result, err := call[string](ctx, client, http.MethodGet, req.Peer.Endpoint, approvalPath+"/"+req.ID, nil)
	if err != nil || (result != "approved" && result != "declined") {
		return req
	}
	n.mu.Lock()
	defer n.mu.Unlock()
	current, ok := n.approvalLocked(req.ID)
	if !ok || (result == "approved" && current.Status == "pending" && n.addPeerLocked(req.Peer) != nil) {
		return req
	}
	if current.Status != "pending" {
		return current
	}
	req.Status = result
	n.approvals[req.ID] = req
	return req
}
