# Virtual USB and native Windows checks

**2026-11-22 · AnyUSB Phase 1**

The [two-guest network lab](virtual-network-lab.md) follows this same-kernel
baseline with measured latency, packet loss, and interruptions.

## Environment

The host ran Windows 11 Pro 24H2 x64 (build `26100.8037`) and Go `1.27.4`.
Portable QEMU `11.1.0` booted Debian's x86-64 kernel `6.12.117+deb13-amd64`
with a temporary initramfs and Alpine 3.23 userland. The `usbip` and `usbipd`
executables both reported `usbip-utils 2.0`. QEMU used TCG, two vCPUs, 1 GiB of
RAM and `-nic none`. Both USB/IP endpoints used guest loopback. Each AnyUSB
agent had its own control socket, state, and TLS identity.

Configfs exposed a synthetic CDC ACM gadget (`1209:a11b`) through `dummy_hcd`.
The guest used `usbip_host` for export, `vhci_hcd` for import, and `cdc_acm` for
`/dev/ttyACM0`. The gadget application opened `/dev/ttyGS0`. The VM booted
without access to host USB devices, and no Windows drivers or network rules
were changed. [Linux USB/IP](https://github.com/torvalds/linux/blob/master/tools/usb/usbip/README), [Linux gadget serial](https://docs.kernel.org/usb/gadget_serial.html)

## Procedure

The guest booted with the following QEMU arguments on the Windows host:

```powershell
qemu-system-x86_64.exe -machine q35,accel=tcg -cpu max -m 1024 -smp 2 `
  -nic none -display none -monitor none -serial stdio -no-reboot `
  -kernel vmlinuz -initrd lab-initramfs.gz `
  -append 'console=ttyS0 rdinit=/init loglevel=4 panic=1'
```

The guest initramfs mounted `devtmpfs`, `proc`, `sysfs`, and `configfs`, brought
up loopback and loaded the kernel modules:

```sh
modprobe dummy_hcd
modprobe libcomposite
modprobe usb_f_acm
modprobe cdc_acm
modprobe usbip_host
modprobe vhci_hcd
```

The fixture created a gadget directory under `/sys/kernel/config/usb_gadget`,
wrote the VID/PID and a fixed serial number, linked `functions/acm.usb0` into
`configs/c.1`, and wrote the `dummy_udc` name from `/sys/class/udc` to `UDC`.
It started `usbipd -4 -t 3240` in the background.

The test process used the production Linux amd64 binaries. The host agent
listened on `127.0.0.1:47443` for peers and `127.0.0.1:47440` for the UI. The
client and second peer listened on ports `47444` and `47445`. Set `DEVICE`,
`PORT`, `CODE`, `PEER`, and `LEASE` from the respective command outputs:

```sh
anyusb -socket /run/lab-host/ctl list
anyusb -socket /run/lab-host/ctl share "$DEVICE"
anyusb -socket /run/lab-client/ctl list --remote 127.0.0.1
anyusb -socket /run/lab-client/ctl connect 127.0.0.1 1-1
anyusb -socket /run/lab-client/ctl list --imports
anyusb -socket /run/lab-client/ctl disconnect "$PORT"
anyusb -socket /run/lab-host/ctl unshare "$DEVICE"
anyusb -socket /run/lab-host/ctl share --managed "$DEVICE"
anyusb -socket /run/lab-host/ctl pair invite
anyusb -socket /run/lab-client/ctl pair 127.0.0.1:47443 "$CODE"
anyusb -socket /run/lab-client/ctl connect --peer "$PEER" "$DEVICE"
anyusb -socket /run/lab-client/ctl list --connections
anyusb -socket /run/lab-client/ctl disconnect --lease "$LEASE"
```

While the managed lease was active, the second paired client requested the
same device. For each reconnect cycle, the fixture cleared the gadget's `UDC`
attribute, waited for the lease to leave `attached`, and rebound `dummy_udc`.
It polled `list --connections` for the original lease and verified exact
binary payloads, including NUL, CR, LF, and `0xff`, across the two TTYs in
both directions. Recovery time starts just before rebinding and ends after
the byte comparison. The fixture also checked stable device ID and a changed
USB incarnation after each cycle.

After disconnecting the managed import, the fixture waited for the gadget
to return to the host's `cdc_acm` driver, then ran the Remote ESP monitor at
115200 baud with DTR enabled:

```sh
python3 -m esptool version
anyusb -socket /run/lab-client/ctl monitor "$PEER" "$DEVICE" --baud 115200 --dtr
```

The fixture compared binary data across CLI stdin and stdout and `/dev/ttyGS0`
and verified lease release after interrupting the monitor.

A separate run invoked `anyusb -socket /run/lab-client/ctl chip-id "$PEER"
"$DEVICE" --baud 115200` against the CDC gadget without an ESP ROM bootloader.
It required real esptool output, a terminal failure result, and a released
lease. The monitor then reopened the same TTY and exchanged bytes both ways.

## Results

| Check | Observed result |
| --- | --- |
| Stock USB/IP | Device listed, attached through `usbipd`, opened as `/dev/ttyACM0`, transferred binary data in both directions, and detached. |
| Managed USB/IP | Paired TLS import transferred binary data in both directions. An already paired second peer was refused while the first client held the lease. |
| Automatic reconnect | 10 of 10 virtual replugs recovered serial data. Range **3,442–5,480 ms**, median **4,664 ms** (rounded). Device ID and busid stayed stable and the USB incarnation changed every time. |
| Remote ESP monitor | Guest esptool reported `5.2.0`. The CLI sent and received binary data through the paired WebSocket, pySerial and the kernel CDC TTY. The agent released the lease on exit. |
| Remote ESP job failure | Host-local esptool opened `/dev/ttyACM0`. The CLI streamed `Connecting` and `No serial data received`, then returned a failed job result. The agent released the lease and the next monitor session reopened the port. |
| Loopback UI | Served its assets, rejected an unauthenticated API request, and returned the CDC device when authorized. |
| Windows PnP | Production PnP code enumerated the machine's local `COM1`. |
| Builds and tests | Both binaries built for Linux and Windows on amd64 and arm64 with `scripts/build.ps1`. Eleven tests, `go vet ./...`, and `go test -race -count=1 -timeout 90s ./...` passed on Windows amd64. |

Both guests powered down after returning `AnyUSB LAB RESULT=0`.
[USB/IP console and per-cycle timings](virtual-usb-console.txt), [Remote ESP job console](virtual-esp-console.txt)

## Defects reproduced

- Linux attached the device, then `usbip port` intermittently reported
  `open vhci_driver`. AnyUSB returned a failure immediately. It now retries
  VHCI enumeration for up to three seconds after a successful attach and
  reports the probe error if confirmation still fails. The regression test
  forces the first enumeration to fail and the next one to succeed.
- With one requested Windows PnP ID, Windows PowerShell 5.1 serialized an
  `InstanceID` object where the Go decoder expected a string. AnyUSB now
  iterates the decoded values and emits string IDs. The regression test uses
  Windows PowerShell 5.1 with controlled PnP input for null, zero, one and two
  IDs. A separate call using the native PnP provider decoded the host's actual
  `COM1` and correctly reported it as local.

## Verification limits

On Windows, the extracted `usbipd-win 5.3.0` executable reported its version.
Its `state` command failed because the installation registry key was absent.
The usbip-win2 UDE driver and ESP32-S3 hardware were also absent.

Native USB/IP with Windows in either role, physical ESP32-S3 flash and reset,
CP210x and native JTAG, IDE and OpenOCD access, and transport over LAN or
Tailscale remain **UNVERIFIED** under the Phase 1 hardware criteria. See the
[hardware matrix](m0.md#hardware-matrix).
