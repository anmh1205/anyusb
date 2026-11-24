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
    {"id":"1-4:1.2","configuration":1,"interface":2,"class":"02","kind":"serial","driver":"cdc_acm"}
  ],
  "profiles": ["debug-probe", "serial", "stlink"],
  "share_policy": "allow",
  "reconnect_policy": "automatic"
}
```

Profiles are advisory. They select guidance and an optional explicit probe;
they do not prove that the native application can use the device.

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
