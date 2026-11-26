# ESP32-C28P built-in Go ESP engine and 4-digit pairing hardware check — 2026-11-26

Status: Linux host (`anmh1205`) deployment, systemd Tailscale boot-order fix,
4-digit CPace pairing over Tailscale, 3-attempt invite exhaustion, built-in
pure-Go Remote ESP (`chip-id`, `verify`, `flash`, `monitor`, `reset`), **and**
Windows Managed USB/IP COM forwarding (`COM11`) with direct Windows pure-Go
serial operations (`serial_windows.go`: `chip-id`, `verify`, `flash` 14.84 MB at
`460800` baud, and direct `COM11` boot capture) **PASS** against physical
ESP32-S3 C28P hardware with no Python, `esptool`, or `pyserial` on `anmh1205`.

## Scope and machines

- USB host `anmh1205` (`100.94.74.49`): `Ubuntu 24.04.5 LTS`, kernel
  `6.8.0-151-generic`, `usbip (usbip-utils 2.0)`, Tailscale `1.104.7`.
- Deployed Linux host binaries (`CGO_ENABLED=0`, `dist/linux-amd64`):
  - `/usr/local/bin/anyusbd` SHA-256 `650198957985f7f724a3dda9c8e5f6f76ce5635839e420835ff7e912d61a1695`
  - `/usr/local/bin/anyusb` SHA-256 `104a25a9fd021a0f8b28c0a6f766d2c4e18930b3eb9b31038c69e5018fcfb6de`
- Client `server` (`100.83.47.41`): Windows (`server\admin`, non-elevated shell)
  running an isolated `dist\windows-amd64\anyusbd.exe -dev-scenario basic`
  instance (`\\.\pipe\anyusb-hw-test` and temporary state directory) because the
  installed LocalSystem service pipe (`\\.\pipe\anyusb-control`) requires
  Administrator privileges.
- Physical target on `anmh1205`: USB busid `1-2.2`, device ID
  `ce2e092f9aa912a2662fc3d72b835827`, `303a:1001` (`Espressif USB JTAG/serial
  debug unit`), `/dev/ttyACM0`, MAC `cc:68:c7:8c:c4:24`, ESP32-S3 rev `v0.2`,
  8 MB PSRAM, 16 MB SPI flash (`manufacturer=5e device=4018`).
- Firmware project: `E:\anmh1205\aqy-bike-exam-sim` (commit `76940f9`),
  manifest `firmware/build-c28p-final/flasher_args.json` (`dio`, `16MB`, `80m`,
  `chip: esp32s3`):
  - `0x0`: `bootloader/bootloader.bin` (21,120 bytes)
  - `0x8000`: `partition_table/partition-table.bin` (3,072 bytes)
  - `0x10000`: `aqy-bike-exam-sim.bin` (1,251,424 bytes)
  - `0x310000`: `audio.bin` (13,565,952 bytes)

## Host boot-order diagnosis and service setup

After rebooting onto kernel `6.8.0-151-generic`, the previous `anyusbd.service`
had entered `failed` state (`status=1/FAILURE`) because systemd started it
before `tailscaled.service` configured `100.94.74.49`:

```text
anyusbd[1752]: listen tcp 100.94.74.49:47443: bind: cannot assign requested address
```

Updated `scripts/systemd/anyusbd.service` (and `/etc/systemd/system/anyusbd.service`
on `anmh1205`) to wait for `network-online.target tailscaled.service` with
`Wants=network-online.target`, `StartLimitIntervalSec=0`, and `RestartSec=2`,
and removed `-usb-only` from `/etc/default/anyusbd` so the built-in Go ESP
engine is active alongside USB/IP. Existing `/var/lib/anyusb/identity.pem` and
the paired `SERVER` (`100.83.47.41:47443`) trust entry were preserved.

`anyusb doctor` on `anmh1205` reported:

```text
esp                      OK        built-in Go ESP bootloader and serial engine
```

Reachability from Windows to `anmh1205` measured:

```text
100.94.74.49:47443=True
100.94.74.49:3240=False
```

## 4-digit pairing and lockout over Tailscale

1. On `anmh1205`, `anyusb -json pair invite` returned 4-digit invite `2267`.
2. From Windows, three wrong attempts (`2268`, `2269`, `2270`) failed with
   `anyusb: pairing code or remote identity did not match`. A fourth attempt
   using the real code `2267` failed with `anyusb: no active pairing invitation`,
   verifying 3-attempt exhaustion.
3. On `anmh1205`, a second 4-digit invite (`2125`) paired cleanly over Tailscale
   mTLS as peer `14d4b53de35c8ab169008ba4b839ae069b671881e31d701243e67000fc849432`.

## Built-in Go ESP operations on physical ESP32-C28P

| Operation | Elapsed | Outcome and key observed output |
| --- | ---: | --- |
| `anyusb chip-id` | 1.68 s | Exit 0. `Chip: ESP32-S3`, `Transport: USB-Serial/JTAG`, `MAC: cc:68:c7:8c:c4:24`, `Flash: manufacturer=5e device=4018 size=16MB`, `Hard resetting via RTS pin...` |
| `anyusb verify` (`--idf .../firmware/build-c28p-final/flasher_args.json`) | 27.68 s | Exit 0. Verified all 4 regions via `SPI_FLASH_MD5` (`0x0`, `0x8000`, `0x10000`, `0x310000` including 13.5 MB `audio.bin`), `Verification successful (digest matched).` |
| `anyusb flash --baud 460800` (`--idf .../firmware/build-c28p-final/flasher_args.json`) | 169.74 s | Exit 0. Changed baud to `460800`, wrote `bootloader.bin` (`0.54s`), `partition-table.bin` (`0.08s`), `aqy-bike-exam-sim.bin` (`15.49s`), `audio.bin` (`125.94s`), verified all 4 digests (`Verification successful (digest matched).`), `Hard resetting via RTS pin...` |
| `anyusb monitor --baud 115200` (6 s capture) | 6.00 s | Captured `3,215` bytes of live boot logs from `/dev/ttyACM0`: `App version: 76940f9-dirty`, `ESP-IDF: v6.1`, `Found 8MB PSRAM device`, `C28P HAL ready (ILI9341 + FT6336 + ES8311)`, `audio ready: snd_ver=3 voice=yes`, `ready as AQY-C426 (CC:68:C7:8C:C4:26)`, `LVGL UI ready`, `AQY boot: hw=0x02 caps=0x07 restored=no`, `main_task: Returned from app_main()` |
| `anyusb reset` | 1.68 s | Exit 0. Detected `Flash: manufacturer=5e device=4018 size=16MB`, `Hard resetting via RTS pin...` |

After testing, the temporary test peer (`win-c28p-test`) was revoked from
`anmh1205` and the temporary Windows state directory was deleted.

## Windows Managed USB/IP COM11 forwarding checks (`usbip-win2`)

Using the running Windows `anyusbd` LocalSystem service via the authorized
Desktop UI loopback API (`\\.\pipe\anyusb-desktop` -> `http://127.0.0.1:47440`),
`anmh1205` busid `1-2.2` (`303a:1001`) was connected via Managed USB/IP over
Tailscale (`confirm_risk=true`), attaching virtual hub port 1 (`usbip2_ude`)
with `COM11` (`usbser`, ready), JTAG (`WINUSB`, ready), and `usbccgp`.

1. **Stock `esptool v5.2.0` baseline on `COM11`:**
   `esptool.exe --chip esp32s3 --port COM11 --after no-reset chip-id` looped in
   `Connecting......................................` and exited with status `2`
   after `147.00 s` (`A fatal error occurred: Failed to connect to ESP32-S3:
   Serial data stream stopped: Possible serial noise or corruption.`),
   matching the previously documented `usbip-win2` 8-reply SYNC issue.
2. **Direct Windows `COM11` serial capture:**
   Opening `COM11` (`115200` 8N1) and pulsing RTS captured `1,055` bytes (before
   flash) and `3,927` bytes (after flash) of boot logs directly from the board
   (`ESP-ROM:esp32s3-20210327`, `App version: 76940f9-dirty`, `ESP-IDF: v6.1`,
   `C28P HAL ready (ILI9341 + FT6336 + ES8311)`, `audio ready: snd_ver=3
   voice=yes`, `ready as AQY-C426 (CC:68:C7:8C:C4:26)`, `LVGL UI ready`).
3. **Mid-test Tailscale NAT route flip observation:**
   During an earlier `verify` attempt (`connection b02b3a58...`, `21.22 s`),
   Tailscale NAT switched between `1.52.88.143:48979` and `21.176.18.130:10839`,
   leaving the Windows COM call with `The semaphore timeout period has expired`
   and the connection in `reconnecting` (`The specified network name is no longer
   available`) until explicitly disconnected (`DELETE /v1/mesh/connections/...`).
   Reconnecting (`e03160f8fc856e5da7cf3bdff7e8491d`) restored `COM11` ready in
   `12 s`.
4. **Reset sequence fix and pure-Go Windows engine (`serial_windows.go`) on `COM11`:**
   Initial `default-reset` (`["classic", "usb-jtag"]`) spent `~45 s` failing
   `classic` before `usb-jtag` succeeded (`chip-id` `47.08 s`, `verify` `69.46 s`).
   Using `usb-reset` dropped `verify` total time to `26.68 s` (`0.48 s` connect).
   Updating `resetSequence` in `agent/esp/flasher.go` to try `usb-jtag` before
   `classic` on Windows (when target is not `esp32`/`esp8266`) then yielded:

| Operation directly via Windows `COM11` | Elapsed | Outcome and key observed output |
| --- | ---: | --- |
| `chip-id` (`default-reset`) | 0.90 s | Exit 0. Connected in `0.47 s`, `Chip: ESP32-S3`, `MAC: cc:68:c7:8c:c4:24`, `Flash: manufacturer=5e device=4018 size=16MB`, RTS hard reset |
| `verify` (`-before usb-reset`, 4 regions) | 26.68 s | Exit 0. Verified all 4 digests including `audio.bin` (13.5 MB) |
| `flash --baud 460800` (`default-reset`, 4 regions) | 339.38 s | Exit 0. `bootloader.bin` (`0.73 s`), `partition-table.bin` (`0.12 s`), `aqy-bike-exam-sim.bin` (`32.19 s`), `audio.bin` 13.5 MB (`279.45 s`), all 4 digests verified (`Verification successful (digest matched).`), RTS hard reset |

Throughout the Windows `COM11` `chip-id`, `flash`, and boot-capture sequence on
connection `e03160f8fc856e5da7cf3bdff7e8491d`, `state` remained `attached`,
`phase=ready`, `reconnect_count=0`, with live tunnel counters progressing from
`5,475 / 6,815` bytes to `16,653,463 / 1,622,295` bytes (`COM11` active).

## Remaining UNVERIFIED items

- Direct serial COM operations using the built-in Go engine hosted directly on
  a **local physical USB-to-UART Windows host port** (without `usbip-win2`, plus
  external CP210x/CH340 adapters) remain `UNVERIFIED`.
- Non-ESP32-S3 targets (`esp8266`, `esp32`, `esp32s2`, `esp32c2`, `esp32c3`,
  `esp32c6`, `esp32h2`, `esp32p4`, `esp32c61`, `esp32c5`) remain `UNVERIFIED` on
  physical hardware with the pure-Go engine.
- Replacing the elevated LocalSystem Windows service binaries with the newly
  built `dist/windows-amd64` binaries remains `UNVERIFIED` in this non-elevated
  session.
