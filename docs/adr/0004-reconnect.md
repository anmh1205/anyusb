# 0004. Lease-aware reconnect

**Accepted and implemented.**

Reconcile hotplug notifications against live backend state. Use a bounded
inventory poll as a fallback for missed events. On disappearance, mark the
export unavailable and preserve a valid lease during a configurable grace
window. On arrival, resolve the identity and current busid, reconcile the actual
bind, re-export if needed, then publish the new endpoint and generation.

On the client, detach only a stale import owned by the current AnyUSB lease.
Attach using the current busid after validating the lease and export generation.
Deduplicate retries, make them cancellable, and resolve the resulting COM/tty
from OS inventory before reporting the device usable. Release, expiry, and mode
changes invalidate stale work. Keep mode changes, passthrough reattach, and
competing local serial opens blocked while a flash job owns the device.

Persist a stable local connection intent containing only peer and device IDs.
Do not persist the lease ID, loopback relay port, remote busid, or client virtual
port. Recreate those transient values after an agent restart. A failed initial
attach remains a visible, cancellable connection and retries with a bounded
backoff; explicit disconnect removes the intent before it tears down the native
import so a crash cannot unexpectedly restore a connection the operator removed.

`usbip-win2` documents background retries after a lost connection and a
30-second first retry in that case. Use an agent-owned retry schedule for the
few-second goal. Test whether `attach --once` covers disconnect after a
successful attach, and verify cancellation using the exact release. Avoid
stashed/persistent imports for managed devices. An explicit detach and stopping
an in-progress driver retry are separate operations. [Upstream retry behavior](https://github.com/vadimgrn/usbip-win2/wiki/How-automatic-reattachment-works), [v0.9.8.1 CLI](https://github.com/vadimgrn/usbip-win2/blob/v.0.9.8.1/userspace/usbip/usbip.cpp)

A new OS serial handle may be required after the old port disappears, even
when the COM/tty name returns unchanged. Report port rediscovery and successful
open separately from recovery of an existing IDE monitor. Measure host device
arrival to client port open using synchronized clocks or a single observer.
Also measure reset/replug stimulus to port open and record every failed cycle.
The latency budget and backend-specific binding behavior remain hardware
acceptance checks in the [M0 lab procedure](../test-report/m0-lab.md#reset-replug-and-recovery).
