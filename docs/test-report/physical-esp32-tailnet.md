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

The first run established physical USB/IP attach and authenticated Tailscale
transport. It did not complete the serial or flash identity probes. The board's
firmware was kept intact.

Cleanup closed both temporary listeners and removed the test-loaded
`usbip_host` module. The host again showed the board on `/dev/ttyACM0`.

## Same-day retry on USB path `1-2.2`

Ubuntu reported the same Espressif USB VID/PID at `1-2.2`. The journal
still showed repeated removals at roughly eight-second intervals before the
test. On a fresh enumeration (`devnum 59`), esptool `5.2.0` completed both
host-local probes in sequence. The first used `--chip esp32s3 --before
usb-reset --after no-reset --no-stub read-mac`. The second used `--chip
esp32s3 --before no-reset --after hard-reset flash-id`. The ROM identified
`ESP32-S3 (QFN56)`, revision `v0.2`, and the flash probe returned manufacturer
ID `ef`, device ID `4018`, and capacity **16 MB**. Both commands exited 0.

- **Remote ESP over direct Tailscale.** `anyusb chip-id` returned the chip MAC.
  `anyusb reset` returned the same 16 MB flash identification and performed
  the RTS hard reset. The host reported a new USB device number between the
  two commands.
- **Managed USB/IP over direct Tailscale.** The Linux client imported the
  board, then ran the same two-command esptool sequence against its own
  `/dev/ttyACM0`. Both reads passed and the flash identifiers matched the
  host-local results.
- **Stock USB/IP through SSH over Tailscale.** The first retry attached the
  board, then the TTY disappeared before esptool could open it. In a separate
  three-attempt run, two attaches timed out waiting for port confirmation and
  the third TTY vanished before open. Stock serial read remains unverified.

[Retry console](physical-esp32-tailnet-retry.txt)

This retry covered identification and reset. A separate
[ivts-firmware run](ivts-firmware-tailnet.md) verified project flashing,
read-back verification, and boot. Long-run USB stability, OpenOCD JTAG,
CP210x, and the native Windows USB/IP directions remain **UNVERIFIED**.
