"""Assemble a disposable Linux initramfs from pre-downloaded, trusted packages."""

import argparse
import gzip
from pathlib import Path, PurePosixPath
import stat
import tarfile
import tempfile


PACKAGES = (
    "busybox", "musl", "linux-tools-usbip", "eudev-libs", "hwdata-usb", "kmod",
    "libcrypto3", "xz-libs", "zlib", "zstd-libs", "iproute2-tc", "iproute2-ss",
    "libcap2", "libelf", "libmnl", "libxtables",
)
MODULE_PREFIXES = (
    "kernel/drivers/usb/gadget/", "kernel/drivers/usb/class/", "kernel/drivers/usb/usbip/",
    "kernel/drivers/usb/core/", "kernel/drivers/usb/common/", "kernel/drivers/usb/storage/",
    "kernel/drivers/hid/", "kernel/drivers/scsi/", "kernel/fs/configfs/",
    "kernel/drivers/net/ethernet/intel/e1000/", "kernel/net/sched/sch_netem.",
    "kernel/net/ipv4/inet_diag.", "kernel/net/ipv4/tcp_diag.", "modules.",
)


def extract_deb(deb, destination):
    with deb.open("rb") as source:
        if source.read(8) != b"!<arch>\n":
            raise ValueError("expected a Debian ar archive")
        while header := source.read(60):
            name, size = header[:16].decode().strip(), int(header[48:58])
            if name.startswith("data.tar."):
                with destination.open("wb") as output:
                    for length in range(0, size, 1 << 20):
                        output.write(source.read(min(1 << 20, size - length)))
                return
            source.seek(size + size % 2, 1)
    raise ValueError("kernel payload missing")


class Initramfs:
    def __init__(self):
        self.entries = {}

    def add(self, path, mode, data=b"", device=(0, 0)):
        path = PurePosixPath(path.removeprefix("./").lstrip("/"))
        for parent in path.parents:
            if parent != PurePosixPath("."):
                self.entries.setdefault(parent.as_posix(), (stat.S_IFDIR | 0o755, b"", (0, 0)))
        self.entries[path.as_posix()] = mode, data, device

    def add_package(self, package):
        with tarfile.open(package, ignore_zeros=True) as archive:
            for member in archive:
                if member.name.removeprefix("./").startswith("."):
                    continue
                if member.isdir():
                    self.add(member.name, stat.S_IFDIR | member.mode)
                elif member.issym():
                    self.add(member.name, stat.S_IFLNK | 0o777, member.linkname.encode())
                elif member.isfile() or member.islnk():
                    self.add(member.name, stat.S_IFREG | member.mode, archive.extractfile(member).read())

    def add_kernel(self, deb, output):
        with tempfile.TemporaryDirectory() as temp:
            payload = Path(temp, "data.tar")
            extract_deb(deb, payload)
            with tarfile.open(payload, mode="r|*") as archive:
                for member in archive:
                    if not member.isfile():
                        continue
                    path = member.name.removeprefix("./").removeprefix("usr/")
                    if path.startswith("boot/vmlinuz-"):
                        (output / "vmlinuz").write_bytes(archive.extractfile(member).read())
                    elif path.startswith("lib/modules/") and path.split("/", 3)[-1].startswith(MODULE_PREFIXES):
                        self.add(path, stat.S_IFREG | 0o644, archive.extractfile(member).read())

    def write(self, path):
        entries = sorted(self.entries.items()) + [("TRAILER!!!", (0, b"", (0, 0)))]
        with gzip.open(path, "wb", compresslevel=1) as output:
            for ino, (name, (mode, data, device)) in enumerate(entries, 1):
                name = name.encode() + b"\0"
                fields = (ino, mode, 0, 0, 1, 0, len(data), 0, 0, *device, len(name), 0)
                header = b"070701" + "".join(f"{field:08x}" for field in fields).encode()
                output.write(header + name)
                output.write(bytes(-(len(header) + len(name)) % 4))
                output.write(data)
                output.write(bytes(-len(data) % 4))


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    for name in ("debian-kernel", "alpine-packages", "binaries", "ttycheck", "output"):
        parser.add_argument("--" + name, type=Path, required=True)
    args = parser.parse_args()
    args.output.mkdir(parents=True, exist_ok=True)
    ram = Initramfs()
    for name in PACKAGES:
        packages = list(args.alpine_packages.glob(f"{name}-[0-9]*.apk"))
        if len(packages) != 1:
            parser.error(f"expected one {name} APK in {args.alpine_packages}")
        ram.add_package(packages[0])
    ram.add_kernel(args.debian_kernel, args.output)
    for name in ("anyusb", "anyusbd"):
        ram.add("lab/" + name, stat.S_IFREG | 0o755, (args.binaries / name).read_bytes())
    ram.add("lab/ttycheck", stat.S_IFREG | 0o755, args.ttycheck.read_bytes())
    ram.add("init", stat.S_IFREG | 0o755, Path(__file__).with_name("net-init.sh").read_bytes().replace(b"\r\n", b"\n"))
    for directory in ("dev", "sys", "proc", "tmp", "run"):
        ram.add(directory, stat.S_IFDIR | 0o755)
    ram.add("dev/console", stat.S_IFCHR | 0o600, device=(5, 1))
    ram.add("var/run", stat.S_IFLNK | 0o777, b"/run")
    ram.add("etc/machine-id", stat.S_IFREG | 0o644, b"55c0a8d65fa340aab001a0b989701e27\n")
    ram.write(args.output / "net-initramfs.gz")


if __name__ == "__main__":
    main()
