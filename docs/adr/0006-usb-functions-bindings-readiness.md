# 0006. USB functions, local bindings and readiness

**Accepted 2026-11-23.**

Keep USB/IP passthrough and the exclusive lease scoped to one whole physical
USB device. A composite device is one share and one lease even when it exposes
several interfaces. Do not put ST-Link, J-Link, serial or another product
protocol inside the managed tunnel.

Extend the v1 JSON model additively. A device reports its device descriptors,
speed, interface functions, derived profiles, risk and reconnect policy. An
import reports OS-local bindings and an aggregate readiness state. Retain
`serial_ports` as a compatibility projection of bindings whose kind is
`serial`; clients must not require it to be non-empty.

A USB function is identified by configuration, interface and alternate-setting
numbers and records its class, subclass, protocol and bound driver when known.
A local binding records the importing OS object created for a function, such as
a COM/tty name, PnP instance, hidraw/input node, block node, network interface
or generic USB device path. USB protocol endpoints are not local bindings.

Use these readiness states:

- `attached`: the native USB/IP import exists but child enumeration is not yet
  complete;
- `enumerating`: descriptors or bindings are still being resolved;
- `ready`: the observed functions have usable driver bindings, or an explicit
  profile probe passed;
- `degraded`: some functions are ready and others report a problem;
- `driver_missing`: an observed function has no usable client driver;
- `error`: the importing OS reports a device or driver problem;
- `unknown`: the available evidence cannot establish application readiness.

Class and product profiles are advisory metadata. Prefer live USB descriptors
and OS bindings, then exact known VID/PID metadata, then normalized product
text. A profile must never establish that a native application works. Support
is claimed only by a dated compatibility row with an exact device, backend,
driver and application workload.

Managed connections require explicit risk acceptance for storage, input,
network and audio/video profiles. Persist that acceptance with the stable
connection intent so a confirmed connection can recover after restart. Unknown
and development devices use `automatic`; high-impact devices use `confirmed`,
meaning that recovery belongs only to the explicitly accepted persistent
connection and must never be described as storage or application-state
recovery. Raw stock-USB/IP commands remain an administrator-only lab workflow
and do not inherit managed policy enforcement.

Profile probes are explicit, read-only local actions. They may enumerate
ST-Link with `STM32_Programmer_CLI -l`, J-Link with Commander
`ShowEmuList USB`, or CMSIS-DAP with `pyocd list`. They never run in the
background, accept a firmware update, halt a target or write memory. A missing
optional vendor tool does not make generic passthrough unavailable.

Linux obtains functions and driver bindings from sysfs ancestry. Windows maps
the exact usbip-win2 virtual hub port to its PnP parent and descendants before
reporting bindings, so identical VID/PID devices remain separate. Hubs and host
controllers are not normal share targets.

This decision keeps old v1 clients functional while allowing new clients to
distinguish transport attach, OS enumeration, driver readiness and native-tool
verification. It increases platform inventory work and requires class-specific
safety and hardware evidence instead of one universal “all USB” claim.
