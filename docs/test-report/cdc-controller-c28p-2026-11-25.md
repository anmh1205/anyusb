# CDC controller and ESP32-C28P hardware check — 2026-11-25

Status: AnyUSB managed USB/IP install, pairing, share, Windows attach, COM
enumeration, project flash, read-back verification, boot monitoring, service
restart recovery and live relay counters **PASS** on physical hardware. Stock
esptool sync over usbip-win2 needs the compatibility note below. The AQY
firmware boots but reports a DMA allocation failure and an LVGL task watchdog.

## Scope and machines

- AnyUSB working tree based on `565d5d4188eea863c27c2a1daa83ff154d9440c6`
  plus the uncommitted implementation under test.
- AQY firmware commit `76940f974514474b35db10434318093873371358` and
  `firmware/build-c28p-final` artifacts.
- Client `SERVER`: Windows 11 Pro 10.0.26100, amd64, Tailscale `100.83.47.41`,
  Go 1.27.4.
- USB host `anmh1205`: Ubuntu 24.04.5 LTS, kernel
  `6.8.0-147-generic`, amd64, Tailscale `100.94.74.49` 1.104.7,
  usbip-utils 2.0 and Python 3.12.3.
- Physical device: Espressif USB Serial/JTAG `303a:1001`, serial
  `CC:68:C7:8C:C4:24`, ESP32-S3 revision 0.2, 8 MB PSRAM and 16 MB flash.

The board is the ESP32-S3-based C28P target used by the AQY project. It first
enumerated at Ubuntu busid `1-1.4.4`; after a later physical re-enumeration it
appeared at `1-2`. AnyUSB correctly treated the old physical-path identity as
absent; the new identity was explicitly shared before reconnecting.

## Installation and isolation

The user approved installing both sides and the required drivers.

Windows installation:

- AnyUSB Desktop 0.1.2, then the current working-tree `anyusbd.exe` and
  `anyusb.exe`.
- usbipd-win 5.3.0 and USBip 0.9.8.1.
- `usbip2_ude` and `usbip2_filter` both running.
- AnyUSB Windows service automatic and running as LocalSystem.
- Installed final hashes: `anyusbd.exe`
  `3F9DE89CAE88DEB938C69EA3719897874B30A0982D65269487EC0C9E183D4FE0` and
  `anyusb.exe`
  `858DBEDCD9A8FE1DB5E9DCCAACC76BDC2B927DF4088EC1666852E41AB2CA1DF5`.
- AnyUSB peer HTTPS firewall rule enabled on TCP 47443 for LocalSubnet and
  Tailscale ranges; mDNS UDP 5353 limited to LocalSubnet.
- The upstream `usbipd` inbound firewall rule is disabled.

Ubuntu installation:

- `/usr/local/bin/anyusbd` and `/usr/local/bin/anyusb` with systemd units
  `anyusbd.service` and `anyusb-usbipd.service`, both enabled and active.
- Installed final hashes: `anyusbd`
  `42D9DE9D5849F46C91D13F6D2F53B195F2FC3FC2E4B27754CB7F0139CE62DAFB` and
  `anyusb`
  `3ABF730B0AFB60924A9014A5C3C5D11B01932B9C7BA68F4FB1ADA661945E40F9`.
- `usbip_host` and `vhci_hcd` loaded at boot.
- `anyusbd` listens only on the Tailscale address at TCP 47443.
- Stock `usbipd` listens on TCP 3240 inside a systemd network sandbox that
  allows loopback only.

Reachability from Windows was checked directly:

```text
100.94.74.49:47443=True
100.94.74.49:3240=False
```

The VC++ runtime installer reported that Windows could be restarted because
runtime DLLs were in use. The USB/IP drivers loaded and all hardware tests
below passed without rebooting, so no reboot was performed.

## Pair, share and native attach

The agents discovered each other over Tailscale and were paired using a
one-time code. Pairing persisted across Windows service restarts. On Ubuntu,
the physical descriptor was classified as:

```text
02/02 serial (cdc_acm)
0a/02 serial (cdc_acm)
ff/ff vendor
profiles: debug-probe, serial, vendor
share policy: allow
reconnect policy: automatic
```

After managed sharing, Windows imported the peer tunnel through loopback:

```text
Port 01: device in use at Full Speed(12Mbps)
  unknown vendor : unknown product (303a:1001)
  -> usbip://127.0.0.1:<ephemeral>/1-2
```

PnP produced these usable bindings on virtual port 1:

| Function | Driver | Result |
| --- | --- | --- |
| CDC interface `MI_00` | `usbser` | `USB Serial Device (COM11)`, ready |
| vendor/JTAG interface `MI_02` | `WINUSB` | ready |
| composite parent | `usbccgp` | enumerated |

The aggregate connection reached `state=attached`, `phase=ready`,
`readiness=ready`, `serial_ports=[COM11]`. This physical run exposed and fixed
two Windows issues: PnP properties are now read in one batch, and current
usbip-win2 controllers are recognized by service `usbip2_ude` even when their
instance ID is `ROOT\USB\...` rather than `ROOT\USBIP_WIN2\...`.

## Project flash through AnyUSB

A host-local SSH flash was accidentally started before the user correction.
It completed safely, but it is explicitly excluded from AnyUSB evidence. The
acceptance run below was performed from Windows against `COM11`, while the
board remained physically connected to Ubuntu and the only USB data path was
the managed AnyUSB tunnel.

Stock esptool 5.6.0 sent SYNC over COM11 and received valid ROM replies, proving
bidirectional bulk/control traffic. usbip-win2 delivered two or three of the
eight duplicate SYNC replies; stock esptool waited for all eight and then
reported `Serial data stream stopped`. For this run only, a local Python
wrapper changed `ESPLoader.sync` to accept the first valid SYNC reply. No
esptool package, AnyUSB binary or firmware artifact was modified by the
wrapper. All later commands and flash protocol checks remained standard.

The C28P image was written at 460800 baud:

| Offset | Artifact | Bytes | SHA-256 |
| --- | --- | ---: | --- |
| `0x0` | `bootloader/bootloader.bin` | 21,120 | `0636C73097FF63A3B84A74EBBC22231619676E550E6EC2D80E1D181A0974A201` |
| `0x8000` | `partition_table/partition-table.bin` | 3,072 | `5032E6404CBE2D9B41625F73F07C96E6E903961A35511FB5470BF0B490E5A0D0` |
| `0x10000` | `aqy-bike-exam-sim.bin` | 1,251,440 | `6F2EC83F7FC97CD9C3FE552871DE892B6623AF608CE5CCA45962D468414CF8D8` |
| `0x310000` | `audio.bin` | 13,565,952 | `FD92560E6708296BF76682E6AD8C36CCC463661CD41A4E86CF47A782214C56A9` |

esptool reported `Hash of data verified` after every write. A separate
`verify-flash` pass then compared all four complete flash regions and returned
`Verification successful (digest matched)` for each. Both commands exited 0.

## Boot and runtime observation

The app was hard-reset from Windows through COM11. The same AnyUSB connection
captured the ROM, bootloader and app logs, including:

```text
rst:0x15 (USB_UART_CHIP_RESET),boot:0xa (SPI_FAST_FLASH_BOOT)
SPI Flash Size : 16MB
Found 8MB PSRAM device
C28P HAL ready (ILI9341 + FT6336 + ES8311)
audio ready: snd_ver=3 voice=yes
ready as AQY-C426 (CC:68:C7:8C:C4:26)
LVGL UI ready
AQY boot: hw=0x02 caps=0x07 restored=no
```

The hardware run also found an AQY firmware problem after boot:

```text
spicommon_dma_setup_priv_buffer: Failed to allocate priv TX buffer
panel_io_spi_tx_color: spi transmit (queue) color failed
task_wdt: Task watchdog got triggered
taskLVGL
```

This is an application/runtime defect, not an AnyUSB attach failure: COM11
remained usable and the managed connection remained ready while the messages
were captured.

## Recovery and live diagnostics

Windows `anyusbd` was replaced and restarted during the hardware fixes. The
saved pairing and current connection intent survived. The Ubuntu `anyusbd` and
stock `usbipd` processes were then restarted together for a real transport-loss
test. The client recorded `reconnect_count=1`, reacquired the lease and returned
COM11 to `phase=ready` in 10.63 seconds. A four-second post-recovery monitor read
returned 415 bytes and exited 0.

Tunnel statistics were checked while the relay was still open. A COM write
followed by seven seconds of monitor traffic changed the counters from:

```text
upload=1343 download=2325
```

to:

```text
upload=2702 download=4374
last_progress=2026-11-25T23:51:52.1975828+07:00
```

This hardware observation exposed and fixed an implementation gap: counters
previously appeared only after relay teardown. They now update per bounded
relay write while the session remains open.

## Verification commands

From `E:\anmh1205\anyusb`, using the verified Go 1.27.4 toolchain:

```powershell
go test ./...
go vet ./...
go test -race ./...
node --check web/static/app.js
Get-Content docs/api/openapi.json -Raw | ConvertFrom-Json
git diff --check
.\scripts\build.ps1 -Go C:\Users\Admin\.cache\tooling\go1.27.4\go\bin\go.exe
```

All commands passed. The build produced `anyusbd` and `anyusb` for Windows and
Linux on amd64 and arm64.

## Remaining limitations

- Stock esptool's eight-response SYNC expectation over usbip-win2 remains a
  compatibility issue; the successful flash used the documented one-response
  wrapper.
- A physical cable unplug/replug recovery time was not measured. Service
  restart recovery and USB reset/monitor continuity were measured.
- JTAG/OpenOCD, CP210x, CDC-NCM hardware and privileged clean-install/upgrade
  on a second Windows image remain unverified.
- The AQY DMA allocation and LVGL watchdog failures require investigation in
  `E:\anmh1205\aqy-bike-exam-sim`; they do not invalidate the AnyUSB transport
  evidence above.

## AnyUSB follow-up — 2026-11-26

The scope after the operator's clarification was AnyUSB. The AQY source tree
was restored to its starting state. A test app image had already been written
and independently digest-verified through `COM11` before that clarification.
The follow-up USB acceptance used attachment and serial-open checks without
another firmware write.

| Capability | Evidence from the follow-up |
| --- | --- |
| CDC and network policy | Linux CDC Union/IAD and NCM network-binding fixtures pass. The Windows compatible-ID fixture classifies driverless NCM as network and requires confirmation. Fresh Windows adapter code identified the real C28P as CDC `02/02` with `usbser` on virtual port 1 and `COM11`. Physical CDC-NCM is **UNVERIFIED**. |
| Retry and readiness | Managed native-attach integration test sees `usbip_attach / backend_unavailable` and 1-second, then 2-second delays using a fake clock. The compiled Windows CLI rejected unconfirmed mock NCM and `connect --confirm-risk --wait 20s` subsequently returned a ready mock network binding. |
| Relay and reconnect statistics | Relay unit coverage checks active upload/download progress. The mock `network-loss` run reached ready, dropped the live tunnel, and reached ready on a new virtual port with one recorded reconnect and a termination reason. |
| Force release and revocation | Two compiled Windows mock agents were paired through the normal CLI. `force-release --device mock-port-1` removed the import and returned a persisted fence. The same peer entered `force_release_cooldown`; revocation then produced non-retryable `peer_revoked`. |
| Diagnostics | `anyusb diagnostics` on the compiled mock client contained live counters and timestamped risk/ready events. The CLI check found no raw remote host, mock serial, serial ports, certificate, or private-key material. |
| State and platform runner | Unversioned migration and corrupt-state preservation tests passed. Native runner tests covered typed failures, output limits, and sensitive-output suppression. |

On the installed Windows service, the authorized local desktop API was used to
`DELETE /v1/mesh/connections/{id}` for the C28P, then
`POST /v1/mesh/connections` for the same peer and physical device. With 400 ms
polling, the interval from starting the disconnect until a successful COM open
was **15.72 s**. Final output was:

```text
COM11 opened successfully
Attached: attached / ready, USB/IP port 1, elapsed seconds 15.72, transmitted 1343 / 2325
```

This measures explicit disconnect/reattach. The separate 10.63-second result
above measures recovery from a remote service restart. Physical replug remains
**UNVERIFIED**. An OpenOCD attempt on this same Windows import reported
`libusb_open() failed with LIBUSB_ERROR_NOT_FOUND`, so JTAG application
compatibility remains **UNVERIFIED**. Stock esptool 5.2.0 also reproduced the
duplicate-SYNC issue described above. The new AnyUSB binaries were exercised in
isolated processes and the live Windows adapter probe. Replacing the installed
services with these binaries requires an elevated Windows session and Ubuntu
`sudo` authentication.

Final verification passed `go test ./...` and `go vet ./...` on Windows Go
1.27.4 and on `anmh1205` Ubuntu Go 1.24.0. Windows also passed
`go test -race ./...`. `scripts/build.ps1` produced both commands for Windows
and Linux on amd64 and arm64. `node --check web/static/app.js`, OpenAPI JSON
parsing, and `git diff --check` passed.
