# AnyUSB desktop USB forwarding

**2026-11-23 · AnyUSB 0.1.2 · package source `5d3a0d7`**

## Native operating-system port and adverse networks

Two QEMU guests used separate Debian `6.12.117+deb13-amd64` kernels and stock
`usbip-utils 2.0`. Both agents ran the 0.1.2 Linux amd64 binaries with
`-usb-only` on an isolated Ethernet link. The exporter provided a CDC ACM
gadget through `usbip-host`; the importer used `vhci_hcd` and `cdc_acm`. The
acceptance probe opened `/dev/ttyACM0` independently of AnyUSB and compared
deterministic SHA-256 payloads in both directions.

The complete second run ended with `AnyUSB NETWORK LAB RESULT=0`:

- Stock USB/IP transferred 32,768 bytes each way before the managed tests.
- Managed attach and 65,536-byte bidirectional transfer passed on clean, LAN,
  WAN, mobile, severe and asymmetric profiles. The most adverse symmetric
  profile was 200 ms ± 60 ms, 5% loss and 1 Mbit/s per direction. Initial
  attach-to-tty times in this TCG run ranged from 6.687 to 18.469 seconds.
- Three virtual unplug/replug cycles on 90 ms ± 25 ms, 3% loss and 3 Mbit/s
  restored the same saved connection and verified 4,096 bytes each way in
  12.671–13.500 seconds from replug.
- A three-second 100% packet-loss interval during a 262,144-byte transfer kept
  the original native import and completed after the network returned.
- A 12-second 100% loss interval changed the connection to `lost`, detached the
  stale import, reattached it and verified data after restoration.
- Forcibly deleting the established peer TCP sockets created a fresh native
  import and verified data.
- During a 40-second partition, the remote lease, host export and local native
  import were removed while the stable connection intent remained
  `reconnecting`. Restoring the link acquired a new remote lease, recreated
  `/dev/ttyACM0` and verified data without another connect request.
- Explicit disconnect removed the saved intent, native import, host lease and
  export.

The first 0.1.2 run passed every earlier case and failed only the old 40-second
acceptance assertion, which expected the connection record itself to disappear.
The retained `reconnecting` record was the new intended behavior; the revised
assertion then passed the complete second run and required automatic byte-level
recovery.

## Service restart, Windows mapping and UI

- An integration test stopped a connected importing agent, started a new agent
  from the same state directory, and observed automatic reattach with the same
  stable connection ID. A separate offline-peer test verified persistence and
  that explicit disconnect removed the stored intent.
- The Windows PnP test models the actual usbip-win2 hierarchy and maps COM ports
  through the exact virtual hub port. Two identical `303a:1001` devices on
  virtual ports 3 and 4 resolved independently to `COM8` and `COM11`.
- `go test ./...`, `go vet ./...`, and `go test -race` for `agent/mesh`, `web`
  and `cli` passed. Go binaries built for Windows and Linux on amd64 and arm64.
- A live packaged Electron run completed share, on-screen pairing, connect,
  delayed `COM7` enumeration and disconnect. It confirmed that no firmware file
  picker or ESP panel is exposed. The inspected screenshot is
  `dist/desktop/previews/anyusb-0.1.2-usb.png`.

## Packages

Both finished packages were extracted and inspected. The Windows payload has
the 0.1.2 Electron app and Go service/CLI, with no Python or esptool payload.
The Ubuntu unit starts `/usr/bin/anyusbd -usb-only`; its payload also has no
Python or esptool runtime. The Windows AnyUSB setup executable remains unsigned;
the setup script separately verifies the hashes and Authenticode signatures of
the upstream driver installers it downloads. The compiled Windows installer
contains the in-place upgrade path: it stops the old service before replacing
binaries, preserves ProgramData state and an existing peer firewall scope, then
updates and restarts the service.

| Package | SHA-256 |
| --- | --- |
| `AnyUSB-0.1.2-Windows-x64.exe` | `6fe6a4fce3384158d7ff5cac3c06f712e612efabb535e1d20fe3266e05bec03d` |
| `anyusb_0.1.2_amd64.deb` | `c36115b8c7f403ee1a25116db3764cae8f8d9a12a1cf6e5c1b323cb9eb5a9727` |

## Remaining physical acceptance

The previously recorded physical ESP32-S3 run proved that a Linux importer
opened its USB/IP `/dev/ttyACM0` and received the project boot log; see
[Physical ESP32-S3 run](ivts-firmware-tailnet.md).

**UNVERIFIED on 0.1.2:** native Windows COM enumeration with the physical
ESP32-S3, selecting that COM port and flashing from VS Code/Arduino IDE, and
privileged installation on clean Windows and Ubuntu systems. At the end of this
run, the named Ubuntu hardware peer was offline on Tailscale and timed out on
SSH. The local Windows host exposed only legacy `COM1` and had no AnyUSB or
USB/IP driver services installed, so neither a physical Windows claim nor a
live installed-service upgrade claim is made.
