# Local USB model and managed connection API

The local API remains v1 and adds fields without removing the serial-compatible
view. It is available only through the protected local socket/pipe and the
loopback desktop proxy. Peer endpoints remain documented in
[openapi.json](openapi.json).

## Device inventory

`GET /v1/devices` and managed shared-device responses include top-level USB
identity plus:

```json
{
  "id": "stable-port-id",
  "bus_id": "1-4",
  "vendor_id": "0483",
  "product_id": "374b",
  "serial": "0025...",
  "usb_version": "2.00",
  "speed": "480",
  "functions": [
    {"id":"1-4:1.0","configuration":1,"interface":0,"class":"ff","kind":"vendor","driver":"usbfs"},
    {"id":"1-4:1.2","configuration":1,"interface":2,"class":"02","subclass":"02","kind":"serial","driver":"cdc_acm"}
  ],
  "profiles": ["debug-probe", "serial", "stlink"],
  "share_policy": "allow",
  "reconnect_policy": "automatic"
}
```

Profiles are advisory. They select guidance and an optional explicit probe;
they do not prove that the native application can use the device.

CDC classification is control-model aware. `02/02` is CDC-ACM (`serial`),
while `02/06`, `02/0c`, `02/0d` and `02/0e` are ECM, EEM, NCM and MBIM
(`network`). A related `0a` data interface inherits the recognized control
kind. Linux uses CDC Union/IAD descriptors when available and reports the
association as `cdc_control_interface`. Without descriptor membership, only
adjacent control and data interfaces are paired. Windows recognizes CDC control
subclasses from PnP compatible IDs even before a driver binds. Unmatched `0a`
remains `cdc-data`.
Policy is then applied once to the complete physical device, so a composite
NCM device requires network risk acceptance and a composite storage/NCM device
keeps the higher-priority storage policy.

## Imported-device bindings

`GET /v1/imports` and `GET /v1/mesh/connections` expose native USB/IP state,
USB functions observed on the client, OS-local bindings and aggregate
readiness:

```json
{
  "port": 3,
  "state": "attached",
  "readiness": "degraded",
  "functions": [
    {"id":"USB\\VID_0483&PID_374B&MI_00\\...","kind":"debug","driver":"WinUSB"},
    {"id":"USB\\VID_0483&PID_374B&MI_02\\...","kind":"serial","driver":"usbser"}
  ],
  "local_bindings": [
    {"function":"USB\\VID_0483&PID_374B&MI_00\\...","kind":"debug","id":"USB\\VID_0483&PID_374B&MI_00\\...","status":"driver_missing"},
    {"function":"USB\\VID_0483&PID_374B&MI_02\\...","kind":"serial","id":"COM8","driver":"usbser","status":"ready"}
  ],
  "serial_ports": ["COM8"]
}
```

`serial_ports` is retained and derived from serial bindings. An empty list is
valid for a debugger, HID device, storage device or an unknown USB device.
Readiness distinguishes transport attachment from driver readiness; it is not
a claim that the user's application passed.
Network bindings report the Linux interface name or Windows connection alias
and driver. Windows keeps the binding enumerating until its network interface
appears. AnyUSB does not configure routes or DHCP.

## CLI readiness waits

Managed automation can wait for either layer explicitly:

```text
anyusb connect --peer PEER DEVICE --wait 30s
anyusb wait --connection CONNECTION --for attached --timeout 30s
```

`ready` is the default target and requires the connection state to be
`attached` with aggregate import readiness `ready`. The weaker `attached`
target requires a current native USB/IP import but does not claim that a driver
or application is usable. A timeout reports the last connection state,
readiness and retry error without deleting the persistent connection intent.

Managed connections also expose additive controller diagnostics:

```json
{
  "state": "reconnecting",
  "phase": "backoff",
  "attempt": 4,
  "next_retry_at": "2026-11-25T10:00:08Z",
  "last_failure": {
    "code": "dial_failed",
    "operation": "lease",
    "retryable": true,
    "message": "connect: connection refused",
    "at": "2026-11-25T10:00:00Z"
  },
  "stats": {
    "upload_bytes": 2048,
    "download_bytes": 4096,
    "reconnect_count": 2,
    "disconnect_reason": "peer_failure"
  }
}
```

Retry delays are exponential (1, 2, 4, 8, 16, 32, then at most 60 seconds)
with ±20% jitter. Policy and identity failures use typed non-retryable codes;
logic never parses the human-readable message. Native attach failures follow
the same controller retry schedule and identify `usbip_attach` as the failed
operation. A successful renewal with an active import clears the retry history.
The relay publishes progress while open and limits a pending write to 15
seconds. An idle stream has no such timeout.

The private `state.json` store has an explicit schema version. Unversioned
state migrates to version 1. Malformed JSON is copied to a unique
`state.json.corrupt-*` file and startup fails closed instead of silently
creating an empty identity/share/connection configuration.

## Recovery and diagnostics

An operator on the USB host can invalidate an active device lease through the
desktop **Buộc nhả** action or:

```text
anyusb force-release --device DEVICE
```

The force release advances a persisted generation fence. A lease from an older
generation can no longer renew, and the same peer has a 60-second cooldown;
other trusted peers may acquire the device after backend reconciliation.

`GET /v1/diagnostics`, `anyusb diagnostics`, and the desktop diagnostic export
return build/runtime metadata, doctor checks, the bounded native-command
history, managed connection state and timestamped controller events. A retry
event carries the failure code, operation and attempt. The bundle omits raw
device serials, OS device paths, connection error messages, keys and certificates.
Command arguments and environment variables are omitted. Inventory and probe
commands omit stdout and stderr, while version output is capped.

`anyusbd -dev-scenario SCENARIO` selects an in-process, development-only
USB/IP adapter. The supported scenarios are `basic`, `cdc-ncm`, `composite`,
`driver-missing`, `device-busy`, `duplicate`, and `network-loss`. Give each
process a separate state directory and local socket or pipe. Both peer endpoints
listen on loopback. A mock import passes through enumeration before showing
its bindings. Stop the mock host to exercise offline recovery, force-release
an attached device to test lease invalidation, or revoke the peer to test a
terminal authorization failure. Mock runs provide controller test evidence.

## Risk confirmation and profile probes

Create a managed connection with:

```json
{"peer":"peer-id","device":"stable-port-id","confirm_risk":true}
```

`confirm_risk` is required for profiles whose `share_policy` is `confirm`.
The confirmation is stored with that stable connection intent so recovery is
limited to the explicitly accepted connection. Hubs use `block` and cannot be
managed shares.

After a matching imported device reaches `attached`, an operator can explicitly
run a read-only native-tool probe:

```text
POST /v1/mesh/connections/{connection}/probe
{"profile":"stlink"}
```

Allowed profiles are `stlink`, `jlink` and `cmsis-dap`. The result records the
tool path, version boundary, command, duration, imported serial match, bounded
output and error. Probes never run automatically and never halt or write a
target.
