# Physical ESP32-S3 over Tailscale

**2026-11-23 · AnyUSB Phase 1 · build `292c197`**

The Ubuntu USB host `anmh1205` ran Ubuntu 24.04.5 LTS, kernel
`6.8.0-147-generic`, and `usbip-utils 2.0`. The separate client PC ran
Windows with a QEMU Linux guest using kernel `6.12.117+deb13-amd64` and
`usbip-utils 2.0`. The ESP32-S3 was physically connected to the Ubuntu
host as a native Espressif USB Serial/JTAG device (`303a:1001`) with
`cdc_acm` and `/dev/ttyACM0`. Tailscale ping from the client PC completed
in 6 ms on a direct path in the initial sample.

The test used the Ubuntu kernel's existing `usbip_host` module. Its stock
USB/IP listener ran inside a temporary network namespace. The host firewall
and Tailscale settings were unchanged. esptool `5.2.0` ran in a disposable
Python environment on the USB host. Commands probed chip and flash identity
without writing firmware. [Espressif esptool commands](https://docs.espressif.com/projects/esptool/en/latest/esp32s3/esptool/basic-commands.html)

## Direct USB baseline

The board was observed at USB paths `1-2.2.2.1` and `1-2.2.2.2` during the
session. Before AnyUSB exported it, the Ubuntu kernel repeatedly logged USB
disconnect and enumeration on `1-2.2.2.2`. A representative sequence of
disconnect times was `08:46:02`, `08:46:10`, `08:46:19`, `08:46:27`,
`08:46:35`, and `08:46:44` local time. `cdc_acm` recreated `/dev/ttyACM0`
approximately one second after each disconnect.

Direct host-local `esptool --port /dev/ttyACM0 --baud 115200 read-mac`
attempts identified the chip as `ESP32-S3`. The first attempt with
`--after hard-reset` failed with a serial `Input/output error`. Further
attempts with `--before default-reset`, `usb-reset`, and `no-reset` identified
the chip but stopped before the read completed. esptool reported
`The chip stopped responding`. The cause of the repeated USB removals was
not established. After detaching and unsharing, a final host-local
`--chip esp32s3 --before usb-reset --after hard-reset --no-stub read-mac` at
the next enumeration again reported `Serial data stream stopped` and exited
with status 2.

## Results across the two physical machines

| Case | Observed result |
| --- | --- |
| AnyUSB control plane | Pairing and an authenticated peer API call passed over direct Tailscale between the client PC and the physical Ubuntu host. The temporary relay published the peer TLS endpoint only. |
| Managed USB/IP | The ESP32-S3 attached and the Linux client enumerated `/dev/ttyACM0` using its VHCI controller. Guest-local esptool opened that TTY. The `flash-id` probe timed out in `Connecting`. |
| Remote ESP job | `anyusb chip-id` ran esptool `5.2.0` on the USB host and streamed its output to the client. esptool identified `ESP32-S3`, then reported `Serial data stream stopped`. AnyUSB returned the failed job status and released the lease. |
| Stock USB/IP | Remote device list and attach passed through the SSH forwarding path over Tailscale. The client detached and the host unshared the device. |

[Hardware network test console](physical-esp32-tailnet-console.txt)

The run establishes physical USB/IP attach and authenticated Tailscale
transport. Stable serial IO, flash identity, flash writes and verification,
OpenOCD JTAG, CP210x, and the native Windows USB/IP directions remain
**UNVERIFIED**. The board's firmware was kept intact.

Cleanup closed both temporary listeners and removed the test-loaded
`usbip_host` module. The host again showed the board on `/dev/ttyACM0`.
