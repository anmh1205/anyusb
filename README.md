# AnyUSB

Share USB devices between Windows and Linux over LAN or Tailscale. AnyUSB
orchestrates native USB/IP backends and runs remote ESP operations on the
computer with the physical board.

## Desktop app

Install AnyUSB on both computers and open it from the Start menu or Ubuntu's
application menu. The x64 Windows installer and Ubuntu 24.04 amd64 package
are generated in `dist/desktop/`.

1. Pick the other computer from **Máy tìm thấy**. Discovery uses the LAN
   and the machines visible to an already signed-in Tailscale client.
2. On the receiving computer, check that the displayed code matches and
   click **Mã trùng, đồng ý**. Pairing is saved on both computers.
3. On the machine with the USB cable, click **Cho phép dùng** on the device.
   On the other machine, click the paired machine and **Kết nối**. The device
   card shows the serial port when the operating system exposes one.
4. To use the host's ESP tools, disconnect that USB import and open **ESP**
   on the device card. Choose the generated `flasher_args.json`, then use
   **Nạp**, **Verify**, or **Mở Serial Monitor**.

The Windows installer prepares the service and signed upstream USB drivers;
Windows may request a reboot. The Ubuntu package installs the app launcher,
agent and backend services. Desktop access is reserved for interactive local
users. Administrators can also use the CLI and script paths below.

To build the installers, first build the Go binaries below, then run
`python scripts/package-desktop.py --help` for the required Electron archives,
release checksum list, NSIS compiler, Python embedded runtime, and esptool
wheel sets. See [desktop packaging](docs/desktop-packaging.md).

## Build

Use Go 1.24 or newer.

```powershell
.\scripts\build.ps1
```

The script builds `anyusbd` and `anyusb` for Linux and Windows on amd64 and arm64
under `dist/`. On Linux, build the current architecture with:

```sh
mkdir -p dist
go build -trimpath -o dist/anyusbd ./agent/cmd/anyusbd
go build -trimpath -o dist/anyusb ./cli
```

## Install on Linux

```sh
sudo sh scripts/install-linux.sh dist/linux-amd64
sudo anyusb doctor
```

Ubuntu needs USB/IP utilities and modules matching its running kernel. Debian
uses the `usbip` package. The installer offers a separate loopback-restricted
stock `usbipd` service, installs esptool 5.2.0 in a venv, and enables the agent.
Add `--client-only` to skip the host module and daemon.

## Install on Windows

Run an elevated PowerShell.

```powershell
.\scripts\install-windows.ps1 -BuildDirectory .\dist\windows-amd64
& "$env:ProgramFiles\AnyUSB\anyusb.exe" doctor
```

The script previews the driver packages and prompts for consent before launching
the signed upstream installers. Reboot if requested. For Remote ESP, install
`scripts/esptool-requirements.txt` into a machine-wide Python environment under
Program Files and pass its interpreter path with `-Python`. Use `-PeerSources`
to enable an inbound Windows Firewall rule scoped to the intended peer addresses.

## Pair and share

Use an elevated terminal on Windows or `sudo` on Linux. On the host, list
physical devices, choose one, and generate a one-time code.

```text
anyusb list
anyusb share --managed DEVICE
anyusb pair invite
```

On the client, pair by LAN or Tailscale address or run `anyusb discover` on the
LAN. Use the peer ID from `anyusb peers` and the device ID from the remote list.

```text
anyusb pair HOST CODE
anyusb peers
anyusb list --peer PEER
anyusb connect --peer PEER DEVICE
anyusb list --connections
anyusb disconnect --lease LEASE
```

Keep TCP 47443 reachable only by the intended networks. Stock USB/IP on TCP
3240 runs separately. Managed exports require that TCP 3240 be accessible to
the agent on loopback and blocked from peer machines. Allow TCP 3240 on a
designated test network for stock-tool interoperability. The original raw
`list --remote`, `share`, `connect HOST BUSID` and `disconnect VHCI_PORT`
commands remain available for that workflow.

Foreground options include `-socket`, `-data`, `-usbip`, `-usbipd`, `-python`,
`-peer-listen`, `-advertise`, and `-backend`. `-advertise` accepts a MagicDNS
name or host address for pairing in both directions. Local control uses a
root-only Unix socket or administrator-only named pipe. Run `anyusb ui` and
open the authorized loopback URL in a browser.

## Remote ESP

Use a managed shared ESP device. Disconnect its USB/IP lease before starting
an ESP operation. The host opens the serial port and runs the pinned esptool.

```text
anyusb chip-id PEER DEVICE
anyusb flash PEER DEVICE --idf build/flasher_args.json
anyusb flash PEER DEVICE 0x0=bootloader.bin 0x10000=app.bin
anyusb verify PEER DEVICE --idf build/flasher_args.json
anyusb reset PEER DEVICE
anyusb monitor PEER DEVICE --baud 115200
```

Use `--port` for a board with multiple host serial interfaces. The web UI
supports firmware selection, flash progress, serial input, baud, DTR and RTS.
The host reserves the device until a running ESP operation finishes.

See the [architecture](docs/architecture.md), [peer API](docs/api/openapi.json),
[software verification](docs/test-report/phase1-software.md), and
[hardware matrix](docs/test-report/m0.md). Phase 1 hardware acceptance
requires the Linux and Windows backends plus both physical ESP32-S3 interfaces.
