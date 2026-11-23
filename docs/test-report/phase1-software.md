# Phase 1 software verification

**2026-11-20 · Windows 11 Pro 24H2 x64 build 26100.8037.**
Build host used Go `1.27.4`. Reproduce the software checks from the repository
root with:

```text
go test -v -count=1 -timeout 60s ./...
go vet ./...
go test -race -count=3 -timeout 90s ./...
```

On Windows, set `CGO_ENABLED=1` and use a compatible GCC for the race run.
The release build uses `CGO_ENABLED=0`.

| Check | Observed result |
| --- | --- |
| Cross-build | `anyusbd` and `anyusb` built for Windows amd64/arm64 and Linux amd64/arm64 using `scripts/build.ps1`. |
| Unit and integration tests | Nine tests passed. `go vet ./...` passed. Race detector passed three consecutive runs. |
| Pairing and passthrough | Loopback TLS tests used three agents, rejected unpaired and second-client requests, consumed the code once, and transferred an import header through the managed tunnel. The test adapter changed busid during a lease and the client reattached to the new one. An ESP lease blocked both managed and local raw export. |
| ESP | Tests validated uploads and rejected overlaps, unsafe paths and conflicting retries. A Python fixture verified host-local subprocess invocation, stdout/stderr, lease release, and replay handling. A second fixture verified serial data, baud, DTR and RTS in both directions through a WebSocket. |
| Platform and UI | Checked Linux and usbip-win2 port fixtures, Windows named pipe CLI round-trip, and loopback UI authorization and origin policy. PowerShell, shell and JavaScript syntax checks passed. The OpenAPI JSON parsed with 12 paths. |

The first pairing test caught the responder's CPace share and key being
assigned in the wrong order. The corrected exchange passed the repeated
integration and race tests.

The [virtual USB lab](virtual-usb-lab.md) records follow-up kernel USB/IP,
serial, and Windows PnP checks from 2026-11-22.
The [physical ESP32-S3 run](physical-esp32-tailnet.md) records a direct
Tailscale pairing and USB/IP import on 2026-11-23, along with the serial
failures observed on that board.

## Hardware acceptance

**UNVERIFIED.** The Windows inventory exposed a legacy COM1 only. The service
manager reported no installed usbipd-win or usbip-win2 UDE service. The Ubuntu
VM was present, but authenticated access and guest USB inventory were not
established. The installers were inspected and their syntax checked.

The four device/direction combinations, physical ESP reset and replug, ten
consecutive flash cycles, real COM/tty recovery time, native OpenOCD, IDE
monitoring, host-local Remote Flash with a real board, and Tailscale transport
remain open. Follow the [hardware matrix](m0.md#hardware-matrix) and
[lab procedure](m0-lab.md). The test adapter and Python fixtures exercise
application behavior without certifying kernel drivers or board compatibility.
