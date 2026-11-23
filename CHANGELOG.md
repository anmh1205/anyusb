# Changelog

## Unreleased

- Added a desktop UI and Windows and Ubuntu package builders. The desktop app
  discovers peers, approves pairing, and attaches shared USB devices using
  native USB/IP.
- Display the detected local COM or tty port for use in VS Code and other IDEs.
  Desktop installations run in USB forwarding mode.
- Remember requested USB attachments across service restarts and keep retrying
  until the remote machine and device return or the operator disconnects it.
- Map Windows COM ports through the exact usbip-win2 virtual hub port, including
  multiple imported devices with the same VID and PID.

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
