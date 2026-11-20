# M0 hardware lab procedure

Run the four cases in the [M0 matrix](m0.md#hardware-matrix). Use an ESP32-S3
lab board with a known-good ESP-IDF build, a CP210x UART bridge, and a native
USB Serial/JTAG connection. Move the physical USB connection to the designated
host for each case. An Ubuntu VMware guest with exclusive USB passthrough can
provide an initial real-board check. Record the hypervisor and network path and
verify the target LAN and Tailscale paths separately.

Use the checked x64 release candidates in the [M0 report](m0.md#licenses-and-signatures).
Obtain approval before installing drivers, changing firewall or Tailscale rules,
or rebooting. The Windows `usbipd-win` MSI installs a local-subnet firewall rule
and the VBoxUSB drivers. The `usbip-win2` installer installs `usbip2_ude` and
`usbip2_filter`, can restart attached USB devices, and requires a reboot. Confirm
the installed drivers load with normal Windows signing policy. On Linux use
the distro's `usbip` package or kernel-matched `linux-tools`, as applicable.
[usbipd-win installation](https://github.com/dorssel/usbipd-win/tree/v5.3.0#how-to-install), [usbip-win2 installation](https://github.com/vadimgrn/usbip-win2/tree/v.0.9.8.1#install-usbip)

## Record the baseline

Save the exact commands, stdout, stderr, exit status, timestamps and relevant
screenshots under `docs/test-report/`. Use one log per direction and USB
interface. Include the USB path, busid, VID/PID, device serial when available,
driver and port mapping on both machines. Record the board model, firmware
commit, ESP-IDF and PlatformIO versions, monitor baud, and OpenOCD version.
Record the USB/IP backend versions independently of the kernel or Windows build.

Windows PowerShell commands. Run `usbip.exe` from the usbip-win2 install
directory or check its resolved path before using it.

```powershell
Get-CimInstance Win32_OperatingSystem | Select-Object Caption, Version, BuildNumber
Get-Command usbipd, usbip.exe | Select-Object Name, Source
usbipd --version
usbip.exe --version
usbipd list
usbipd state
Get-PnpDevice -PresentOnly -Class Ports
Get-PnpDeviceProperty -InstanceId $InstanceId -KeyName 'DEVPKEY_Device_LocationPaths'
python -m esptool version
```

Set `$InstanceId` to the USB parent device from inventory. On Linux, set
`BUSID` to the selected board's busid.

```bash
cat /etc/os-release
uname -r
usbip version
usbipd --version
modinfo usbip-host
modinfo vhci-hcd
lsusb -t
usbip list --local
udevadm info --query=property --path="/sys/bus/usb/devices/$BUSID"
python -m esptool version
```

## Linux host to Windows client

On the Linux host, load `usbip-host` and start the approved `usbipd` service
if needed. Use `sudo usbipd --daemon` only for an ad-hoc instance on the
approved test network when no service owns TCP 3240. Bind the chosen test board.

```bash
sudo modprobe usbip-host
usbip list --local
sudo usbip bind --busid="$BUSID"
```

On Windows, first verify that TCP 3240 is reachable from the intended client.
Set `$LinuxHost` to its approved address and `$BusId` to the board's current
busid from the remote listing.

```powershell
Test-NetConnection $LinuxHost -Port 3240
usbip.exe list --remote $LinuxHost
usbip.exe attach --remote $LinuxHost --bus-id $BusId --once --terse
usbip.exe port
Get-PnpDevice -PresentOnly -Class Ports
```

Record the numeric virtual USB port and the resulting COM port. In the prepared
ESP-IDF project, use `idf.py -p $ComPort flash monitor` and confirm the expected
boot log. In the prepared PlatformIO project, run
`pio run -t upload --upload-port $ComPort` and
`pio device monitor --port $ComPort --baud 115200` for both USB interfaces.
Close one monitor before another command takes the port.

## Windows host to Linux client

On Windows, identify the test device and use an elevated PowerShell for bind.
Start with ordinary bind. Record any error that would require a separate
forced-bind test.

```powershell
usbipd list
usbipd state
usbipd bind --busid $BusId
usbipd state
```

On Linux, load `vhci-hcd` and use the Windows address and current busid reported
by the remote listing.

```bash
sudo modprobe vhci-hcd
usbip list --remote="$WINDOWS_HOST"
sudo usbip attach --remote="$WINDOWS_HOST" --busid="$BUSID"
usbip port
lsusb -t
ls -l /dev/ttyUSB* /dev/ttyACM* /dev/serial/by-path/* 2>/dev/null
```

Run `idf.py -p "$TTY_PORT" flash monitor` with the observed device node.
CP210x generally enumerates under `ttyUSB` and native USB Serial/JTAG under
`ttyACM`. Confirm the boot log from the prepared image for both.

## Reset, replug and recovery

Run these checks against all four matrix cases.

1. Record the host bind and busid, client import and serial port. Run a
   non-writing baseline with esptool 5.2.0 using
   `python -m esptool --chip esp32s3 --port PORT --after hard-reset flash-id`.
   Use the real COM/tty in place of `PORT`. Esptool v4 spells the option value
   `hard_reset` and the subcommand `flash_id`. Record the actual command version.
2. Repeat with a physical reset while USB remains connected. Then use the ROM
   download mode or a board-supported USB-OTG/TinyUSB runtime mode as applicable.
   Record changes in VID/PID, device serial, busid, and USB parent path. Record
   whether the CP210x itself disappeared during target reset.
3. Physically unplug and replug at the same host port. Capture host and client
   state before manual recovery. On Windows capture `usbipd list` and
   `usbipd state` to distinguish a persisted bind from a present shared device.
   On Linux collect `udevadm monitor --kernel --udev --subsystem-match=usb`,
   `journalctl -k`, and the remote export listing. Record whether the same
   busid became exportable before any rebind.
4. At the client, record whether the old import stays present or a backend
   retry starts. For usbip-win2, compare an ordinary attach and `--once`.
   Use `usbip.exe attach --remote HOST --bus-id BUSID --stop` to test cancellation
   for the specific import. Use the backend detach command only if the old
   virtual port is still attached. Re-list remote exports, rebind on the host
   if needed, then attach the current busid. Record each necessary step.
5. Measure both stimulus-to-port-open and host-arrival-to-port-open. Use
   synchronized timestamps with known precision, or a single observer tracking
   both machines. Include polling interval, whether recovery was automatic,
   and time to a successful reopen with a boot log or a known serial exchange.
   When the port stays usable, record that no reattach was needed. Keep manual
   M0 recovery timings distinct from later agent measurements.

For each native USB Serial/JTAG path, stop the serial monitor and run the
ESP-IDF OpenOCD build using `openocd -f board/esp32s3-builtin.cfg -c "init; reset halt; shutdown"`.
Record detection of the built-in adapter and a halted target, then resume or
reset the board. If the serial interface works and OpenOCD cannot see JTAG,
check the separate JTAG interface driver on Windows or the OpenOCD udev
permissions on Linux. The USB UART bridge alone exposes no JTAG interface.
[ESP32-S3 JTAG configuration](https://docs.espressif.com/projects/esp-idf/en/stable/esp32s3/api-guides/jtag-debugging/configure-builtin-jtag.html)

Detach the test device from the client before unbinding it from the host.
Use `usbip.exe detach --port $UsbipPort` or `sudo usbip detach --port="$VHCI_PORT"`
on the client, then `sudo usbip unbind --busid="$BUSID"` or elevated
`usbipd unbind --busid $BusId` on the respective host. Recheck that the host can
open the physical port. If the USB/IP server was started solely for the lab,
restore its prior stopped state.

To repeat over Tailscale, verify the approved TCP 3240 path and record
`tailscale version` and `tailscale ping` to the peer. Attach using the tailnet
address or MagicDNS name and confirm the actual connection uses that path.
