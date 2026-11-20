# 0005. Gate managed USB/IP imports by the peer lease

The stock backends accept native USB/IP connections on TCP 3240. An AnyUSB
client opens a separate ephemeral loopback listener and asks its native virtual
controller to attach there. The client bridges this socket through a binary
WebSocket over the authenticated peer API. The host validates the peer lease
and the 40-byte USB/IP import request against the current busid. It then forwards
the byte stream to the stock server on loopback.

The relay handles the import header and transports bytes. Linux and Windows
continue to implement the USB protocol and driver operations. Each lease admits
one tunnel and the host closes it on revocation, device loss, or expiry. Raw
clients can still connect directly to the stock server in a trusted lab
deployment. To enforce managed leases against raw clients, block direct TCP
3240 access from other machines.

The host tracks the live incarnation and exports the returning approved
device. The client renews every second, checks its native virtual port, and
retries on the current busid while the lease is valid. Hardware recovery time
remains an acceptance measurement.
