# ivts-firmware flash over Tailscale

**2026-11-23 · Firmware `271e8c9` · AnyUSB build `292c197`**

The source was `E:\anmh1205\anti-vibe\ivts-firmware`. ESP-IDF `6.1.1` and
Xtensa GCC `15.2.0` built the `esp32s3` target. The Windows client PC ran the
AnyUSB CLI and daemon inside a QEMU Linux guest. The ESP32-S3 rev `v0.2` was
physically connected to the separate Ubuntu USB host at `1-2.2`. The host's
USB controller reported VID/PID `303a:1001` and `/dev/ttyACM0`.

The host ran Ubuntu 24.04.5 LTS and kernel `6.8.0-147-generic`. The Linux guest
ran Debian kernel `6.12.117+deb13-amd64`. Both sides used `usbip-utils 2.0`.
The USB host ran esptool `5.2.0` in its temporary virtual environment. The
firmware and logs crossed the existing Tailscale connection through AnyUSB.

## Preflight and rollback data

`get-security-info` reported Secure Boot and flash encryption disabled. A
full-chip read exceeded the initial command timeout. A subsequent read
captured the first 512 KiB, which covers every sector written by the project
manifest, including its OTA data. The test checked that the aligned end of
each firmware segment was at or below `0x80000` and revalidated SHA-256
before sending the flash request.

- Backup on Ubuntu at `/home/anmh1205/.anyusb-ivts.ZwFTdhbh/flash-before-512KiB.bin`
- Backup SHA-256 `f196730a42231778caeefc54887c1dc07620d8b9bff3415b9c915679b31ab872`

The Windows project retained its source files. The test copied the generated
manifest and images into the client guest. The copies selected `no-reset`
and `usb-reset` as bootloader entry alternatives and selected `no-reset`
after each job to keep the serial session available for verification and the
boot probe.

## Build artifacts

`idf.py build` completed from the Windows project root. ESP-IDF generated
the images with esptool `5.6.0`. The local application build identified
itself as `271e8c9` and targeted `esp32s3`. The manifest specified DIO,
80 MHz flash frequency, and a 16 MB part.

| Flash offset | Build artifact | Bytes |
| --- | --- | ---: |
| `0x00000` | `bootloader/bootloader.bin` | 20,720 |
| `0x08000` | `partition_table/partition-table.bin` | 3,072 |
| `0x0f000` | `ota_data_initial.bin` | 8,192 |
| `0x20000` | `ivts-firmware.bin` | 169,808 |

Application binary SHA-256
`66b054d2df703711561572ee44a930ef69e52060454c706681deb5d33a039de6`.

## Hardware results

The first launch selected the initramfs from the earlier serial test and
stopped before upload because it lacked the project manifest. The corrected
client image contained the firmware and manifest. The client SHA-256 for
`ivts-firmware.bin` matched the Windows build.

1. The client paired with the USB host directly over Tailscale. `anyusb flash`
   uploaded the ESP-IDF manifest and four files. Host-local esptool wrote
   201,792 bytes in four segments at a configured baud of `921600` and
   reported a matching write digest for every segment.
2. A separate `anyusb verify` job checked those offsets against the same
   files. `verify-flash` returned `Verification successful (digest matched)`
   for all four segments.
3. The client used managed USB/IP to import the board as `/dev/ttyACM0`.
   The boot probe opened the port, applied the RTS reset and received
   `I (257) main: ivts-firmware boot` from the running application.

The project firmware was left on the board. This run covered one complete
flash, independent verification, and successful boot. [Flash and boot console](ivts-firmware-tailnet-console.txt)

Cleanup stopped the temporary listeners, unloaded the test's `usbip_host`
module, and returned `/dev/ttyACM0` to Ubuntu. The backup still passed
SHA-256 verification and was readable only by its owner.
