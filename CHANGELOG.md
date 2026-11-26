# Changelog

## Unreleased

- Reviewed UI/UX, installation, and core reliability across host and client workflows:
  - Fixed `agent/mesh` lock contention (`hostMu` vs `mu` and per-connection `statsMu`) so live USB/IP relay traffic never stalls behind Windows PnP/PowerShell or Linux inventory/export calls.
  - Fixed zombie-tunnel reconnect deadlocks during NAT route flips by adding WebSocket ping keepalives, immediate tunnel-loss wakeup in `followLease`, and superseding stale same-owner tunnels in `acquireWithPolicy`.
  - Fixed `devicesFromDiscovery` preference (preferring Tailscale IPv4 over IPv6 and syncing peer names), added dynamic mDNS interface monitoring (`MaintainAdvertise`), suppressed healthy poll clutter in `CommandDiagnostics`, and waited for host serial port re-enumeration in `BeginESP`.
  - Fixed Windows `Doctor()` to verify `RUNNING` state rather than SCM registration alone, distinguished peer 401 revocations from local UI token expiry, added peer name/prefix resolution, flexible CLI flag ordering, and human-readable CLI tables/summaries.
  - Enhanced Windows/Linux installers (`-Upgrade`/`--upgrade`, `-Desktop`, Windows service auto-recovery, `apt-get -y`, `linux-modules-extra` fallback, UFW hint) and Desktop auto-retry/re-auth on service restarts.
  - Refined the Web/Desktop UI for simple two-sided setup and daily operation: automatic health/reboot alerts, quick-start 2-side guide, copyable local IP/Tailscale addresses, peer unpair & endpoint editing, holder peer names on shared USBs, copyable COM/tty ports, and collapsible technical telemetry.
- Replaced host-side Python/`esptool` subprocesses and `pyserial` monitor scripts
  with a pure-Go ESP ROM SLIP bootloader and serial engine (`agent/esp`),
  removing runtime Python dependencies from Linux and Windows installs while
  preserving ESP-IDF `flasher_args.json` manifest compatibility. Verified on
  Linux host `anmh1205` against a physical ESP32-S3 C28P (`303a:1001`) over
  Tailscale (`chip-id`, `verify`, `flash --baud 460800`, `monitor`, `reset`) and
  on Windows `COM11` forwarded via Managed USB/IP (`usbip-win2`); also tuned
  Windows `resetSequence` in `agent/esp/flasher.go` to prefer `usb-jtag` before
  `classic` for USB-capable targets, cutting `COM11` `chip-id` time from
  `47.08s` to `0.90s`. Local non-`usbip-win2` Windows hosts and non-ESP32-S3
  chips remain `UNVERIFIED`.
- Updated `scripts/systemd/anyusbd.service` to wait for `network-online.target`
  and `tailscaled.service` with `RestartSec=2` so Tailscale-bound agents do
  not fail on boot before interface IP assignment.
- Simplified CPace pairing invites and interactive TLS approval codes from 8
  digits to 4 digits, tightened invite start attempts to 3 tries per 2-minute
  window, and cleared exhausted invites on final proof failure.
- Fixed USB CDC classification so ACM control/data pairs remain serial while
  ECM, EEM, NCM and MBIM functions are treated as network devices. Unknown or
  unmatched CDC data interfaces are no longer guessed to be serial, and
  whole-device risk policy remains authoritative for composite devices.
- Added typed managed-connection failures, explicit controller phases,
  bounded exponential retry with jitter, relay byte/progress statistics and
  persisted force-release fences. The CLI and desktop UI now expose retry
  state, force release, and downloadable diagnostics.
- Added a bounded native-command runner, versioned state migration with
  corrupt-state preservation, and development-only mock USB/IP scenarios for
  deterministic CDC, composite, driver, busy, duplicate and network tests.
- Added readiness-aware CLI waits for managed connections. `connect --wait`
  and `wait --connection` can distinguish a native USB/IP attachment from an
  aggregate ready OS driver binding and return the final connection as JSON.
- Fixed Windows import discovery on current usbip-win2 drivers by recognizing
  the `usbip2_ude` root controller and batching PnP property reads. Physical
  ESP32-S3 validation now maps virtual port 1 to `COM11` without timing out.
- Update tunnel byte counters and last-progress timestamps while a relay is
  still open, rather than only when it disconnects.
- Use CDC Union/IAD membership on Linux and compatible-ID descriptors on
  Windows to classify CDC devices before drivers bind. Report actual network
  interfaces and count a CDC control/data pair as one OS binding.
- Back off on failed native imports and bound pending relay writes. Export
  timestamped retry events with sensitive inventory details removed.
- Exercise enumeration, independently addressed identical devices and failure
  recovery through isolated mock agents and the normal managed connection API.
- Fixed managed USB safety and readiness edge cases: hub blocking now remains
  authoritative for composite and previously saved shares, active leases
  re-check changed device profiles, connection metadata follows lease renewal,
  Linux waits for local device nodes, and Windows no longer treats a healthy
  zero problem code, an enumerator parent, or an ambiguous VID/PID fallback as
  a failed or ready application binding.
- Added general USB functions, client-local bindings and readiness reporting
  for Linux and Windows, including composite and non-serial devices. Added
  explicit ST-Link, J-Link and CMSIS-DAP native-tool probes, class-aware risk
  confirmation, hub rejection, USB-neutral CLI/UI guidance, schemas and the
  hardware-evidence plan. Physical probe compatibility remains `UNVERIFIED`
  until the recorded hardware matrix is run.
- Added a desktop UI and Windows and Ubuntu package builders. The desktop app
  discovers peers, approves pairing, and attaches shared USB devices using
  native USB/IP.
- Display the detected local COM or tty port for use in VS Code and other IDEs.
  Desktop installations run in USB forwarding mode.
- Remember requested USB attachments across service restarts and keep retrying
  until the remote machine and device return or the operator disconnects it.
- Map Windows COM ports through the exact usbip-win2 virtual hub port, including
  multiple imported devices with the same VID and PID.
- Upgrade the Windows desktop package in place while preserving pairing,
  sharing, and requested connection state.

- Kept active managed USB/IP imports through brief lease-renewal interruptions.
  A new import still requires a confirmed lease and current bus ID.
- Fixed intermittent Linux attach failures when `usbip port` cannot yet read
  the new VHCI import, and a Windows PowerShell PnP inventory error with a
  single device ID.
- Added the Windows service and usbipd-win and usbip-win2 adapters.
- Added TLS peer identity, code pairing, LAN discovery, managed USB/IP
  leases, tunnel authorization, and reconnection.
- Added host-side ESP jobs, firmware upload, live logs, and serial monitoring.
- Added the loopback UI, build script, and driver-aware installation scripts.

- Added the Linux USB/IP agent and CLI with sysfs inventory, share and unshare,
  remote listing, attach and detach, a local hotplug stream, and diagnostics.
- Added a Linux systemd unit and build instructions for the agent and CLI.
- Documented the proposed AnyUSB architecture and four M0 design decisions.
- Recorded host prerequisites, Windows release-package checks, the open hardware
  matrix, and a reproducible lab procedure for both USB/IP directions.
