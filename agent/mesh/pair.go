package mesh

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"regexp"
	"time"

	"filippo.io/cpace"
)

var codePattern = regexp.MustCompile(`^[0-9]{8}$`)

type invite struct {
	Code    string    `json:"code"`
	Expires time.Time `json:"expires"`
	tries   int
}

type pairSession struct {
	peer Peer
	key  []byte
}

type pairStart struct {
	Name     string `json:"name"`
	Endpoint string `json:"endpoint"`
	Share    []byte `json:"share"`
}

type pairReply struct {
	Session string `json:"session"`
	Share   []byte `json:"share"`
	Proof   []byte `json:"proof"`
	Name    string `json:"name"`
}

type pairFinish struct {
	Session string `json:"session"`
	Proof   []byte `json:"proof"`
}

func (n *Node) newInvite() (invite, error) {
	number, err := rand.Int(rand.Reader, big.NewInt(100000000))
	if err != nil {
		return invite{}, err
	}
	n.mu.Lock()
	defer n.mu.Unlock()
	n.invite = invite{Code: fmt.Sprintf("%08d", number), Expires: time.Now().Add(2 * time.Minute), tries: 5}
	clear(n.pending)
	return n.invite, nil
}

func pairInfo(clientID, serverID string) *cpace.ContextInfo {
	return cpace.NewContextInfo(clientID, serverID, []byte("anyusb-peer-pair-v1"))
}

func proof(key []byte, role, session string) []byte {
	mac := hmac.New(sha256.New, key)
	mac.Write([]byte("anyusb-peer-pair-v1\x00" + role + "\x00" + session))
	return mac.Sum(nil)
}

func (n *Node) beginPair(id string, input pairStart) (pairReply, error) {
	if id == "" || id == n.id || len(input.Name) > 128 || len(input.Share) > 256 {
		return pairReply{}, failure(http.StatusBadRequest, "invalid pairing request")
	}
	if input.Endpoint != "" {
		var err error
		input.Endpoint, err = PeerEndpoint(input.Endpoint)
		if err != nil {
			return pairReply{}, err
		}
	}
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.invite.tries == 0 || time.Now().After(n.invite.Expires) {
		return pairReply{}, failure(http.StatusForbidden, "no active pairing invitation")
	}
	n.invite.tries--
	share, key, err := cpace.Exchange(n.invite.Code, pairInfo(id, n.id), input.Share)
	if err != nil {
		return pairReply{}, failure(http.StatusBadRequest, "invalid PAKE share")
	}
	session := randomID()
	n.pending[session] = pairSession{peer: Peer{ID: id, Name: input.Name, Endpoint: input.Endpoint}, key: key}
	return pairReply{Session: session, Share: share, Proof: proof(key, "server", session), Name: n.name}, nil
}

func (n *Node) finishPair(id string, input pairFinish) error {
	n.mu.Lock()
	defer n.mu.Unlock()
	session, exists := n.pending[input.Session]
	if !exists || session.peer.ID != id || time.Now().After(n.invite.Expires) {
		return failure(http.StatusForbidden, "invalid or expired pairing session")
	}
	delete(n.pending, input.Session)
	if !hmac.Equal(input.Proof, proof(session.key, "client", input.Session)) {
		return failure(http.StatusForbidden, "pairing code did not match")
	}
	if err := n.addPeerLocked(session.peer); err != nil {
		return err
	}
	n.invite = invite{}
	clear(n.pending)
	return nil
}

func (n *Node) pair(ctx context.Context, endpoint, code string) (Peer, error) {
	if !codePattern.MatchString(code) {
		return Peer{}, failure(http.StatusBadRequest, "pairing code must be eight digits")
	}
	endpoint, err := PeerEndpoint(endpoint)
	if err != nil {
		return Peer{}, err
	}
	unverified := n.peerClient("")
	defer unverified.CloseIdleConnections()
	res, err := send(ctx, unverified, http.MethodGet, peerURL(endpoint, "/v1/pair/info"), nil, "")
	if err != nil {
		return Peer{}, err
	}
	res.Body.Close()
	id := fingerprint(res.TLS.PeerCertificates[0].Raw)
	if id == n.id {
		return Peer{}, failure(http.StatusBadRequest, "cannot pair this agent with itself")
	}
	share, session, err := cpace.Start(code, pairInfo(n.id, id))
	if err != nil {
		return Peer{}, err
	}
	client := n.peerClient(id)
	defer client.CloseIdleConnections()
	reply, err := call[pairReply](ctx, client, http.MethodPost, endpoint, "/v1/pair/start", pairStart{Name: n.name, Endpoint: n.address, Share: share})
	if err != nil {
		return Peer{}, err
	}
	key, err := session.Finish(reply.Share)
	if err != nil || !hmac.Equal(reply.Proof, proof(key, "server", reply.Session)) {
		return Peer{}, failure(http.StatusForbidden, "pairing code or remote identity did not match")
	}
	if _, err := call[struct{}](ctx, client, http.MethodPost, endpoint, "/v1/pair/finish", pairFinish{Session: reply.Session, Proof: proof(key, "client", reply.Session)}); err != nil {
		return Peer{}, err
	}
	peer := Peer{ID: id, Name: reply.Name, Endpoint: endpoint}
	n.mu.Lock()
	defer n.mu.Unlock()
	return peer, n.addPeerLocked(peer)
}

func (n *Node) addPeerLocked(peer Peer) error {
	previous, existed := n.config.Peers[peer.ID]
	n.config.Peers[peer.ID] = peer
	if err := n.saveLocked(); err != nil {
		if existed {
			n.config.Peers[peer.ID] = previous
		} else {
			delete(n.config.Peers, peer.ID)
		}
		return err
	}
	return nil
}

type httpFailure struct {
	Status  int
	Message string
}

func (e httpFailure) Error() string { return e.Message }

func failure(status int, message string) error { return httpFailure{status, message} }

func send(ctx context.Context, client *http.Client, method, endpoint string, body io.Reader, contentType string) (*http.Response, error) {
	request, err := http.NewRequestWithContext(ctx, method, endpoint, body)
	if err != nil {
		return nil, err
	}
	if contentType != "" {
		request.Header.Set("Content-Type", contentType)
	}
	res, err := client.Do(request)
	if err != nil {
		return nil, err
	}
	if res.StatusCode >= 200 && res.StatusCode < 300 {
		return res, nil
	}
	defer res.Body.Close()
	var problem struct{ Message string }
	if err := json.NewDecoder(io.LimitReader(res.Body, 8192)).Decode(&problem); err != nil || problem.Message == "" {
		problem.Message = res.Status
	}
	return nil, failure(res.StatusCode, problem.Message)
}

func call[T any](ctx context.Context, client *http.Client, method, endpoint, path string, payload any) (T, error) {
	var result T
	var body io.Reader
	if payload != nil {
		data, err := json.Marshal(payload)
		if err != nil {
			return result, err
		}
		body = bytes.NewReader(data)
	}
	res, err := send(ctx, client, method, peerURL(endpoint, path), body, "application/json")
	if err != nil {
		return result, err
	}
	defer res.Body.Close()
	err = json.NewDecoder(io.LimitReader(res.Body, 4<<20)).Decode(&result)
	return result, err
}

func isHTTPStatus(err error, status int) bool {
	var problem httpFailure
	return errors.As(err, &problem) && problem.Status == status
}
