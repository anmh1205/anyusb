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
const forceReleaseCooldown = time.Minute

type Lease struct {
	ID         string           `json:"id"`
	Device     string           `json:"device"`
	Owner      string           `json:"owner"`
	Mode       string           `json:"mode"`
	BusID      string           `json:"bus_id,omitempty"`
	DeviceInfo *platform.Device `json:"device_info,omitempty"`
	Expires    time.Time        `json:"expires"`
	Online     bool             `json:"online"`
	Closing    bool             `json:"closing"`
	Generation uint64           `json:"generation"`
}

type LeaseFence struct {
	Peer       string    `json:"peer"`
	Device     string    `json:"device"`
	Generation uint64    `json:"generation"`
	Until      time.Time `json:"until"`
}

type lease struct {
	Lease
	device       platform.Device
	stopTunnel   context.CancelFunc
	tunnelDone   chan struct{}
	busy         bool
	attached     bool
	riskAccepted bool
}

type SharedDevice struct {
	platform.Device
	Present bool   `json:"present"`
	Owner   string `json:"lease_owner,omitempty"`
	Mode    string `json:"lease_mode,omitempty"`
}

type Event struct {
	Epoch     string    `json:"epoch"`
	Seq       uint64    `json:"seq"`
	Type      string    `json:"type"`
	Device    string    `json:"device"`
	At        time.Time `json:"at,omitempty"`
	Code      string    `json:"code,omitempty"`
	Operation string    `json:"operation,omitempty"`
	Attempt   int       `json:"attempt,omitempty"`
}

type DiagnosticSnapshot struct {
	StateVersion int                    `json:"state_version"`
	Epoch        string                 `json:"epoch"`
	Revision     uint64                 `json:"revision"`
	Connections  []DiagnosticConnection `json:"connections"`
	Events       []Event                `json:"recent_events"`
	LeaseFences  []LeaseFence           `json:"active_lease_fences,omitempty"`
}

type DiagnosticConnection struct {
	ID          string             `json:"id"`
	Device      string             `json:"device"`
	State       string             `json:"state"`
	Phase       string             `json:"phase,omitempty"`
	Readiness   string             `json:"readiness,omitempty"`
	Attempt     int                `json:"attempt,omitempty"`
	NextRetryAt *time.Time         `json:"next_retry_at,omitempty"`
	LastFailure *ConnectionFailure `json:"last_failure,omitempty"`
	Stats       TunnelStats        `json:"stats"`
}

func (n *Node) publishLocked(kind, id string) {
	n.publishEventLocked(Event{Type: kind, Device: id})
}

func (n *Node) publishEventLocked(event Event) {
	n.revision++
	event.Epoch, event.Seq, event.At = n.epoch, n.revision, time.Now().UTC()
	n.recentEvents = append(n.recentEvents, event)
	if len(n.recentEvents) > 128 {
		n.recentEvents = append([]Event(nil), n.recentEvents[len(n.recentEvents)-128:]...)
	}
	for out := range n.subscribers {
		select {
		case out <- event:
		default:
			delete(n.subscribers, out)
			close(out)
		}
	}
}

func (n *Node) Diagnostics() DiagnosticSnapshot {
	n.mu.Lock()
	defer n.mu.Unlock()
	connections := make([]DiagnosticConnection, 0, len(n.connections))
	for _, current := range n.connections {
		view := DiagnosticConnection{ID: current.ID, Device: current.Device, State: current.State,
			Phase: current.Phase, Attempt: current.Attempt, NextRetryAt: current.NextRetryAt, Stats: current.statsSnapshot()}
		if current.Import != nil {
			view.Readiness = current.Import.Readiness
		}
		if current.LastFailure != nil {
			redacted := *current.LastFailure
			redacted.Message = ""
			view.LastFailure = &redacted
		}
		connections = append(connections, view)
	}
	sort.Slice(connections, func(i, j int) bool { return connections[i].ID < connections[j].ID })
	fences := make([]LeaseFence, 0, len(n.config.Fences))
	for _, fence := range n.config.Fences {
		if time.Now().Before(fence.Until) {
			fences = append(fences, fence)
		}
	}
	sort.Slice(fences, func(i, j int) bool {
		if fences[i].Device != fences[j].Device {
			return fences[i].Device < fences[j].Device
		}
		return fences[i].Peer < fences[j].Peer
	})
	return DiagnosticSnapshot{
		StateVersion: currentStateVersion, Epoch: n.epoch, Revision: n.revision,
		Connections: connections, Events: append([]Event(nil), n.recentEvents...), LeaseFences: fences,
	}
}

func (n *Node) subscribe() (<-chan Event, func()) {
	n.mu.Lock()
	defer n.mu.Unlock()
	channel := make(chan Event, 32)
	channel <- Event{Epoch: n.epoch, Seq: n.revision, Type: "snapshot_required", At: time.Now().UTC()}
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
	n.hostMu.Lock()
	defer n.hostMu.Unlock()
	devices, err := snapshot(ctx, n.adapter)
	if err != nil {
		return platform.Device{}, err
	}
	for _, d := range devices {
		if d.ID != target && d.BusID != target {
			continue
		}
		n.mu.Lock()
		conflict := n.config.Shares[d.ID] != nil || n.leases[d.ID] != nil
		n.mu.Unlock()
		if conflict {
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
	n.hostMu.Lock()
	defer n.hostMu.Unlock()
	id := target
	n.mu.Lock()
	needSnapshot := enabled || n.config.Shares[id] == nil
	n.mu.Unlock()
	var d platform.Device
	if needSnapshot {
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
		if d.SharePolicy == platform.ShareBlock {
			return fmt.Errorf("%w: this USB device is outside the whole-device share boundary", platform.ErrInvalid)
		}
	}
	n.mu.Lock()
	defer n.mu.Unlock()
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
	return n.acquireWithPolicy(ctx, owner, id, mode, mode == "esp")
}

func (n *Node) acquireWithPolicy(ctx context.Context, owner, id, mode string, riskAccepted bool) (Lease, error) {
	n.hostMu.Lock()
	defer n.hostMu.Unlock()
	n.mu.Lock()
	if _, ok := n.config.Peers[owner]; !ok {
		n.mu.Unlock()
		return Lease{}, failure(http.StatusForbidden, "peer has been revoked")
	}
	if fence, blocked := n.config.Fences[leaseFenceKey(owner, id)]; blocked && time.Now().Before(fence.Until) {
		n.mu.Unlock()
		return Lease{}, withFailureCode("force_release_cooldown", fmt.Errorf("%w: lease was force-released; retry after %s", platform.ErrConflict, fence.Until.Format(time.RFC3339)))
	}
	if current := n.leases[id]; current != nil {
		if current.Owner == owner && current.Mode == "usbip" && mode == "usbip" && !current.Closing && current.stopTunnel != nil {
			stop, done := current.stopTunnel, current.tunnelDone
			n.mu.Unlock()
			stop()
			if done != nil {
				timer := time.NewTimer(2 * time.Second)
				select {
				case <-done:
					timer.Stop()
				case <-timer.C:
				case <-ctx.Done():
					timer.Stop()
					return Lease{}, ctx.Err()
				}
			}
			n.mu.Lock()
			if _, ok := n.config.Peers[owner]; !ok {
				n.mu.Unlock()
				return Lease{}, failure(http.StatusForbidden, "peer has been revoked")
			}
			if fence, blocked := n.config.Fences[leaseFenceKey(owner, id)]; blocked && time.Now().Before(fence.Until) {
				n.mu.Unlock()
				return Lease{}, withFailureCode("force_release_cooldown", fmt.Errorf("%w: lease was force-released; retry after %s", platform.ErrConflict, fence.Until.Format(time.RFC3339)))
			}
			current = n.leases[id]
		}
		if current != nil {
			if current.Owner == owner && current.Mode == "usbip" && mode == "usbip" && !current.Closing {
				accepted := current.riskAccepted || riskAccepted
				if err := validateManagedUSBPolicy(current.device, accepted); err != nil {
					n.closeLeaseLocked(current)
					n.mu.Unlock()
					return Lease{}, err
				}
				current.riskAccepted = accepted
				current.Expires = time.Now().Add(leaseTTL)
				leaseCopy := current.Lease
				n.mu.Unlock()
				return leaseCopy, nil
			}
			peer := n.config.Peers[current.Owner]
			label := peer.Name
			if label == "" {
				label = current.Owner
			}
			modeLabel := current.Mode
			n.mu.Unlock()
			return Lease{}, fmt.Errorf("%w: leased to %s (%s)", platform.ErrConflict, label, modeLabel)
		}
	}
	variants := slices.Clone(n.config.Shares[id])
	n.mu.Unlock()

	devices, err := snapshot(ctx, n.adapter)
	if err != nil {
		return Lease{}, err
	}
	d := devices[id]
	if d.ID == "" || !slices.Contains(variants, describe(d)) {
		return Lease{}, fmt.Errorf("%w: device is absent or its current USB identity was not shared", platform.ErrNotFound)
	}
	if mode == "usbip" {
		if err := validateManagedUSBPolicy(d, riskAccepted); err != nil {
			return Lease{}, err
		}
	}
	exportedHere := false
	if mode == "usbip" && !d.Exported {
		d, err = n.adapter.Export(ctx, d.BusID)
		exportedHere = err == nil
	} else if mode == "esp" && d.Exported {
		d, err = n.adapter.Unexport(ctx, d.BusID)
	}
	if err != nil {
		return Lease{}, err
	}
	unexportRollback := func() {
		if exportedHere {
			_, _ = n.adapter.Unexport(ctx, d.BusID)
		}
	}
	if d.ID != id || !slices.Contains(variants, describe(d)) {
		unexportRollback()
		return Lease{}, fmt.Errorf("%w: device changed while preparing the lease", platform.ErrConflict)
	}
	if mode == "usbip" {
		if err := validateManagedUSBPolicy(d, riskAccepted); err != nil {
			unexportRollback()
			return Lease{}, err
		}
	}

	n.mu.Lock()
	if _, ok := n.config.Peers[owner]; !ok {
		n.mu.Unlock()
		unexportRollback()
		return Lease{}, failure(http.StatusForbidden, "peer has been revoked")
	}
	if fence, blocked := n.config.Fences[leaseFenceKey(owner, id)]; blocked && time.Now().Before(fence.Until) {
		n.mu.Unlock()
		unexportRollback()
		return Lease{}, withFailureCode("force_release_cooldown", fmt.Errorf("%w: lease was force-released; retry after %s", platform.ErrConflict, fence.Until.Format(time.RFC3339)))
	}
	if n.leases[id] != nil || !slices.Contains(n.config.Shares[id], describe(d)) {
		n.mu.Unlock()
		unexportRollback()
		return Lease{}, fmt.Errorf("%w: device state changed while preparing the lease", platform.ErrConflict)
	}
	if n.generations == nil {
		n.generations = make(map[string]uint64)
	}
	generation := n.generations[id] + 1
	n.generations[id] = generation
	le := &lease{Lease: Lease{ID: randomID(), Device: id, Owner: owner, Mode: mode, BusID: d.BusID,
		DeviceInfo: &d, Online: true, Expires: time.Now().Add(leaseTTL)}, device: d, busy: mode == "esp",
		riskAccepted: riskAccepted}
	le.Generation = generation
	n.leases[id] = le
	n.publishLocked("leased", id)
	leaseCopy := le.Lease
	n.mu.Unlock()
	return leaseCopy, nil
}

func validateManagedUSBPolicy(device platform.Device, riskAccepted bool) error {
	switch {
	case device.SharePolicy == platform.ShareBlock:
		return withFailureCode("policy_blocked", fmt.Errorf("%w: this USB device is outside the whole-device share boundary", platform.ErrInvalid))
	case platform.RequiresRiskConfirmation(device) && !riskAccepted:
		return withFailureCode("risk_required", fmt.Errorf("%w: %s USB device requires explicit risk acceptance", platform.ErrInvalid, device.Risk))
	default:
		return nil
	}
}

func (n *Node) currentLeaseLocked(owner, token string) *lease {
	for _, current := range n.leases {
		if current.ID == token && current.Owner == owner && current.Generation == n.generations[current.Device] {
			return current
		}
	}
	return nil
}

func leaseFenceKey(peer, device string) string { return peer + "/" + device }

func (n *Node) forceRelease(device string) (LeaseFence, error) {
	n.mu.Lock()
	defer n.mu.Unlock()
	le := n.leases[device]
	if le == nil || le.Closing {
		return LeaseFence{}, platform.ErrNotFound
	}
	if le.busy {
		return LeaseFence{}, fmt.Errorf("%w: an ESP operation is running", platform.ErrConflict)
	}
	if n.config.Fences == nil {
		n.config.Fences = make(map[string]LeaseFence)
	}
	if n.generations == nil {
		n.generations = make(map[string]uint64)
	}
	key := leaseFenceKey(le.Owner, le.Device)
	previous, existed := n.config.Fences[key]
	generation := max(n.generations[le.Device], le.Generation) + 1
	fence := LeaseFence{Peer: le.Owner, Device: le.Device, Generation: generation, Until: time.Now().Add(forceReleaseCooldown)}
	n.config.Fences[key] = fence
	if err := n.saveLocked(); err != nil {
		if existed {
			n.config.Fences[key] = previous
		} else {
			delete(n.config.Fences, key)
		}
		return LeaseFence{}, err
	}
	n.generations[le.Device] = generation
	n.closeLeaseLocked(le)
	n.publishLocked("force_released", le.Device)
	return fence, nil
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
	if err == nil && len(ports) == 0 && (le.DeviceInfo == nil || slices.Contains(le.DeviceInfo.Profiles, "serial")) {
		deadline := time.Now().Add(2500 * time.Millisecond)
		for time.Now().Before(deadline) && len(ports) == 0 && err == nil && ctx.Err() == nil {
			timer := time.NewTimer(150 * time.Millisecond)
			select {
			case <-ctx.Done():
				timer.Stop()
			case <-timer.C:
				ports, err = n.adapter.LocalSerialPorts(ctx, le.Device)
			}
		}
	}
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
	n.hostMu.Lock()
	defer n.hostMu.Unlock()
	n.mu.Lock()
	empty := len(n.config.Shares) == 0 && len(n.leases) == 0
	n.mu.Unlock()
	if empty {
		return
	}
	devices, err := snapshot(n.ctx, n.adapter)

	var privateUnexports []string
	type closingTarget struct {
		id    string
		busID string
		gen   uint64
	}
	var closingUnexports []closingTarget
	type exportTarget struct {
		id    string
		busID string
		gen   uint64
		risk  bool
	}
	var pendingExports []exportTarget

	n.mu.Lock()
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
				privateUnexports = append(privateUnexports, d.BusID)
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
		if approved {
			deviceInfo := d
			le.DeviceInfo = &deviceInfo
			if le.Mode == "usbip" && validateManagedUSBPolicy(d, le.riskAccepted) != nil {
				n.closeLeaseLocked(le)
			}
		}
		if le.Closing {
			if approved && d.Exported {
				closingUnexports = append(closingUnexports, closingTarget{id: id, busID: d.BusID, gen: le.Generation})
				continue
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
				pendingExports = append(pendingExports, exportTarget{id: id, busID: d.BusID, gen: le.Generation, risk: le.riskAccepted})
				continue
			}
			le.device, le.BusID, le.Online = d, d.BusID, true
			le.DeviceInfo = &d
			n.publishLocked("exported", id)
		}
		if approved && !le.Closing {
			le.device = d
		}
	}
	n.mu.Unlock()

	for _, busID := range privateUnexports {
		// Restore a private managed share after an unclean agent shutdown.
		_, _ = n.adapter.Unexport(n.ctx, busID)
	}
	var released []closingTarget
	for _, item := range closingUnexports {
		if _, unexportErr := n.adapter.Unexport(n.ctx, item.busID); unexportErr == nil {
			released = append(released, item)
		}
	}
	type exportOutcome struct {
		target exportTarget
		device platform.Device
	}
	var exportedResults []exportOutcome
	for _, item := range pendingExports {
		if exported, exportErr := n.adapter.Export(n.ctx, item.busID); exportErr == nil {
			exportedResults = append(exportedResults, exportOutcome{target: item, device: exported})
		}
	}

	if len(released) == 0 && len(exportedResults) == 0 {
		return
	}
	var rollbackExports []string
	n.mu.Lock()
	for _, item := range released {
		if le := n.leases[item.id]; le != nil && le.Generation == item.gen && le.Closing {
			delete(n.leases, item.id)
			n.publishLocked("released", item.id)
		}
	}
	for _, outcome := range exportedResults {
		item, d := outcome.target, outcome.device
		le := n.leases[item.id]
		if le == nil || le.Generation != item.gen || le.Closing ||
			d.ID != item.id || !slices.Contains(n.config.Shares[item.id], describe(d)) ||
			validateManagedUSBPolicy(d, le.riskAccepted) != nil {
			if le != nil && le.Generation == item.gen {
				n.closeLeaseLocked(le)
			}
			rollbackExports = append(rollbackExports, d.BusID)
			continue
		}
		le.device, le.BusID, le.Online = d, d.BusID, true
		le.DeviceInfo = &d
		n.publishLocked("exported", item.id)
	}
	n.mu.Unlock()
	for _, busID := range rollbackExports {
		_, _ = n.adapter.Unexport(n.ctx, busID)
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
	n.hostMu.Lock()
	defer n.hostMu.Unlock()
	devices, err := snapshot(ctx, n.adapter)
	if err != nil {
		return
	}
	var toUnexport []string
	n.mu.Lock()
	for id, le := range n.leases {
		d := devices[id]
		if !le.busy && d.Exported && slices.Contains(n.config.Shares[id], describe(d)) {
			toUnexport = append(toUnexport, d.BusID)
		}
	}
	n.mu.Unlock()
	for _, busID := range toUnexport {
		_, _ = n.adapter.Unexport(ctx, busID)
	}
}
