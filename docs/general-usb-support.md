# General USB support plan

**Planning baseline: 2026-11-23.** This document turns the existing USB/IP
passthrough into an explicit, testable product contract for debug probes and
general USB devices. It is a roadmap, not evidence that the listed hardware is
already supported. Results without a matching hardware run remain
`UNVERIFIED` in `docs/test-report/`.

## Decision summary

AnyUSB already forwards a whole USB device. It does not carry serial bytes as
its managed data plane: the importing operating system attaches the remote
device through its native USB/IP virtual controller and binds its own drivers.
COM and tty names are metadata discovered after that attach. Host-side ESP
operations are a separate, serial-specific mode.

The next phase should therefore retain the existing USB/IP and whole-device
lease architecture. It should add a neutral model of USB functions and local
driver bindings, class-aware readiness and risk policy, and a hardware
compatibility matrix. ST-Link, J-Link and similar probes become verified
profiles on top of the generic device model; they must not become special cases
inside the USB/IP tunnel.

"General USB support" means:

- Any non-hub device that the selected host backend can export, the selected
  client backend can import, and the client operating system can drive may be
  offered for passthrough.
- Product support is claimed only for a recorded device, host/client direction,
  operating-system and backend version, driver, native application and tested
  workload.
- Unknown devices may be forwarded after explicit sharing, but an attach alone
  is not presented as proof that the device or its application is ready.
- Hubs, host controllers and platform-critical devices are outside the normal
  lease boundary. Timing-sensitive and high-bandwidth devices remain
  experimental until their workloads pass a dedicated lab procedure.

## Implementation status

**2026-11-24 software baseline:** ADR 0006, the additive function/binding
model, Linux sysfs discovery, Windows exact-virtual-port PnP discovery,
readiness aggregation, risk confirmation, hub rejection, USB-neutral CLI/UI,
optional ST-Link/J-Link/CMSIS-DAP probes, schemas and lab procedures are
implemented. The [software and virtual-composite report](test-report/general-usb-software-2026-11-24.md)
records 46 Go test events, vet, race, four cross-build targets and a passing two-VM
CDC + vendor-HID + mass-storage matrix with storage integrity checks.

Milestone evidence is intentionally split:

- M0 and the software scope of M1 are complete.
- M2 is verified for the Linux virtual composite and Windows topology fixtures;
  real usbip-win2 driver behavior remains `UNVERIFIED` on this host.
- M3 profile probes and guidance are implemented, but physical ST-Link, J-Link
  and CMSIS-DAP rows remain `UNVERIFIED` because none of those probes or vendor
  tools were present.
- M4 policy and the disposable virtual HID/storage workload are verified;
  physical storage-loss, keyboard/mouse, token, network and audio/video rows
  remain `UNVERIFIED`.
- M5 software hardening and the adverse-network virtual baseline pass. Physical
  upgrade, sleep/resume and exact hardware rows remain open evidence, not
  hidden support claims.
- M6 remains closed by design: no vendor provider is added without a concrete
  failed or high-value J-Link USB/IP row and a separate ADR.

## Current-state audit

| Area | Observed in the repository | Consequence |
| --- | --- | --- |
| Data plane | `platform.Adapter` exports and attaches a busid; the managed tunnel validates the USB/IP import request and relays its binary stream. | The transport is device-generic and should be preserved. |
| Ownership | One managed lease covers one physical USB device, including a composite device. Connection intent and reattach are already device-scoped. | ST-Link/J-Link composite functions should share one lease, not separate leases per COM/debug interface. |
| Inventory | `platform.Device` exposes the top-level identity, VID/PID, product strings and export state. Linux reads the device sysfs node; Windows reads `usbipd state` plus PnP location. | The service cannot explain which functions a composite device contains or whether their drivers are ready. |
| Imported state | `platform.Import` records the virtual USB/IP port and a serial-port list. Linux maps tty descendants; Windows maps COM descendants through the exact usbip-win2 virtual hub port. | COM/tty is the only modeled local binding. Debug, HID, block, WinUSB/libusb and other functions are invisible to the product. |
| UI and CLI | The UI correctly says that USB is attached, then gives serial-oriented IDE guidance. CLI import tables have a `SERIAL PORTS` column. | Users can mistake the product for a COM-only tool, and non-serial devices have no positive readiness signal. |
| Device-specific operations | The only host-assisted profile is ESP (`esptool`, monitor and reset). | This must stay distinct from generic passthrough. Future vendor-assisted modes need their own contracts and leases. |
| Verification | Software tests and a virtual CDC ACM lab cover generic attach/relay/reconnect mechanics. Current reports keep physical Windows USB/IP, ST-Link/J-Link and native debugger workloads unverified. | Existing evidence supports the transport mechanism, not a broad hardware compatibility claim. |

The first architectural defect is not the tunnel. It is that the public model
jumps from a top-level USB device directly to `SerialPorts`, so neither the UI
nor tests can express success for a debugger, storage device or other USB
function.

## Target system model

```text
physical port + whole USB device
        |
        +-- descriptors and configurations
        |       +-- function/interface: serial
        |       +-- function/interface: debug probe
        |       +-- function/interface: mass storage
        |       `-- function/interface: vendor-specific/unknown
        |
        +-- one AnyUSB share and exclusive lease
        |
        `-- one native USB/IP import on the client
                +-- OS driver binding(s)
                +-- local handles/nodes where meaningful
                `-- optional native-tool readiness probe
```

Three concepts must remain separate:

1. A USB **endpoint** is a protocol-level transfer endpoint described by the
   device. It is not a COM port, block device or debugger handle.
2. A USB **function/interface** describes part of a simple or composite device,
   such as CDC data, mass storage, HID or a vendor-specific debug interface.
3. A **local binding** is what the importing OS created for a function: for
   example `COM8`, `/dev/ttyACM0`, a PnP instance, a kernel driver, a block node
   or a hidraw node.

The service should report each layer without treating one as proof of the next:

```text
transport attached -> USB enumerated -> driver bound -> application probe passed
```

For an unknown device, `USB enumerated` can be the highest generic readiness
claim. For a verified ST-Link profile, a successful native-tool probe may
establish application readiness.

## Support and evidence vocabulary

Use the following terms consistently in UI, documentation and reports:

| Status | Meaning |
| --- | --- |
| `SUPPORTED` | The exact compatibility row meets its acceptance procedure and is intended to remain working. |
| `VERIFIED` | A dated run passed, but the row is not yet a maintained support commitment. |
| `EXPERIMENTAL` | Passthrough is available, but limits or failure behavior are not yet bounded well enough for a support promise. |
| `UNSUPPORTED` | The project deliberately blocks or excludes the case, or an authoritative backend constraint prevents it. |
| `UNVERIFIED` | No qualifying run exists for the exact row. This remains the required report label. |

Never infer `SUPPORTED` from VID/PID recognition, successful sharing, a virtual
port, a COM/tty name, or another project's tested-device list. Those facts can
select guidance or a lab case, but the AnyUSB workload still has to pass.

## Device-family scope

### First support target: development and debug devices

| Profile | Functions to model | Client-side acceptance tool | Important cases |
| --- | --- | --- | --- |
| USB serial | CDC ACM, CP210x, CH340, FTDI | Independent serial open and binary exchange; IDE monitor | Multiple ports, reset/re-enumeration, stable mapping |
| ST-Link | ST debug, optional bridge and VCP interfaces | `STM32_Programmer_CLI` or STM32CubeProgrammer plus OpenOCD/IDE; VCP checked separately | ST-LINK/V2.1 and STLINK-V3, composite interface drivers, target reset |
| J-Link | Current WinUSB or SEGGER driver and vendor-specific debug interface | J-Link Commander/GDB Server plus an actual target connection | Probe serial selection, legacy versus current driver, firmware update prompts |
| CMSIS-DAP | HID or WinUSB/vendor interface | pyOCD or OpenOCD target detection, halt and resume | CMSIS-DAP v1 HID versus v2 bulk |
| DFU/bootloader | DFU class or vendor bootloader | Vendor/standard enumeration plus read-only identification before write tests | Runtime-to-bootloader VID/PID change, same-port approval |

ST's current Windows package describes ST-Link as a composite device with
debug, bridge and virtual-COM interfaces. The Windows USB/IP host project lists
an ST-LINK/V2.1 as tested, but that is upstream evidence only; it does not cover
AnyUSB's managed tunnel, both import directions or target operations. SEGGER
also ships J-Link Remote Server. That is a useful fallback or later optimized
provider, not evidence that raw J-Link USB/IP passthrough works.

### General USB expansion

| Class or use | Initial policy | Minimum acceptance evidence |
| --- | --- | --- |
| Mass storage/card reader | Opt-in, warn about unsafe removal; do not promise transparent recovery during writes | Enumerate, read hash, controlled write/fsync/unmount, reconnect and filesystem check on sacrificial media |
| Security key/smart card/license dongle | Opt-in; preserve physical user-presence expectations | Native middleware/application operation, unplug/replug and exclusivity tests |
| HID test devices | Permit non-critical devices; warn or block keyboards/mice until input-injection policy is accepted | Descriptor/binding evidence plus deterministic reports from a controlled HID fixture |
| Printer/scanner | Experimental by model | Native driver install, representative job, cancellation and replug |
| USB network/Bluetooth adapters | Experimental and high-impact | Link creation, traffic, detach/recovery and proof that host/client management paths stay reachable |
| Audio/video/capture | Experimental, never covered by a generic claim | Sustained isochronous workload with loss, latency, bandwidth and recovery measurements on each backend direction |
| USB hub/host controller | Unsupported as a normal share target | Explicit exclusion test and diagnostic |
| Unknown/vendor-specific | Attach allowed after explicit share; readiness remains unknown | Enumeration and exact local driver state; add a profile only after workload verification |

## Required product capabilities

### R1. Preserve generic whole-device passthrough

- Keep USB/IP as the default data plane and native host/client tools in
  commands and diagnostics.
- Keep the share, lease, tunnel authorization and reconnect unit at the whole
  physical device.
- Do not require a COM/tty endpoint to share or connect a device.
- Keep host-assisted ESP operations mutually exclusive with passthrough.

Acceptance: a device fixture with no serial function can be shared, leased,
attached, listed and disconnected without an empty serial list being treated as
an error.

### R2. Represent USB functions without guessing readiness

Extend the platform model additively. The exact Go names belong in the design
ADR, but the information contract should cover:

```text
Device
  identity, topology, descriptors, speed
  functions[]
    configuration/interface numbers
    class/subclass/protocol
    descriptors and profile hints

Import
  native USB/IP port and remote identity
  local_bindings[]
    function/interface reference
    kind, OS identifier/path, driver, status/problem
  readiness
    attached | enumerating | ready | degraded | driver_missing | error | unknown
```

- Retain `serial_ports` during the v1 compatibility period and derive it from
  `local_bindings[kind=serial]`.
- Prefer class/interface descriptors and OS state. A known VID/PID database may
  add a display name, risk hint and suggested probe, but must not override live
  descriptors or prove capability.
- Model composite devices once at the top level and associate bindings with
  their child functions.

Acceptance: fixtures cover a serial-only device, composite ST-Link-shaped
device, HID device, mass-storage device and unknown vendor-specific device.

### R3. Resolve client bindings on both platforms

Linux work:

- Enumerate interface nodes below the imported sysfs device and record
  `bInterfaceClass`, subclass, protocol and bound driver.
- Generalize the existing tty ancestry mapper to binding resolvers for tty,
  hidraw/input, block, network and the generic `/dev/bus/usb` identity where
  useful.
- Keep driver-less vendor interfaces visible rather than silently dropping
  them.

Windows work:

- Extend the exact usbip-win2 virtual-hub-port mapping from the imported parent
  to all PnP descendants, not just class `Ports`.
- Record class/class GUID, instance ID, service/driver, status and problem code.
- Emit COM names as serial bindings, and expose non-serial descendants without
  inventing filesystem-style paths for APIs that use device interfaces.
- Keep identical VID/PID imports disambiguated by virtual port.

Acceptance: two identical composite devices on separate virtual ports produce
separate child-binding sets on Windows and Linux fixtures.

### R4. Make UI and CLI USB-neutral

- Replace the CLI-only `SERIAL PORTS` emphasis with a generic bindings/readiness
  view while keeping serial ports easy to copy.
- Show device functions and driver state after attach. Give COM/tty instructions
  only when a serial binding exists.
- For ST-Link/J-Link/CMSIS-DAP, show the verified native tool and probe serial or
  instance information when available. Do not claim tool readiness until its
  probe passes.
- Show `driver missing`, `enumerating`, `attached but unverified` and
  `experimental` distinctly.
- Show risk-specific confirmation for storage, input and network devices.

Acceptance: UI tests cover serial, debugger-only, composite, unknown and
driver-missing cards. A non-serial attached device receives useful guidance and
never tells the user to select a COM port.

### R5. Add profile probes outside the transport core

A profile probe observes an imported device using the same native tool the user
will use. It does not change the USB/IP tunnel or replace the OS driver.

- Define a small probe interface with a time limit, structured result, exact
  tool/version and no destructive operation by default.
- Start with read-only enumeration for ST-Link, J-Link and CMSIS-DAP.
- Never launch a vendor probe automatically in the background. Even a nominal
  enumeration command may claim an exclusive device handle, interrupt an IDE
  or prompt for probe firmware. Probes require an explicit operator/lab action,
  and AnyUSB must not accept a firmware update prompt on the user's behalf.
- Run target halt/flash/write operations only in an explicit lab or operator
  action with a named target.
- Keep vendor software optional. `doctor` should distinguish missing optional
  profile tooling from a broken USB/IP backend.
- Treat J-Link Remote Server or another vendor-native network mode as a future
  transport provider with a separate lease/mode, not as a hidden fallback.

Acceptance: a missing profile tool leaves generic passthrough usable; a failed
probe reports its command, version boundary and actionable driver state.

### R6. Apply class-aware recovery and safety policy

- Keep automatic reconnect for development devices after its profile tests
  pass.
- Default storage to a conservative policy: warn before connect and do not
  describe reattach as data recovery. Decide in the ADR whether automatic
  reattach is disabled by default for writable block devices.
- Require an additional warning or deny policy for keyboards, mice and adapters
  that could change the importing machine's input or network control.
- Reject hubs explicitly on both platforms and document the whole-device lease
  boundary.
- Preserve current requirements for explicit approval before installing
  drivers or changing firewall/network policy.

Acceptance: policy tests show that classification changes guidance or reconnect
behavior but never silently shares a device.

### R7. Publish a compatibility matrix backed by reports

Each row records:

- exact device make/model, hardware revision, VID/PID and firmware;
- host and client OS/kernel/build, architecture and USB topology;
- host/export and client/import backend versions;
- client driver/service and native application version;
- AnyUSB commit/build and network path/profile;
- share, attach, driver binding, native-tool operation and disconnect results;
- reset/replug cycles, recovery distribution and every failure;
- status from the vocabulary above and a link to raw output.

Acceptance: no family-level support statement exists without at least one
maintained row, and the UI/documentation can link the exact scope of a claim.

## Delivery plan and work breakdown

### M0 — Contract and architecture decision

**Objective:** agree on what “general USB” promises before changing APIs.

- Write ADR 0006 for the device/function/binding/readiness model.
- Decide additive v1 fields versus a versioned local API migration.
- Decide the default reconnect policy for writable storage and high-impact HID
  or network devices.
- Define profile matching precedence: live descriptors, OS bindings, known
  device metadata, then operator override.
- Define the initial supported matrix: at minimum one ST-Link, one J-Link, one
  CMSIS-DAP device and the existing serial baseline.
- Freeze lab hardware models and obtain explicit approval before driver or
  network changes.

**Exit:** ADR accepted, schema examples reviewed, matrix rows and lab owners are
named, and no implementation question can silently redefine the support claim.

### M1 — Neutral model and software fixtures

**Objective:** remove the serial-only shape without changing the data plane.

- Add function, local-binding and readiness types to `agent/platform`.
- Preserve JSON compatibility for `serial_ports`.
- Add descriptor/class parsing and deterministic aggregation rules.
- Extend peer/local API schemas and examples.
- Add table-driven classification tests and fixtures for simple, composite,
  duplicate, unknown and partially bound devices.
- Add UI and CLI representations for non-serial devices.

**Exit:** all current Go tests and vet pass; schema compatibility tests pass;
non-serial fixtures complete share/connect/list/disconnect flows.

### M2 — Linux and Windows binding discovery

**Objective:** report what the importing OS actually created.

- Implement Linux interface and binding enumeration.
- Implement Windows PnP descendant and driver/problem enumeration using the
  exact virtual-port parent already used for COM mapping.
- Add bounded post-attach enumeration that ends in a structured readiness
  state instead of waiting only for serial ports.
- Extend `doctor` with platform backend and driver-binding diagnostics.
- Create a virtual composite gadget with at least CDC ACM, HID and mass-storage
  functions; use a disposable backing image.
- Verify two identical imported composite fixtures remain disambiguated.

**Exit:** virtual Linux tests prove multi-function import and bindings; Windows
fixtures prove exact topology mapping. Real Windows driver behavior remains
`UNVERIFIED` until M3/M4 hardware runs.

### M3 — Debug-probe compatibility pack

**Objective:** make ST-Link, J-Link and CMSIS-DAP first-class verified uses.

- Add read-only profile probes and version reporting.
- Add tool-specific guidance without embedding vendor tools in AnyUSB.
- Exercise Linux host -> Windows client, Windows host -> Linux client, and the
  same-OS directions that the selected backends support.
- For each probe, verify enumeration by serial, target connect, target ID/read,
  halt/resume, and a controlled flash/verify on a designated board.
- For composite ST-Link models, verify debug and VCP independently and together.
- Run at least 10 connect/disconnect cycles and 10 physical replug or target
  reset/re-enumeration cycles per release candidate row; record all timings.
- Compare J-Link USB/IP passthrough with SEGGER Remote Server before deciding
  whether a provider-specific mode belongs in a later release.

**Exit:** the compatibility matrix contains at least one passing maintained row
for ST-Link, J-Link and CMSIS-DAP, with raw logs. A failed raw J-Link row is
useful evidence but does not complete this milestone; it triggers the M6
provider decision. Marketing/UI claims name only the passing scopes.

### M4 — General-device safety and compatibility pack

**Objective:** expand beyond development probes without unsafe generic claims.

- Add mass-storage, controlled HID and security-token lab procedures.
- Implement the accepted reconnect/risk policy and confirmation text.
- Verify storage on disposable media, including hash, write, fsync, unmount,
  abrupt loss and filesystem check.
- Verify a controlled HID fixture before enabling physical keyboard/mouse use.
- Add optional printer/scanner and network-adapter rows only when hardware and
  operator need justify them.
- Define audio/video performance procedure; retain `EXPERIMENTAL` until both
  platform directions meet workload-specific thresholds.

**Exit:** each enabled class has a risk policy, native workload test and
failure/recovery procedure. Untested classes stay visible as unverified or
experimental rather than inheriting a global support badge.

### M5 — Release hardening

**Objective:** make support claims reproducible and maintainable.

- Run `go test ./...`, `go vet ./...`, race tests where supported and the full
  cross-build.
- Re-run the existing CDC and adverse-network baselines to protect reconnect,
  lease and tunnel behavior.
- Test upgrade/state migration with new schema fields.
- Test duplicate devices, partial driver binding, peer revocation during use,
  service restart, sleep/resume and explicit disconnect.
- Review driver installers, signatures, licenses and reboot effects for the
  exact versions used; do not install or alter firewall rules without approval.
- Publish release-specific compatibility rows and leave every uncovered row
  `UNVERIFIED`.

**Exit:** release evidence covers the generic transport, each promised profile,
upgrade/recovery and safety policy at their real boundaries.

### M6 — Optional host-assisted providers

**Objective:** add vendor-native remote workflows only where they outperform or
stabilize passthrough.

- Evaluate SEGGER J-Link Remote Server as a separate `jlink-remote` lease mode.
- Evaluate equivalent vendor-supported services only against a concrete failed
  or high-value USB/IP case.
- Keep provider lifecycle, authentication, licensing, client configuration and
  observability separate from the generic `usbip` mode.
- Never switch modes automatically while an application may own the device.

**Exit:** a provider is added only with an ADR, explicit operator choice,
exclusive ownership and its own compatibility evidence.

## Dependency and priority view

```text
M0 contract
   -> M1 neutral model
       -> M2 OS bindings
           -> M3 debug probes ----+
           -> M4 general devices -+-> M5 release hardening

M3 evidence -> optional M6 provider decision
```

The shortest path to the user's stated goal is M0 through M3. M4 broadens the
device families; M5 is required before a broad release claim. M6 is optional
when J-Link USB/IP passes; it becomes part of the critical path for J-Link if
raw passthrough cannot meet the accepted workload.

## Test matrix template

| Device/profile | Host | Client | Backend versions | Client driver | Native workload | Reconnect cycles | Status/report |
| --- | --- | --- | --- | --- | --- | --- | --- |
| Existing CDC baseline | Linux | Linux | Recorded in virtual lab | `cdc_acm` | Binary serial exchange | 10 virtual replugs | Verified in existing report |
| ST-Link model/revision | Linux | Windows | To record | To record | Programmer + OpenOCD/IDE + optional VCP | 10 + 10 | `UNVERIFIED` |
| ST-Link model/revision | Windows | Linux | To record | To record | Programmer + OpenOCD/IDE + optional VCP | 10 + 10 | `UNVERIFIED` |
| J-Link model/revision | Linux | Windows | To record | To record | Commander + GDB Server/IDE | 10 + 10 | `UNVERIFIED` |
| J-Link model/revision | Windows | Linux | To record | To record | Commander + GDB Server/IDE | 10 + 10 | `UNVERIFIED` |
| CMSIS-DAP v1/v2 | Each promised direction | Each promised direction | To record | To record | pyOCD/OpenOCD | 10 + 10 | `UNVERIFIED` |
| Mass-storage model | Each enabled direction | Each enabled direction | To record | Native storage stack | Hash + controlled write/unmount/fsck | Policy-specific | `UNVERIFIED` |

Add rows; do not replace exact models with a single family row. A passing
ST-LINK/V2.1 row does not automatically cover STLINK-V3, and a current WinUSB
J-Link row does not cover legacy SEGGER-driver models.

## Stop and reconsider conditions

Revisit the design before expanding implementation when any of these occurs:

- a backend cannot expose enough topology to associate imported child
  functions with the correct virtual port;
- a required class needs per-interface ownership while native USB/IP can lease
  only the whole device;
- automatic reattach can corrupt or destabilize the selected workload;
- a native vendor provider is materially safer or more reliable than USB/IP,
  and using it does not violate the product's ownership/security model;
- a support claim depends on installing an unapproved driver or opening a
  network path;
- hardware results differ by device revision, driver mode or backend version in
  a way the profile model cannot express.

## Definition of done for “not COM-only”

The milestone is complete only when all of the following are true:

- a non-serial USB device has a tested end-to-end path through the production
  service, CLI and UI;
- ST-Link and J-Link each have at least one passing, exact and reproducible
  compatibility row; a documented failure is evidence for replanning, not
  completion of this goal;
- the UI reports functions, bindings and readiness without requiring a COM/tty
  port;
- the API preserves composite-device ownership and exposes non-serial bindings;
- reconnect and risk behavior are defined per class, not inherited from the CDC
  baseline;
- every public support statement links to hardware evidence, while uncovered
  combinations remain `UNVERIFIED`.

## External implementation references

- [Linux USB/IP protocol and tools](https://docs.kernel.org/usb/usbip_protocol.html)
- [usbipd-win tested devices](https://github.com/dorssel/usbipd-win/wiki/Tested-Devices)
- [usbip-win2 client](https://github.com/vadimgrn/usbip-win2)
- [ST-LINK Windows USB driver and interface descriptions](https://www.st.com/en/development-tools/stsw-link009.html)
- [SEGGER J-Link Remote Server](https://www.segger.com/products/debug-probes/j-link/tools/j-link-remote-server/)
