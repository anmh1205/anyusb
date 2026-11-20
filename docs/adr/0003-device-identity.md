# 0003. Physical port identity and device metadata

**Proposed at M0.**

Assign an opaque device ID to an approved USB device record on the host. Anchor
the record to the host identity and normalized physical port path, including the
controller and hub chain. Keep VID, PID, and USB serial as descriptors and
matching evidence for each incarnation. This implements the port-first tuple
while allowing expected descriptor changes to keep the same record and lease.

Use the Linux sysfs USB device ancestry and Windows PnP location paths of the
parent USB device. Treat a composite USB Serial/JTAG device as one lease target.
Keep its serial and JTAG interfaces as child endpoints. Resolve the backend
`busid`, OS instance ID, virtual port, and COM/tty names again after arrival.
`usbipd-win` persists a bind by Windows instance ID, so check the bind again
when a device changes its reported identity. [Windows registry implementation](https://github.com/dorssel/usbipd-win/blob/v5.3.0/Usbipd/UsbipdRegistry.cs)

Accept a same-port reappearance with matching descriptors and serial if
available. Permit known ROM/runtime variants after explicit approval or a board
profile defines the expected transition. Hold a conflicting serial or unknown
variant for operator confirmation before any automatic export. Hardware with
no unique serial can be indistinguishable from an identical replacement in the
same physical port. Physical port moves require a new match or an explicit
remapping. Firmware identity fields and port topology provide inventory evidence,
not cryptographic identity.

ESP32-S3 USB Serial/JTAG is a fixed-function controller. Runtime TinyUSB on
USB-OTG is a separate function, and its CDC behavior depends on firmware. Test
these as distinct modes where supported by the board. [ESP32-S3 USB Serial/JTAG](https://docs.espressif.com/projects/esp-idf/en/stable/esp32s3/api-guides/usb-serial-jtag-console.html)
