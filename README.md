# AnyUSB

Share USB devices between Windows and Linux over LAN or Tailscale. AnyUSB will
orchestrate existing USB/IP backends and run remote ESP operations on the host
with the physical board. Android is planned for a later phase.

M1 provides the Linux host and client adapter, the `anyusbd` agent, and the
`anyusb` CLI. See the [architecture](docs/architecture.md) and
[M0 hardware report](docs/test-report/m0.md) for the wider plan and current
hardware verification status.

## Build

Use Go 1.24 or newer. Build on Linux, or set `GOOS=linux`, `GOARCH=amd64` or
`arm64`, and `CGO_ENABLED=0` when cross-compiling.

```sh
mkdir -p dist
go build -trimpath -o dist/anyusbd ./agent/cmd/anyusbd
go build -trimpath -o dist/anyusb ./cli
```

## Run on Linux

Install the distribution's USB/IP tooling for the running kernel. A host needs
`usbip-host` and its stock `usbipd` daemon. A client needs `vhci-hcd`. Follow
the [lab setup](docs/test-report/m0-lab.md) for module and backend commands.
Use a trusted lab network with restricted TCP 3240 access for sharing.

Start the agent in one terminal and use the CLI from another.

```sh
sudo ./dist/anyusbd
sudo ./dist/anyusb doctor
sudo ./dist/anyusb list
```

The CLI uses a root-only Unix socket at `/run/anyusb/control.sock`. Both
commands accept `-socket PATH` before positional arguments. If the USB/IP tool
is outside the service's `PATH`, start `anyusbd` with `-usbip /path/to/usbip`.
A [systemd unit](scripts/systemd/anyusbd.service) is available for deployments
that place `anyusbd` in `/usr/local/bin`.

### Host

Replace `1-2` with the physical device's current busid or the port-based ID
from `anyusb list`.

```sh
sudo ./dist/anyusb share 1-2
sudo ./dist/anyusb unshare 1-2
```

### Client

Replace `usb-host` and `1-2` with the remote host and an exported busid. Use
the virtual port from `connect` or `list --imports` when disconnecting.

```sh
sudo ./dist/anyusb list --remote usb-host
sudo ./dist/anyusb connect usb-host 1-2
sudo ./dist/anyusb list --imports
sudo ./dist/anyusb disconnect 0
```

`anyusb watch` streams local hardware and export changes as NDJSON.
`anyusb -json list --imports` returns structured inventory, including the
serial device nodes that are available at the time of the call. This milestone
uses explicit share/connect operations. Peer pairing, leases, automatic
reconnect, and remote ESP tooling are later milestones.
