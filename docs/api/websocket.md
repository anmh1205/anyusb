# AnyUSB peer WebSockets and pairing

See the [OpenAPI schema](openapi.json) for HTTPS requests and response models.
All transports use TLS 1.3. Send the paired client certificate on peer
connections and compare the server certificate SHA-256 fingerprint with the
saved pin. Pairing is the only exception to the saved-pin requirement.

## Pairing

The operator creates an eight-digit invitation on the host. It expires after
two minutes or five start attempts. The client first gets the certificate from
`GET /v1/pair/info`. Use the lowercase hex DER fingerprints of the client
and server certificates as the respective CPace identities, with
`anyusb-peer-pair-v1` as associated data. The implementation uses
[filippo.io/cpace](https://filippo.io/cpace).

Send the CPace client share and the caller name/address to
`POST /v1/pair/start`. The host replies with its share and a session ID. Verify
the host proof as HMAC-SHA256 over the UTF-8 string
`anyusb-peer-pair-v1 NUL server NUL SESSION_ID` using the CPace key. Send the
equivalent proof for role `client` to `POST /v1/pair/finish`. The host
consumes the invitation and both peers save the certificate pins.

## Events and jobs

Events use text frames with `epoch`, `seq`, `type`, and optional `device`.
After opening or detecting a gap, reload the shared-device snapshot over HTTPS.

Job logs use text frames with the current `job` and optional `logs` array.
Every log has a monotonically increasing `number`, a `stream`, and `text`.
Use the last number as `after` when reconnecting. Retained logs are bounded.
The final job status is `succeeded` or `failed` with an exit code.

## Serial monitor

Text frames are JSON objects. Host messages carry base64 `data` or an
`error`. Client messages may contain base64 `data`, numeric `baud`, and
Boolean `dtr` and `rts`. Close the socket to release the monitor lease.

## Managed USB/IP

Use subprotocol `anyusb-usbip-v1` with binary frames. Concatenate payloads
as a byte stream. The first 40 bytes must be a USB/IP 1.1.1 import
request for the lease's current busid. The server forwards the stream to its
stock USB/IP backend. The native USB/IP driver handles USB transfers.
