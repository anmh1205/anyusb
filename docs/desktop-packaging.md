# AnyUSB desktop packages

## Install

- On Windows x64, run `AnyUSB-0.1.2-Windows-x64.exe` and open
  **AnyUSB** from Start. Initial setup downloads usbipd-win 5.3.0 and usbip-win2
  0.9.8.1 if they are missing. It validates the release digests and Windows
  signatures before running the driver installers. Complete a reboot if setup
  requests one. The AnyUSB setup executable itself is unsigned.
- On Ubuntu 24.04 LTS amd64, open `anyusb_0.1.2_amd64.deb` in the graphical
  package installer and open **AnyUSB** from the applications menu. The package
  installs the launcher and two systemd services. Ubuntu resolves missing
  system packages from its repositories.
  Stock USB/IP tools and modules must match the running kernel. The **Chẩn đoán**
  view shows the installed backend status.

Both copies use the same controls and save approved peers. LAN discovery uses
mDNS and a TLS identity probe. Across networks, both machines must already be
signed in to Tailscale and their policy must allow TCP 47443. The Windows setup
scopes its peer firewall rule to the local subnet and Tailscale address ranges.
On a Linux host with a firewall already enabled, its policy must allow the
desired peers to reach TCP 47443. The managed data path uses that same TLS port.

## Build

Use Go 1.24 or newer, Python 3.11 or newer, and NSIS 3.11 on a Windows x64 build
machine. Build the Go binaries with `scripts/build.ps1` first.

| Input | Contents |
| --- | --- |
| `--windows-electron`, `--linux-electron` | Electron 45.4.1 win32-x64 and linux-x64 release ZIPs |
| `--checksums` | `SHASUMS256.txt` from the same Electron release |
| `--makensis` | Path to `makensis.exe` |

Fetch the compiler and runtime archives from their release pages.

[Electron 45.4.1](https://github.com/electron/electron/releases/tag/v45.4.1) ·
[NSIS](https://nsis.sourceforge.io/Download)

Run `python scripts/package-desktop.py --help` for the complete argument list.
The default output is `dist/desktop/`. The script verifies both Electron ZIP
digests against the supplied release checksum file and prints a SHA-256 digest
for each finished package.
