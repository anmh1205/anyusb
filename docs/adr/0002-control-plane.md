# 0002. HTTPS and WebSocket control plane

**Proposed at M0.**

Use versioned HTTPS/JSON for control operations and firmware uploads, and
WebSockets for event delivery, job logs, and bidirectional serial monitoring.
Use a dedicated configurable peer listener. Keep the local UI listener on
loopback and independently authorized. Publish an OpenAPI document for REST
operations and a versioned WebSocket message schema with examples. This serves
Go, browser and future Kotlin clients without requiring a browser RPC proxy.

The host holds a long-lived key pair. A short, one-time code authorizes the
Ristretto255 CPace exchange implemented by `filippo.io/cpace`. Bind
both identity keys, roles, and the pairing session to authenticated confirmation
over the resulting key before either side persists a peer. Pairing requires
explicit local initiation, expiry, and attempt limits. Consume the session on
success or exhaustion. Subsequent HTTPS and WSS sessions use mutual TLS and
accept only pinned peer identities. Revocation removes trust and blocks new
operations. mDNS and manually entered addresses supply endpoint hints only.

Desktop pairing adds an approval request alongside the CPace invitation flow.
The requester commits to a random value with the two TLS fingerprints. The
receiver generates its random value before the requester reveals its value.
The apps display the same four-digit comparison code derived from both
values and the pinned identities. The receiver approves only after comparing
the displays. The requester retrieves the result over the same TLS identity.
Requests expire after two minutes and never persist as trusted peers before
approval. The Tailscale client supplies candidate addresses when it is
already signed in. AnyUSB confirms the peer API and TLS fingerprint before
showing a discovery result.
[CPace Go package](https://filippo.io/cpace)

Send device and lease snapshots with a host epoch and revision. Number events
within each epoch. On a gap or epoch change, reload the snapshot before acting.
Use a stable request ID for retried state mutations and flash submissions. An
existing request ID returns the original result or job reference and rejects a
different payload. Associate streamed logs and terminal status with the job ID.

REST and WebSocket keep local browser delivery and tooling straightforward.
gRPC would provide generated service and stream types, with extra browser
integration for bidirectional monitoring. The TLS and pairing design applies
only to control traffic. Raw USB/IP access follows the network boundary in the
[architecture](../architecture.md#tcp-3240-access-by-backend).
