package mesh

import (
	"context"
	"fmt"
	"net/http"
	"slices"
	"sort"
	"time"

	"github.com/anmh1205/anyusb/agent/platform"
)

const leaseTTL = 30 * time.Second

type Lease struct {
	ID      string    `json:"id"`
	Device  string    `json:"device"`
	Owner   string    `json:"owner"`
	Mode    string    `json:"mode"`
	BusID   string    `json:"bus_id,omitempty"`
	Expires time.Time `json:"expires"`
	Online  bool      `json:"online"`
	Closing bool      `json:"closing"`
}

type lease struct {
	Lease
	device     platform.Device
	stopTunnel context.CancelFunc
	busy       bool
	attached   bool
}

type SharedDevice struct {
	platform.Device
	Present bool   `json:"present"`
	Owner   string `json:"lease_owner,omitempty"`
	Mode    string `json:"lease_mode,omitempty"`
}

type Event struct {
	Epoch  string `json:"epoch"`
	Seq    uint64 `json:"seq"`
	Type   string `json:"type"`
	Device string `json:"device"`
}

func (n *Node) publishLocked(kind, id string) {
	n.revision++
	event := Event{Epoch: n.epoch, Seq: n.revision, Type: kind, Device: id}
	for out := range n.subscribers {
		select {
		case out <- event:
		default:
			delete(n.subscribers, out)
			close(out)
		}
	}
}

func (n *Node) subscribe() (<-chan Event, func()) {
	n.mu.Lock()
	defer n.mu.Unlock()
	channel := make(chan Event, 32)
	channel <- Event{Epoch: n.epoch, Seq: n.revision, Type: "snapshot_required"}
	n.subscribers[channel] = struct{}{}
	return channel, func() {
		n.mu.Lock()
		defer n.mu.Unlock()
		if _, ok := n.subscribers[channel]; ok {
			delete(n.subscribers, channel)
			close(channel)
		}
	}
}

func snapshot(ctx context.Context, adapter platform.Adapter) (map[string]platform.Device, error) {
	devices, err := adapter.ListLocalDevices(ctx)
	if err != nil {
		return nil, err
	}
	result := make(map[string]platform.Device, len(devices))
	for _, device := range devices {
		if _, duplicate := result[device.ID]; duplicate {
			return nil, fmt.Errorf("%w: duplicate physical port identity %s", platform.ErrConflict, device.ID)
		}
		result[device.ID] = device
	}
	return result, nil
}

func describe(d platform.Device) descriptor { return descriptor{d.VendorID, d.ProductID, d.Serial} }

func (n *Node) RawShare(ctx context.Context, target string, enabled bool) (platform.Device, error) {
	n.mu.Lock()
	defer n.mu.Unlock()
	devices, err := snapshot(ctx, n.adapter)
	if err != nil {
		return platform.Device{}, err
	}
	for _, d := range devices {
		if d.ID != target && d.BusID != target {
			continue
		}
		if n.config.Shares[d.ID] != nil || n.leases[d.ID] != nil {
			return platform.Device{}, fmt.Errorf("%w: disable managed sharing before using stock USB/IP for this device", platform.ErrConflict)
		}
		if enabled {
			return n.adapter.Export(ctx, d.ID)
		}
		return n.adapter.Unexport(ctx, d.ID)
	}
	return platform.Device{}, platform.ErrNotFound
}

func (n *Node) shared(ctx context.Context) ([]SharedDevice, error) {
	devices, err := snapshot(ctx, n.adapter)
	if err != nil {
		return nil, err
	}
	n.mu.Lock()
	defer n.mu.Unlock()
	result := make([]SharedDevice, 0, len(n.config.Shares))
	for id, variants := range n.config.Shares {
		d := devices[id]
		item := SharedDevice{Device: d, Present: d.ID != "" && slices.Contains(variants, describe(d))}
		if !item.Present {
			item.Device = platform.Device{ID: id}
		}
		if le := n.leases[id]; le != nil {
			item.Owner, item.Mode = le.Owner, le.Mode
		}
		result = append(result, item)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].ID < result[j].ID })
	return result, nil
}

func (n *Node) setShared(ctx context.Context, target string, enabled bool) error {
	n.mu.Lock()
	defer n.mu.Unlock()
	id := target
	var d platform.Device
	if enabled || n.config.Shares[id] == nil {
		devices, err := snapshot(ctx, n.adapter)
		if err != nil {
			return err
		}
		for _, candidate := range devices {
			if candidate.ID == target || candidate.BusID == target {
				d, id = candidate, candidate.ID
				break
			}
		}
		if d.ID == "" {
			return platform.ErrNotFound
		}
	}
	if n.leases[id] != nil {
		return fmt.Errorf("%w: device has an active lease", platform.ErrConflict)
	}
	previous := n.config.Shares[id]
	if enabled {
		if slices.Contains(previous, describe(d)) {
			return nil
		}
		n.config.Shares[id] = append(slices.Clone(previous), describe(d))
	} else {
		delete(n.config.Shares, id)
	}
	if err := n.saveLocked(); err != nil {
		if previous == nil {
			delete(n.config.Shares, id)
		} else {
			n.config.Shares[id] = previous
		}
		return err
	}
	n.publishLocked("sharing_changed", id)
	return nil
}

func (n *Node) acquire(ctx context.Context, owner, id, mode string) (Lease, error) {
	n.mu.Lock()
	defer n.mu.Unlock()
	if _, ok := n.config.Peers[owner]; !ok {
		return Lease{}, failure(http.StatusForbidden, "peer has been revoked")
	}
	if current := n.leases[id]; current != nil {
		if current.Owner == owner && current.Mode == "usbip" && mode == "usbip" && !current.Closing {
			current.Expires = time.Now().Add(leaseTTL)
			return current.Lease, nil
		}
		peer := n.config.Peers[current.Owner]
		label := peer.Name
		if label == "" {
			label = current.Owner
		}
		return Lease{}, fmt.Errorf("%w: leased to %s (%s)", platform.ErrConflict, label, current.Mode)
	}
	devices, err := snapshot(ctx, n.adapter)
	if err != nil {
		return Lease{}, err
	}
	d := devices[id]
	if d.ID == "" || !slices.Contains(n.config.Shares[id], describe(d)) {
		return Lease{}, fmt.Errorf("%w: device is absent or its current USB identity was not shared", platform.ErrNotFound)
	}
	if mode == "usbip" && !d.Exported {
		d, err = n.adapter.Export(ctx, d.BusID)
	} else if mode == "esp" && d.Exported {
		d, err = n.adapter.Unexport(ctx, d.BusID)
	}
	if err != nil {
		return Lease{}, err
	}
	if d.ID != id || !slices.Contains(n.config.Shares[id], describe(d)) {
		return Lease{}, fmt.Errorf("%w: device changed while preparing the lease", platform.ErrConflict)
	}
	le := &lease{Lease: Lease{ID: randomID(), Device: id, Owner: owner, Mode: mode, BusID: d.BusID, Online: true, Expires: time.Now().Add(leaseTTL)}, device: d, busy: mode == "esp"}
	n.leases[id] = le
	n.publishLocked("leased", id)
	return le.Lease, nil
}

func (n *Node) currentLeaseLocked(owner, token string) *lease {
	for _, current := range n.leases {
		if current.ID == token && current.Owner == owner {
			return current
		}
	}
	return nil
}

func (n *Node) renew(owner, token string) (Lease, error) {
	n.mu.Lock()
	defer n.mu.Unlock()
	le := n.currentLeaseLocked(owner, token)
	if le == nil || le.Closing || time.Now().After(le.Expires) {
		return Lease{}, platform.ErrNotFound
	}
	le.Expires = time.Now().Add(leaseTTL)
	return le.Lease, nil
}

func (n *Node) release(owner, token string) error {
	n.mu.Lock()
	defer n.mu.Unlock()
	le := n.currentLeaseLocked(owner, token)
	if le == nil {
		return nil
	}
	if le.busy {
		return fmt.Errorf("%w: an ESP operation is running", platform.ErrConflict)
	}
	n.closeLeaseLocked(le)
	return nil
}

func (n *Node) closeLeaseLocked(le *lease) {
	le.Closing = true
	if le.stopTunnel != nil {
		le.stopTunnel()
	}
}

func (n *Node) BeginESP(ctx context.Context, owner, device, requested string) (string, func(), error) {
	le, err := n.acquire(ctx, owner, device, "esp")
	if err != nil {
		return "", nil, err
	}
	finish := func() {
		n.mu.Lock()
		defer n.mu.Unlock()
		if current := n.currentLeaseLocked(owner, le.ID); current != nil {
			delete(n.leases, le.Device)
			n.publishLocked("released", le.Device)
		}
	}
	ports, err := n.adapter.LocalSerialPorts(ctx, le.Device)
	if err == nil {
		slices.Sort(ports)
		ports = slices.Compact(ports)
		switch {
		case requested != "" && slices.Contains(ports, requested):
			return requested, finish, nil
		case requested == "" && len(ports) == 1:
			return ports[0], finish, nil
		default:
			err = fmt.Errorf("%w: select a current host serial port; available: %v", platform.ErrConflict, ports)
		}
	}
	finish()
	return "", nil, err
}

func (n *Node) reconcileLoop() {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-n.ctx.Done():
			return
		case <-ticker.C:
			n.reconcile()
		}
	}
}

func (n *Node) reconcile() {
	n.mu.Lock()
	defer n.mu.Unlock()
	if len(n.config.Shares) == 0 && len(n.leases) == 0 {
		return
	}
	devices, err := snapshot(n.ctx, n.adapter)
	if err == nil {
		for id, variants := range n.config.Shares {
			d := devices[id]
			old, seen := n.inventory[id]
			approved := d.ID != "" && slices.Contains(variants, describe(d))
			switch {
			case approved && (!seen || !platform.SameIncarnation(old, d)):
				if seen {
					n.publishLocked("removed", id)
				}
				n.publishLocked("arrived", id)
				n.inventory[id] = d
			case !approved && seen:
				n.publishLocked("removed", id)
				delete(n.inventory, id)
			}
			if approved && n.leases[id] == nil && d.Exported {
				// Restore a private managed share after an unclean agent shutdown.
				_, _ = n.adapter.Unexport(n.ctx, d.BusID)
			}
		}
	}
	for id, le := range n.leases {
		if le.busy {
			continue
		}
		if time.Now().After(le.Expires) {
			n.closeLeaseLocked(le)
		}
		if err != nil {
			continue
		}
		d := devices[id]
		approved := d.ID != "" && slices.Contains(n.config.Shares[id], describe(d))
		if le.Closing {
			if approved && d.Exported {
				if _, err := n.adapter.Unexport(n.ctx, d.BusID); err != nil {
					continue
				}
			}
			delete(n.leases, id)
			n.publishLocked("released", id)
			continue
		}
		if !approved || !platform.SameIncarnation(d, le.device) || !d.Exported {
			if le.Online {
				le.Online, le.BusID = false, ""
				if le.stopTunnel != nil {
					le.stopTunnel()
				}
				n.publishLocked("lost", id)
			}
		}
		if approved && !le.Online && le.stopTunnel == nil {
			if !d.Exported {
				exported, exportErr := n.adapter.Export(n.ctx, d.BusID)
				if exportErr != nil || exported.ID != id || !slices.Contains(n.config.Shares[id], describe(exported)) {
					continue
				}
				d = exported
			}
			le.device, le.BusID, le.Online = d, d.BusID, true
			n.publishLocked("exported", id)
		}
	}
}

func (n *Node) Drain(ctx context.Context) {
	n.mu.Lock()
	sessions := make([]*connection, 0, len(n.connections))
	for _, session := range n.connections {
		session.cancel()
		sessions = append(sessions, session)
	}
	for _, le := range n.leases {
		if !le.busy {
			n.closeLeaseLocked(le)
		}
	}
	n.mu.Unlock()
	for _, session := range sessions {
		select {
		case <-session.done:
		case <-ctx.Done():
			return
		}
	}
	n.mu.Lock()
	defer n.mu.Unlock()
	devices, err := snapshot(ctx, n.adapter)
	if err != nil {
		return
	}
	for id, le := range n.leases {
		d := devices[id]
		if !le.busy && d.Exported && slices.Contains(n.config.Shares[id], describe(d)) {
			_, _ = n.adapter.Unexport(ctx, d.BusID)
		}
	}
}
