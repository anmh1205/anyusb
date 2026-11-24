# General USB software and virtual-composite verification

**Run date:** 2026-11-24 (Asia/Saigon)

**Source under test:** `93a757065132b487be438442fb6300c0f0a2a146` plus the
report-only documentation update that follows this run

**Result:** software and isolated Linux virtual-composite scope passed;
physical ST-Link, J-Link, CMSIS-DAP and Windows USB/IP hardware rows remain
`UNVERIFIED`.

No driver was installed, no service or firewall rule was changed, and no host
network policy was changed during this work.

## Environment

```text
Microsoft Windows 11 Pro 10.0.26100 build 26100, 64-bit
go version go1.27.4 windows/amd64
git version 2.51.0.windows.1
Python 3.11.9
Node v24.12.0
QEMU 11.1.0 (v11.1.0-12130-ge470268ff4)
Virtual guest kernel 6.12.117+deb13-amd64
usbip (usbip-utils 2.0)
tc utility, iproute2-6.17.0
```

Read-only host inventory found no present external USB device and only the
ACPI `COM1` serial port. `usbipd.exe`, `usbip.exe`,
`STM32_Programmer_CLI.exe`, `JLink.exe` and `pyocd.exe` were not on `PATH`.
Services `usbipd`, `VBoxUSBMon`, `usbip2_ude` and `usbip2_filter` were not
registered. Therefore no physical Windows USB/IP or debug-probe claim is made.

## Software verification

Commands:

```powershell
go test ./...
go vet ./...
$env:CGO_ENABLED = '1'; go test -race ./...
./scripts/build.ps1 -Go C:\Users\Admin\.codex\tools\go1.27.4\go\bin\go.exe
node --check web/static/app.js
python -m py_compile scripts/lab/net-initramfs.py scripts/lab/net-matrix.py
Get-Content docs/api/openapi.json -Raw | ConvertFrom-Json -Depth 100
git diff --check
```

All passed. `go test -json ./...` counted 42 passing tests across seven tested
packages (plus two packages with no test files): ESP 3, mesh 10, platform 18,
Linux adapter 3, Windows adapter 5, CLI 1 and web 2. Race tests passed for all
of the same tested packages. The OpenAPI document parsed with 12 paths and nine
schemas.

Cross-build output:

| Target | CLI bytes | Agent bytes |
| --- | ---: | ---: |
| linux-amd64 | 11,070,392 | 14,360,518 |
| linux-arm64 | 10,217,352 | 13,301,465 |
| windows-amd64 | 11,401,728 | 14,787,072 |
| windows-arm64 | 10,441,728 | 13,562,880 |

Fixtures cover serial-only, composite, HID, storage, hub, known debug probes,
unknown devices, partial binding, duplicate bindings, two identical Windows
imports on different usbip-win2 virtual ports, risk acceptance persistence,
hub rejection and an explicit missing optional profile tool. Windows PnP tests
executed against Windows PowerShell but did not install or exercise usbip-win2.

## Isolated composite USB/IP lab

The runner built a disposable configfs gadget on one QEMU guest using
`dummy_hcd`, then imported it into a second guest through stock USB/IP and the
managed tunnel. QEMU UDP sockets and serial control sockets bound only to
Windows loopback. The gadget was one whole device (`1209:a11b`, serial
`anyusb-composite-lab`) with CDC ACM control/data, vendor-page HID and an 8 MiB
mass-storage function backed by a disposable file.

Command:

```powershell
python scripts/lab/net-matrix.py `
  --qemu C:\Users\Admin\AppData\Local\Temp\anyusb-kernel-lab\qemu\qemu-system-x86_64.exe `
  --image C:\Users\Admin\AppData\Local\Temp\anyusb-kernel-lab\network\composite-head-image `
  --logs C:\Users\Admin\AppData\Local\Temp\anyusb-kernel-lab\network\composite-head-final3
```

Observed imported functions and bindings were:

```text
CDC control  class 02  cdc_acm    -> /dev/ttyACM0
CDC data     class 0a  cdc_acm    -> driver-bound companion interface
vendor HID   class 03  usbhid     -> /dev/hidraw0
mass storage class 08  usb-storage -> /dev/sda
generic USB identity              -> /dev/bus/usb/002/...
```

The aggregate stayed `enumerating` while `usb-storage` was bound but `/dev/sda`
did not yet exist, then became `ready`. The raw stock case wrote random data,
synced it and read back 256 KiB with SHA-256
`225d93297c5d8205b43750b4c24efd45f5997c0c108782851e51fd4c6f9dfcf0`.
That digest remained unchanged after every managed attach and every recovery
case. The controlled HID used a vendor usage page and could not inject keyboard
or pointer input.

| Case | Time to all bindings / recovery | Workload result |
| --- | ---: | --- |
| Stock USB/IP, clean | — | storage hash; 32 KiB serial each way in 80 ms |
| Managed clean | 8.922 s | storage hash; 64 KiB in 299 ms |
| Managed LAN, 4 ms ± 1 ms | 11.281 s | storage hash; 64 KiB in 376 ms |
| Managed WAN, 40 ms ± 12 ms, 1% configured loss | 20.313 s | storage hash; 64 KiB in 1.082 s |
| Managed mobile, 90 ms ± 25 ms, 3% configured loss | 39.656 s | storage hash; 64 KiB in 5.090 s |
| Managed severe, 200 ms ± 60 ms, 5% configured loss, 1 Mbit/s | 77.250 s | storage hash; 64 KiB in 9.935 s |
| Managed asymmetric | 32.343 s | storage hash; 64 KiB in 4.003 s |
| Mobile virtual replug 1 / 2 / 3 | 43.047 / 42.687 / 41.172 s | serial verified and storage hash retained |
| Three-second blackhole during transfer | original import retained | 256 KiB completed 16.672 s after restoration; storage hash retained |
| Twelve-second blackhole | 40.000 s | fresh import, serial verified, storage hash retained |
| Forced TCP reset | 32.563 s | fresh import, serial verified, storage hash retained |
| Forty-second blackhole | lease/export/import expired, then recovered | same confirmed intent, serial verified, storage hash retained |

The final line was:

```text
AnyUSB NETWORK LAB RESULT=0
```

## Defects found and corrected by the composite run

1. The CDC data companion interface was initially projected into
   `serial_ports` as if its sysfs interface ID were a second tty. The
   compatibility list now includes only ready OS serial bindings.
2. A bound `usb-storage` driver was initially counted as ready before the block
   node existed. Expected node-backed functions now report `enumerating` until
   the node is observed. A generic USB identity marked `enumerated` is neutral
   and does not hold an otherwise ready composite in the pending state.

Both have unit regressions and passed the final full virtual run.

## Evidence matrix after this run

| Scope | Status | Evidence boundary |
| --- | --- | --- |
| Linux -> Linux CDC + vendor HID + disposable storage composite | `VERIFIED` | Stock and managed USB/IP, bindings, integrity, adverse network and recovery |
| Windows exact usbip-win2 port/descendant mapping | `VERIFIED` software fixture | PowerShell and Go fixture only; real driver/hardware remains `UNVERIFIED` |
| Generic non-serial lease and API/CLI/UI path | `VERIFIED` software/virtual | HID/storage bindings do not require COM/tty |
| ST-Link, any direction | `UNVERIFIED` | No probe, target, vendor tool or installed Windows USB/IP backend on this host |
| J-Link, any direction | `UNVERIFIED` | No probe, target, vendor tool or Remote Server comparison |
| CMSIS-DAP, any direction | `UNVERIFIED` | No physical v1/v2 probe or target |
| Physical storage/HID/token/network/audio/video/printer/scanner | `UNVERIFIED` | Policy and procedure exist; no hardware workload run |

M6's J-Link Remote Server provider was not added: there is no concrete failed
J-Link USB/IP row to justify a separate provider, lifecycle and ADR.
