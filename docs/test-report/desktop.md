# AnyUSB desktop verification

**2026-11-23 · AnyUSB 0.1.0 · Windows 11 Pro build 26100 and Ubuntu 24.04.5 LTS**

## Windows and Ubuntu with a physical ESP32-S3

The Windows dashboard discovered the separate Ubuntu machine `anmh1205` over
its existing Tailscale connection. The desktop API identified the peer by its
TLS certificate. In two browser sessions, both sides showed the same pairing
code and the receiving dashboard approved the request. On Ubuntu, the physical
Espressif USB Serial/JTAG device (`303a:1001`) was available at `/dev/ttyACM0`.
Ubuntu ran kernel `6.8.0-147-generic` and the ESP jobs ran with esptool `5.2.0`.

The Ubuntu test agent ran temporarily under the existing dialout account. The
Windows test agent used the real mesh and dashboard code with a fixture USB
adapter. SSH forwarded the Ubuntu administration dashboard to the two-browser
test runner. Discovery, pairing and ESP data went over the AnyUSB peer TLS
connection on Tailscale. The test made no firewall changes and used the existing
serial driver on Ubuntu.

From the dashboard, the operator chose the generated `ivts-firmware`
`flasher_args.json` from commit `271e8c9`. The app found the four referenced files.
The initial **Verify** matched the firmware already on the board. **Nạp** then
wrote all four regions, and an independent **Verify** matched all four again.

| Address | Bytes | Flash write digest | Read-back verification |
| --- | ---: | --- | --- |
| `0x00000` | 20,720 | Matched | Matched |
| `0x08000` | 3,072 | Matched | Matched |
| `0x0f000` | 8,192 | Matched | Matched |
| `0x20000` | 169,808 | Matched | Matched |

**Serial Monitor** opened the running application at 115200 baud and received
`ivts-firmware boot` after the RTS reset. The board was left with the project
firmware. [Previous native Linux USB/IP and firmware run](ivts-firmware-tailnet.md)

## Desktop and package checks

- Two QEMU guests on an isolated Ethernet segment discovered each other as
  `LAN` through multicast DNS and the TLS identity probe. Both ran Debian
  kernel `6.12.117+deb13-amd64` with independently addressed e1000 interfaces.
  The isolated segment had no default route. Adding a multicast route inside
  each guest enabled the scan in both directions.
- Playwright exercised two isolated AnyUSB nodes through sharing, matching
  pairing codes, connect, the visible `COM7`, and disconnect. The fixture
  provided `COM7` while the real agent negotiated the USB/IP tunnel.
- The Windows installer compiled and extracted. The packaged Python had a
  valid Windows signature and started esptool `5.2.0`. The extracted Electron
  app displayed its retry screen when the service was missing. With a test
  launcher supplying the dashboard URL, the Electron file-picker handler read
  the real project manifest and four referenced files. The OS picker selection
  was supplied by the Playwright fixture.
- Windows confirmed `Sysnative` resolves native PowerShell from a 32-bit
  process and that PowerShell uses `C:\Program Files`. The real desktop CLI
  rejected a bootstrap pipe owned by an ordinary user.
- On Ubuntu, `dpkg-deb -I`, `dpkg-deb -x`, shell syntax checks, and `ldd` passed.
  An isolated Python environment installed esptool `5.2.0` offline using only
  the bundled wheels.
- `go test ./...`, `go vet ./...`, and targeted race runs for `agent/mesh`,
  `web`, and `cli` passed. Cross-builds covered Windows and Linux on amd64 and
  arm64. The desktop installers target Windows x64 and Ubuntu amd64.

**UNVERIFIED** means a complete privileged installation or hardware acceptance
run still needs evidence. It applies to both installers' clean-system service
provisioning, Ubuntu's application-menu launch through polkit, and native
Windows COM creation with the physical ESP32-S3.

## Installer checksums

| File | SHA-256 |
| --- | --- |
| `AnyUSB-0.1.0-Windows-x64.exe` | `96ef13b173cf785e73aff05fcfd7af461b6b72eb9e03f6f4adb7187afd56faee` |
| `anyusb_0.1.0_amd64.deb` | `9cb23ff248d9a0272809dddba6bf80352355061dbb96b46a46f7783ad06bd22f` |
