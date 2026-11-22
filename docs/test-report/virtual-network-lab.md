# USB/IP across two Linux guests with network faults

**2026-11-22 · AnyUSB Phase 1**

The lab connects two QEMU `11.1.0` guests using an isolated Ethernet segment.
Each guest boots its own Debian `6.12.117+deb13-amd64` kernel and Alpine 3.23
userland. The exporter uses configfs and `dummy_hcd` to provide a CDC ACM
device (`1209:a11b`) backed by `/dev/ttyGS0`. The importer has its own VHCI
controller and loads `cdc_acm` on `/dev/ttyACM0`. The guests communicate at
`10.67.0.1` and `10.67.0.2` through e1000 NICs. QEMU encapsulates Ethernet in
UDP on Windows loopback. The serial control connections also bind to Windows
loopback and remain available while the guest data link is blocked. The lab
has no bridge to a host NIC or external route. [QEMU network emulation](https://www.qemu.org/docs/master/system/devices/net.html)

Linux `tc netem` shapes egress independently at each endpoint. Twelve ICMP
echo attempts sample each profile. The transfer checks compare deterministic
binary payloads byte for byte after a round trip through the kernel TTYs.
Recovery timing comes from one observer and ends after verified bytes, so
it includes reopening the client TTY. [Linux netem manual](https://man7.org/linux/man-pages/man8/tc-netem.8.html)

| Profile | Added delay per direction | Random loss per direction | Rate per direction |
| --- | ---: | ---: | ---: |
| Clean | 0 ms | 0% | Unlimited |
| LAN | 4 ms ± 1 ms | 0% | Unlimited |
| WAN | 40 ms ± 12 ms | 1% | 10 Mbit/s |
| Mobile | 90 ms ± 25 ms | 3% | 3 Mbit/s |
| Severe | 200 ms ± 60 ms | 5% | 1 Mbit/s |
| Asymmetric, host to client | 35 ms ± 10 ms | 1% | 10 Mbit/s |
| Asymmetric, client to host | 120 ms ± 45 ms | 4% | 768 kbit/s |
| Blackhole | — | 100% | — |

## Reproduction

Use a QEMU x86-64 system binary, Python 3.10+, the Debian amd64 kernel package,
and the Alpine v3.23 x86-64 APKs listed in `PACKAGES` in
[`net-initramfs.py`](../../scripts/lab/net-initramfs.py). Use verified distro packages.
The runner keeps state in memory and only starts QEMU processes and loopback
sockets on the Windows host. Build the regular Linux amd64 CLI and daemon
first. Then build the TTY checker as a standalone binary and assemble the
initramfs. The example uses PowerShell and assumes `go` and `python` are on `PATH`.

```powershell
./scripts/build.ps1 -Targets linux-amd64
$env:GOOS = 'linux'
$env:GOARCH = 'amd64'
$env:CGO_ENABLED = '0'
go build -trimpath -o "$LabDir/ttycheck" ./scripts/lab/ttycheck_linux.go
python scripts/lab/net-initramfs.py --debian-kernel $KernelDeb `
  --alpine-packages $AlpinePackages --binaries dist/linux-amd64 `
  --ttycheck "$LabDir/ttycheck" --output "$LabDir/image"
python scripts/lab/net-matrix.py --qemu $QemuExe --image "$LabDir/image" `
  --logs "$LabDir/run"
```

The runner executes raw USB/IP, managed pairing and fresh attachments, USB
replug, three complete packet-loss intervals, and forced TCP socket closure.
It compares the import and lease states before and after interruption.

## Results after the fix

Stock USB/IP listed, attached and detached the device across the two kernels.
The exchange verified 32 KiB in each direction. For managed USB/IP, each
profile used a fresh lease and verified 64 KiB in each direction.

| Managed profile | Mean ICMP RTT | Time to TTY | Time to verify exchange | Egress drops, host / client |
| --- | ---: | ---: | ---: | ---: |
| Clean | 28.9 ms | 3.41 s | 0.28 s | 0 / 0 |
| LAN | 30.8 ms | 3.91 s | 0.38 s | 0 / 0 |
| WAN | 94.0 ms | 4.94 s | 0.95 s | 3 / 0 |
| Mobile | 195.1 ms | 7.36 s | 3.76 s | 1 / 5 |
| Severe | 408.7 ms | 15.16 s | 7.38 s | 13 / 13 |
| Asymmetric | 174.5 ms | 7.34 s | 2.63 s | 4 / 7 |

Drop counters include ICMP and AnyUSB traffic on the test interfaces. The
clean RTT includes scheduling overhead on the QEMU host. Time to TTY and
network recovery use the host observer's clock. The byte exchange timer runs
inside the client guest.

On the mobile profile, all three virtual USB replugs reopened the TTY and
verified 4 KiB in each direction, in 13.11, 13.38, and 13.44 seconds from
gadget rebind. That recovery time exceeded a few seconds in this lab.

The three-second blackhole began after the client had received the first 4 KiB
of a 256 KiB exchange. All 256 KiB matched in both directions, and the local USB
incarnation and lease stayed the same. After a twelve-second blackhole, the
client reattached automatically and verified bytes after 13.39 seconds from
network restoration.

For the TCP reset case, `ss -K` terminated two established peer sockets.
The client acquired a fresh local USB incarnation under the same lease and
verified both directions after 13.08 seconds. In the forty-second blackhole,
the host unexported the gadget and the client cleared the import and lease
before restoration. An explicit reconnect obtained a new lease, reopened the
TTY after 8.03 seconds, and verified 4 KiB each way.

All 14 cases passed in the fixed run. The Windows amd64 verification also
passed 12 Go tests, `go vet ./...`, and `go test -race ./...`.
[Full network lab console](virtual-network-console.txt)

## Defect found before the fix

In the first full run, the 16 KiB exchange completed after a three-second
blackhole. The client reported `lost` immediately afterward. The client
treated one failed renewal as proof that the device was offline and detached
the import, even when the data stream recovered through TCP retransmission.
The second run on the old build passed the short-outage case, so the failure
was intermittent.

The client now retains an existing import during a short renewal interruption.
It still requires a successful renewal to open a new import. The grace starts
at the last successful renewal and uses a six-second threshold. A confirmed
authorization or lease rejection ends the session immediately. The regression
test injects a temporary renewal error followed by a successful renewal and an
explicit rejection.

## Limits

Both guests share one Windows host and QEMU's software CPU emulation. Linux
queuing provides controlled delay, loss, and rate limits. Physical LAN or
Wi-Fi, Tailscale paths, Windows native USB/IP, and ESP32-S3 flash, reset and
JTAG remain **UNVERIFIED** under the Phase 1 hardware criteria.
