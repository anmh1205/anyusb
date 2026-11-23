"""Build the Windows x64 installer and Ubuntu amd64 package from verified runtimes."""

import argparse
import hashlib
import io
from pathlib import Path, PurePosixPath
import re
import shutil
import stat
import struct
import subprocess
import tarfile
import tempfile
import zipfile


ROOT = Path(__file__).resolve().parents[1]
UBUNTU = ROOT / "packaging" / "ubuntu"
ICON = ROOT / "web" / "static" / "anyusb.png"
DEPENDS = ", ".join((
    "init-system-helpers", "systemd", "kmod", "iproute2", "linux-tools-common", "linux-tools-generic",
    "pkexec", "libc6 (>= 2.34)", "libasound2t64", "libatk-bridge2.0-0t64", "libatk1.0-0t64",
    "libcairo2", "libdbus-1-3", "libdrm2", "libgbm1", "libglib2.0-0t64", "libgtk-3-0t64", "libnspr4",
    "libnss3", "libpango-1.0-0", "libx11-6", "libxcb1", "libxcomposite1", "libxdamage1", "libxext6",
    "libxfixes3", "libxkbcommon0", "libxrandr2",
))


def file_digest(path):
    with path.open("rb") as src:
        return hashlib.file_digest(src, "sha256").hexdigest()


def verify_electron(path, checksums):
    pattern = rf"^([0-9a-f]{{64}})\s+\*?{re.escape(path.name)}\s*$"
    match = re.search(pattern, checksums.read_text(encoding="utf-8-sig"), re.MULTILINE)
    if not match or match[1] != file_digest(path):
        raise ValueError(f"Electron checksums did not verify {path.name}")


def archive_path(name):
    path = PurePosixPath(name)
    if path.is_absolute() or ".." in path.parts or ":" in name or "\\" in name:
        raise ValueError(f"unexpected archive path {name}")
    return path


def package_windows(args, workspace):
    tree = workspace / "windows"
    tree.mkdir()
    with zipfile.ZipFile(args.windows_electron) as runtime:
        for member in runtime.namelist():
            archive_path(member)
        runtime.extractall(tree)
    (tree / "electron.exe").rename(tree / "AnyUSB-Desktop.exe")
    shutil.copytree(ROOT / "desktop", tree / "resources" / "app")
    binaries = ROOT / "dist" / "windows-amd64"
    for name in ("anyusbd.exe", "anyusb.exe"):
        shutil.copy2(binaries / name, tree / name)
    shutil.copy2(ROOT / "scripts" / "install-windows.ps1", tree / "install-windows.ps1")
    png = ICON.read_bytes()
    ico = struct.pack("<HHHBBBBHHII", 0, 1, 1, 64, 64, 0, 0, 1, 32, len(png), 22) + png
    (tree / "anyusb.ico").write_bytes(ico)
    setup = args.output / f"AnyUSB-{args.version}-Windows-x64.exe"
    cmd = [args.makensis, "/V2", "/INPUTCHARSET", "UTF8", f"/DOUTPUT={setup}", f"/DPAYLOAD={tree}", f"/DVERSION={args.version}",
           f"/DLICENSE={ROOT / 'LICENSE'}", ROOT / "packaging" / "windows" / "AnyUSB.nsi"]
    subprocess.run(cmd, check=True)
    return setup


def tar_bytes(tar, name, data, mode=0o644):
    entry = tarfile.TarInfo(name)
    entry.mode, entry.size = mode, len(data)
    tar.addfile(entry, io.BytesIO(data))


def tar_source(tar, name, source, mode=0o644):
    entry = tarfile.TarInfo(name)
    entry.mode, entry.size = mode, source.stat().st_size
    with source.open("rb") as data:
        tar.addfile(entry, data)


def deb_add(package, name, data=None, source=None):
    length = len(data) if source is None else source.stat().st_size
    header = f"{name + '/':<16}{0:<12}{0:<6}{0:<6}{'100644':<8}{length:<10}`\n"
    package.write(header.encode("ascii"))
    if source is None:
        package.write(data)
    else:
        with source.open("rb") as stream:
            shutil.copyfileobj(stream, package)
    if length % 2:
        package.write(b"\n")


def package_ubuntu(args, workspace):
    data = workspace / "data.tar.gz"
    control = workspace / "control.tar.gz"
    runtime_root = "opt/anyusb/desktop"
    with tarfile.open(data, "w:gz", compresslevel=3) as tar, zipfile.ZipFile(args.linux_electron) as runtime:
        for member in runtime.infolist():
            relative = archive_path(member.filename).as_posix()
            if relative == "electron":
                relative = "anyusb-desktop"
            entry = tarfile.TarInfo(runtime_root + "/" + relative)
            mode = member.external_attr >> 16
            entry.mode = (mode & 0o777) or (0o755 if member.is_dir() else 0o644)
            if member.is_dir():
                entry.type = tarfile.DIRTYPE
                tar.addfile(entry)
            elif stat.S_ISLNK(mode):
                entry.type = tarfile.SYMTYPE
                entry.linkname = runtime.read(member).decode()
                archive_path(entry.linkname)
                tar.addfile(entry)
            else:
                if relative in ("anyusb-desktop", "chrome_crashpad_handler"):
                    entry.mode = 0o755
                elif relative == "chrome-sandbox":
                    entry.mode = 0o4755
                entry.size = member.file_size
                with runtime.open(member) as src:
                    tar.addfile(entry, src)
        for source in sorted((ROOT / "desktop").iterdir()):
            if source.is_file():
                tar_source(tar, f"{runtime_root}/resources/app/{source.name}", source)
        for name in ("anyusbd", "anyusb"):
            tar_source(tar, f"usr/bin/{name}", ROOT / "dist" / "linux-amd64" / name, 0o755)
        tar_source(tar, "usr/share/applications/anyusb.desktop", UBUNTU / "anyusb.desktop")
        tar_source(tar, "usr/share/pixmaps/anyusb.png", ICON)
        tar_source(tar, "usr/share/polkit-1/actions/io.anyusb.policy", UBUNTU / "anyusb.policy")
        tar_bytes(tar, "usr/lib/anyusb/open-dashboard", (UBUNTU / "open-dashboard").read_text().encode(), 0o755)
        tar_source(tar, "usr/share/doc/anyusb/copyright", ROOT / "LICENSE")
        tar_bytes(tar, "usr/lib/modules-load.d/anyusb.conf", b"usbip-host\nvhci-hcd\n")
        units = ROOT / "scripts" / "systemd"
        agent = (units / "anyusbd.service").read_text().replace("/usr/local/bin/anyusbd ", "/usr/bin/anyusbd -usb-only ")
        tar_bytes(tar, "lib/systemd/system/anyusbd.service", agent.encode())
        tar_source(tar, "lib/systemd/system/anyusb-usbipd.service", units / "anyusb-usbipd.service")
    with tarfile.open(control, "w:gz") as tar:
        fields = f"""Package: anyusb
Version: {args.version}
Section: utils
Priority: optional
Architecture: amd64
Maintainer: AnyUSB <anyusb@localhost>
Depends: {DEPENDS}
Description: AnyUSB desktop for USB over LAN and Tailscale
 Use a remote USB device through the local operating system and applications.
"""
        tar_bytes(tar, "control", fields.encode())
        for name in ("postinst", "prerm", "postrm"):
            tar_bytes(tar, name, (UBUNTU / name).read_text().encode(), 0o755)
    path = args.output / f"anyusb_{args.version}_amd64.deb"
    with path.open("wb") as package:
        package.write(b"!<arch>\n")
        deb_add(package, "debian-binary", b"2.0\n")
        deb_add(package, "control.tar.gz", source=control)
        deb_add(package, "data.tar.gz", source=data)
    return path


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    for name in ("checksums", "windows-electron", "linux-electron", "makensis"):
        parser.add_argument("--" + name, type=lambda v: Path(v).resolve(), required=True)
    parser.add_argument("--output", type=lambda v: Path(v).resolve(), default=ROOT / "dist" / "desktop")
    parser.add_argument("--version", default="0.1.1")
    args = parser.parse_args()
    if not re.fullmatch(r"[0-9]+\.[0-9]+\.[0-9]+", args.version):
        parser.error("version must be major.minor.patch")
    args.output.mkdir(parents=True, exist_ok=True)
    verify_electron(args.windows_electron, args.checksums)
    verify_electron(args.linux_electron, args.checksums)
    with tempfile.TemporaryDirectory(prefix="anyusb-build-") as temporary:
        workspace = Path(temporary)
        windows = package_windows(args, workspace)
        ubuntu = package_ubuntu(args, workspace)
    for package in (windows, ubuntu):
        print(f"{file_digest(package)}  {package}", flush=True)


if __name__ == "__main__":
    main()
