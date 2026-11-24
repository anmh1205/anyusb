# General USB hardware acceptance procedure

Use this procedure only with named lab hardware and a disposable target or
storage medium. It extends [the M0 stock-tool procedure](m0-lab.md); all of its
version, raw-output, timestamp and recovery-time requirements still apply.
Driver installation, reboot, firewall changes and network-policy changes need
explicit approval before execution.

An import is not a pass by itself. Record four separate boundaries:

```text
USB/IP transport attached
  -> importing OS enumerated the USB functions
  -> correct client drivers and local bindings are ready
  -> the native application completed the named workload
```

Create one report and raw console log per exact device revision and direction.
Use `UNVERIFIED` for every step that was not observed on hardware.

## Common inventory

Before and after each case, record the AnyUSB commit/build, device make and
hardware revision, VID:PID, serial, firmware, physical USB path, cable/hub,
host and client architecture, OS/kernel/build, native USB/IP tool versions,
driver/service versions and native application version.

```text
anyusb doctor
anyusb list
anyusb list --peer PEER
anyusb list --connections
anyusb list --imports
```

On Linux also capture `uname -a`, `usbip version`, `usbipd --version`,
`lsusb -v`, `lsusb -t`, relevant sysfs interface descriptors, `udevadm info`,
and `journalctl -k`. On Windows capture `Get-ComputerInfo`, `usbipd --version`,
`usbipd state`, `usbip.exe --version`, `usbip.exe port`, the physical and
usbip-win2 PnP ancestry, class GUID, service, status and problem code.

## Debug probes

Run the read-only AnyUSB profile probe first. It must report the exact imported
serial; a missing optional vendor tool does not fail generic passthrough.

```text
anyusb probe --connection CONNECTION stlink
anyusb probe --connection CONNECTION jlink
anyusb probe --connection CONNECTION cmsis-dap
```

The implemented enumeration commands are `STM32_Programmer_CLI -l`, J-Link
Commander `ShowEmuList USB`, and `pyocd list`. Save the reported tool/version,
command, duration, output and serial match. Do not accept a probe-firmware
update prompt as part of an automated run.

For each approved target, then run the project's native programmer/debugger
with an explicit probe serial, exact target part, interface and speed. Record:

1. target connect and identification;
2. a non-destructive target memory/register read;
3. halt and resume;
4. flash of a designated disposable image and independent read-back/verify;
5. IDE/debug-server connection by the same serial;
6. optional VCP open and binary exchange as a separate ST-Link function.

Never infer a debug pass from VCP success or vice versa. For ST-Link composite
hardware, repeat debug-only, VCP-only and simultaneous debug-plus-VCP. For
CMSIS-DAP, record v1 HID versus v2 bulk. For J-Link, record the selected SEGGER
driver family and compare USB/IP with J-Link Remote Server only after a failed
or high-value passthrough case; never switch modes automatically.

Run at least 10 connect/disconnect cycles and 10 physical replug or target
reset/re-enumeration cycles in every claimed direction. Record min/median/max
recovery time and every failure.

## Disposable storage

The operator must identify the exact removable disk by serial and capacity and
confirm it contains no required data. Refuse the case if identity is ambiguous.
Record `confirm_risk`, the block/disk binding and whether automatic recovery
belongs to the same accepted persistent connection.

1. Create a fresh disposable filesystem and write a deterministic data set.
2. Flush (`fsync`/`sync`), unmount/eject cleanly, reconnect, remount and compare
   a SHA-256 manifest.
3. Repeat 10 clean detach/attach cycles.
4. With a second freshly formatted medium, interrupt an active write once,
   allow the OS to settle, run the native filesystem check, and record all
   damage and recovery. Do not call this data recovery.
5. Verify explicit disconnect, service restart and sleep/resume separately.

## Controlled HID and security tokens

Start with a lab-programmed vendor-page HID that cannot inject keyboard or
pointer input. Confirm the expected HID/hidraw binding, exchange a bounded test
report, disconnect and verify the node disappears. Physical keyboards and mice
remain `UNVERIFIED` and should not be enabled as a support claim from this case.

For a security token, use a spare test credential and the platform's native
client. Verify enumeration, user-presence flow, one test authentication,
disconnect/reconnect and credential usability. Never use a production token or
export private credential material to the report.

## Network, audio, video, printer and scanner

Network adapters require a non-management test path so detaching the adapter
cannot remove control of either AnyUSB machine. Record link, addressing,
traffic integrity, throughput and recovery. Audio/video require sustained
workload latency, drop and bandwidth measurements in both directions. Printers
and scanners require a native job plus cancel/recovery. These families remain
`EXPERIMENTAL` or `UNVERIFIED` until their exact matrix rows pass.

USB hubs and host controllers are excluded. A managed share attempt must be
rejected and no native export should be created.

## Acceptance record

For every row, attach raw stdout/stderr and record:

- share, lease, native export, attach, functions, bindings and readiness;
- native application workload result and exact serial selection;
- disconnect cleanup and recovery distribution;
- failures, OS event logs and whether a reboot or driver reset was needed;
- final status: `SUPPORTED`, `VERIFIED`, `EXPERIMENTAL`, `UNVERIFIED`, or
  `UNSUPPORTED` using [the support vocabulary](../general-usb-support.md).

Only an exact passing row may be named in UI/release support claims.
