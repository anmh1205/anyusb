# AnyUSB desktop USB forwarding

**2026-11-23 · AnyUSB 0.1.1 · source `3f95181`**

## Native operating system port

Two QEMU guests used separate Debian `6.12.117+deb13-amd64` kernels and stock
`usbip-utils 2.0`. The isolated Ethernet network ran on the Windows host's
loopback. Both agents started with `-usb-only`. The exporter provided a CDC
ACM gadget using the Linux USB gadget driver. The importer used `vhci_hcd` and
`cdc_acm`. The agents used their normal managed USB/IP attach and detach API.

- The client kernel created `/dev/ttyACM0` in 4.109 s after the connect
  request. The independent `stty -F /dev/ttyACM0 115200` command opened it.
  The serial use test then verified 65,536 bytes in each direction.
- After a virtual unplug and replug, AnyUSB restored the same managed lease
  while the link was configured for 90 ms ± 25 ms, 3% loss, and 3 Mbit/s per
  direction. The serial test reopened the port and verified 4,096 bytes in
  each direction. Recovery took 13.156 s from replug to verified data.
- Disconnect removed the imported USB device and `/dev/ttyACM0` on the client.

## App and packages

- The Windows Electron app was exercised with real agent pairing and USB/IP
  tunnel code and a fixture adapter that reported `COM7` after a delay. The
  connection card updated when the port appeared and cleared it on disconnect.
  The firmware file input and firmware picker IPC were absent.
- `go test ./...`, `go vet ./...`, and the targeted Go race test for
  `agent/mesh`, `web`, and `cli` passed. Go binaries were built for Windows and
  Linux on amd64 and arm64. Desktop installers target x64.
- Both installers were extracted and validated. Windows packaging omitted
  the embedded Python runtime, and Ubuntu packaging omitted the ESP wheels
  and the Python venv dependency. The Ubuntu systemd unit uses `-usb-only`.

The previous test on real ESP32-S3 hardware verified that a Linux client
opened its USB/IP `/dev/ttyACM0` and saw the project's boot log.
[Physical ESP32-S3 run](ivts-firmware-tailnet.md)

**UNVERIFIED** on this release. Windows COM enumeration with the physical
ESP32-S3, the IDE's selection and flash flow on that Windows COM port, and
privileged installation on a clean Windows or Ubuntu system.

| Package | SHA-256 |
| --- | --- |
| `AnyUSB-0.1.1-Windows-x64.exe` | `f15f9aa6788579494110a85c73921fb2d5dd629b14695240f7d5210ed68122aa` |
| `anyusb_0.1.1_amd64.deb` | `f4903153ccef9d338353500f1d56cf061061070b1c7d8e085eb9500a411fd419` |
