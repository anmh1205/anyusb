"""Exercise AnyUSB between two disposable QEMU Linux kernels on an isolated link."""

import argparse
from contextlib import ExitStack
import itertools
import json
import os
from pathlib import Path
import re
import shlex
import socket
import subprocess
import time


GADGET = "/sys/kernel/config/usb_gadget/anyusb-lab"
HOST = "10.67.0.1"
CLIENT = "10.67.0.2"
PEER = HOST + ":47443"
CONTROL = "/run/lab/anyusb"


def log(message):
    print(message, flush=True)


def require(condition, message):
    if not condition:
        raise RuntimeError(message)


def eventually(action, timeout=20):
    deadline = time.monotonic() + timeout
    while True:
        try:
            return action()
        except (RuntimeError, json.JSONDecodeError) as error:
            if time.monotonic() >= deadline:
                raise RuntimeError(f"timed out after {timeout}s: {error}") from error
        time.sleep(0.35)


def local_ports(kind):
    with ExitStack() as stack:
        sockets = [stack.enter_context(socket.socket(socket.AF_INET, kind)) for _ in range(2)]
        for sock in sockets:
            sock.bind(("127.0.0.1", 0))
        return [sock.getsockname()[1] for sock in sockets]


class Guest:
    def __init__(self, args, role, mac, local, remote, control):
        self.role = role
        self.control = control
        self.boot = args.logs / f"{role}-boot.log"
        self.sequence = itertools.count(1)
        self.sock = self.lines = None
        command = [str(args.qemu), "-machine", "q35,accel=tcg", "-cpu", "max",
                   "-m", "512", "-smp", "1", "-display", "none", "-monitor", "none", "-no-reboot",
                   "-device", f"e1000,netdev=lab,mac={mac}", "-netdev",
                   f"socket,id=lab,localaddr=127.0.0.1:{local},udp=127.0.0.1:{remote}",
                   "-serial", "file:" + str(self.boot), "-serial",
                   f"tcp:127.0.0.1:{control},server=on,wait=off,nodelay=on",
                   "-kernel", str(args.image / "vmlinuz"), "-initrd", str(args.image / "net-initramfs.gz"),
                   "-append", f"console=ttyS0 rdinit=/init loglevel=4 panic=1 anyusb.lab={role}"]
        with (args.logs / f"{role}-qemu.log").open("wb") as errors:
            self.process = subprocess.Popen(command, stdin=subprocess.DEVNULL, stdout=errors, stderr=errors,
                                            creationflags=subprocess.CREATE_NO_WINDOW if os.name == "nt" else 0)

    def ready(self):
        def booted():
            require(self.process.poll() is None, f"{self.role} QEMU exited")
            require(self.boot.exists(), f"waiting for {self.role} console")
            output = self.boot.read_text(errors="replace")
            require("AnyUSB lab control ready" in output, f"{self.role} console: {output[-900:]}")
            return output

        log(f"{self.role} boot:\n{eventually(booted, 75).strip()}")
        self.sock = socket.create_connection(("127.0.0.1", self.control), timeout=5)
        self.lines = self.sock.makefile("rb")

    def close(self):
        if self.lines is not None:
            self.lines.close()
            self.sock.close()
        if self.process.poll() is None:
            self.process.terminate()
            self.process.wait(timeout=10)

    def run(self, command, timeout=35, allowed=(0,)):
        index = next(self.sequence)
        start, end = f"__LAB_BEGIN_{index}__", f"__LAB_END_{index}__:"
        request = f"printf '\\n{start}\\n'; {{\n{command}\n}}; rc=$?; printf '\\n{end}%s\\n' \"$rc\"\n"
        self.sock.settimeout(timeout)
        self.sock.sendall(request.encode())
        output, collecting = [], False
        while True:
            raw = self.lines.readline()
            require(bool(raw), f"{self.role} console closed")
            line = raw.decode(errors="replace").rstrip("\r\n")
            if line.startswith(end):
                break
            if line == start:
                collecting = True
            elif collecting:
                output.append(line)
        output = "\n".join(output).strip()
        status = int(line.removeprefix(end))
        require(status in allowed, f"{self.role} command exited {status}: {output}")
        return output

    def cli(self, *arguments):
        result = self.run(shlex.join(("/lab/anyusb", "-socket", CONTROL, "-json", *arguments)))
        return json.loads(result)

    def start_agent(self, address):
        self.run(f"mkdir -m 0700 /run/lab && /lab/anyusbd -socket {CONTROL} -data /run/anyusb-data -name {self.role} "
                 f"-peer-listen {address}:47443 -advertise {address}:47443 -mdns=false "
                 "-ui-listen '' </dev/null >/tmp/agent.log 2>&1 &")
        eventually(lambda: self.cli("list"))


class Matrix:
    profiles = {
        "clean": "delay 0ms",
        "LAN": "delay 4ms 1ms",
        "WAN": "delay 40ms 12ms loss 1% rate 10mbit",
        "mobile": "delay 90ms 25ms loss 3% rate 3mbit",
        "severe": "delay 200ms 60ms loss 5% rate 1mbit",
        "asymmetric": ("delay 35ms 10ms loss 1% rate 10mbit", "delay 120ms 45ms loss 4% rate 768kbit"),
        "blackhole": "loss 100%",
    }

    def __init__(self, host, client):
        self.host, self.client = host, client
        self.transfers = itertools.count(1)
        self.connection = None
        self.failures = []

    def profile(self, name):
        rule = self.profiles[name]
        rules = (rule, rule) if isinstance(rule, str) else rule
        for guest, direction in zip((self.host, self.client), rules):
            guest.run("tc qdisc replace dev eth0 root netem " + direction)
        label = f"each direction: {rule}" if isinstance(rule, str) else f"host: {rules[0]}; client: {rules[1]}"
        log(f"link profile {name}, {label}")

    def drops(self):
        return [int(re.search(r"\(dropped (\d+)", guest.run("tc -s qdisc show dev eth0"))[1])
                for guest in (self.host, self.client)]

    def prepare(self):
        self.host.run(fr"""(set -e
g={GADGET}
mkdir "$g"
echo 0x1209 >"$g/idVendor"
echo 0xa11b >"$g/idProduct"
mkdir "$g/strings/0x409" "$g/configs/c.1" "$g/configs/c.1/strings/0x409"
echo anyusb-composite-lab >"$g/strings/0x409/serialnumber"
echo 'AnyUSB disposable CDC + HID + storage' >"$g/configs/c.1/strings/0x409/configuration"
mkdir "$g/functions/acm.usb0" "$g/functions/hid.usb0" "$g/functions/mass_storage.usb0"
echo 0 >"$g/functions/hid.usb0/protocol"
echo 0 >"$g/functions/hid.usb0/subclass"
echo 8 >"$g/functions/hid.usb0/report_length"
printf '\006\000\377\011\001\241\001\025\000\046\377\000\165\010\225\010\011\001\201\002\225\010\011\001\221\002\300' >"$g/functions/hid.usb0/report_desc"
dd if=/dev/zero of=/tmp/anyusb-mass.img bs=1M count=8 status=none
echo 1 >"$g/functions/mass_storage.usb0/lun.0/removable"
echo /tmp/anyusb-mass.img >"$g/functions/mass_storage.usb0/lun.0/file"
ln -s "$g/functions/acm.usb0" "$g/configs/c.1/acm.usb0"
ln -s "$g/functions/hid.usb0" "$g/configs/c.1/hid.usb0"
ln -s "$g/functions/mass_storage.usb0" "$g/configs/c.1/mass_storage.usb0"
ls /sys/class/udc | head -n 1 >"$g/UDC"
usbipd -4 -t 3240 </dev/null >/tmp/usbipd.log 2>&1 &
/lab/ttycheck echo /dev/ttyGS0 </dev/null >/tmp/echo.log 2>&1 &
)""")
        self.host.start_agent(HOST)
        self.client.start_agent(CLIENT)
        self.device = eventually(self.present)
        self.id = self.device["id"]
        profiles = set(self.device.get("profiles", []))
        require({"serial", "hid", "storage"}.issubset(profiles), f"composite profiles: {self.device}")
        log(f"composite CDC/HID/storage: {self.device['bus_id']} {self.id}, {self.device['incarnation']}")

    def present(self):
        devices = [d for d in self.host.cli("list") if (d.get("vendor_id"), d.get("product_id")) == ("1209", "a11b")]
        require(len(devices) == 1, f"expected one CDC gadget, found {devices}")
        return devices[0]

    def tty(self):
        imports = self.client.cli("list", "--imports")
        require(len(imports) == 1 and len(imports[0].get("serial_ports", [])) == 1, f"client imports: {imports}")
        kinds = {binding.get("kind") for binding in imports[0].get("local_bindings", [])}
        require({"serial", "hid", "storage"}.issubset(kinds), f"composite client bindings: {imports}")
        require(imports[0].get("readiness") in ("ready", "degraded"), f"composite readiness: {imports}")
        return imports[0]["serial_ports"][0]

    def block(self):
        imports = self.client.cli("list", "--imports")
        nodes = [binding["id"] for binding in imports[0].get("local_bindings", [])
                 if binding.get("kind") == "storage" and binding.get("status") == "ready"] if len(imports) == 1 else []
        require(len(nodes) == 1, f"expected one disposable block binding: {imports}")
        return nodes[0]

    def storage_hash(self):
        node = shlex.quote(self.block())
        return self.client.run(f"dd if={node} bs=64K count=4 2>/dev/null | sha256sum | cut -d' ' -f1", timeout=90)

    def write_storage_fixture(self):
        node = shlex.quote(self.block())
        self.client.run(f"dd if=/dev/urandom of={node} bs=64K count=4 2>/dev/null && sync", timeout=90)
        digest = self.storage_hash()
        require(re.fullmatch(r"[0-9a-f]{64}", digest) is not None, f"invalid storage hash: {digest}")
        log(f"disposable storage wrote, synced and read 256 KiB, sha256={digest}")
        return digest

    def verify_storage_fixture(self):
        digest = self.storage_hash()
        require(digest == self.storage_digest, f"disposable storage hash changed: {digest} != {self.storage_digest}")
        log(f"disposable storage hash retained: {digest}")

    def attached(self):
        links = self.client.cli("list", "--connections")
        require(len(links) == 1 and links[0]["id"] == self.connection and links[0]["state"] == "attached",
                f"connection state: {links}")
        return self.tty()

    def lost(self):
        links = self.client.cli("list", "--connections")
        require(len(links) == 1 and links[0]["id"] == self.connection and links[0]["state"] != "attached",
                f"expected the retained connection to lose its import: {links}")
        return links[0]["state"]

    def clear(self):
        imports = self.client.cli("list", "--imports")
        links = self.client.cli("list", "--connections")
        shared = self.host.cli("list", "--shared")
        require(not imports and not links and len(shared) == 1 and not shared[0].get("lease_owner")
                and not shared[0].get("exported", False), f"cleanup: {imports=}, {links=}, {shared=}")

    def connect(self):
        started = time.monotonic()
        self.connection = self.client.cli("connect", "--peer", self.peer, self.id, "--confirm-risk")["id"]
        tty = eventually(self.attached, 45)
        self.verify_storage_fixture()
        log(f"managed attach: {tty} after {elapsed(started)} ms")
        return tty

    def disconnect(self):
        if self.connection is not None:
            self.client.cli("disconnect", "--connection", self.connection)
            eventually(self.clear)
            self.connection = None

    def transfer(self, tty, size):
        index = next(self.transfers)
        command = shlex.join(("/lab/ttycheck", "check", tty, str(size), str(index), "28s"))
        log(self.client.run(command))

    def raw(self):
        self.profile("clean")
        self.host.cli("share", self.id)
        bus = self.device["bus_id"]
        try:
            listed = self.client.cli("list", "--remote", HOST)
            require(len(listed) == 1 and listed[0]["bus_id"] == bus, f"remote list: {listed}")
            self.client.cli("connect", HOST, bus)
            tty = eventually(self.tty)
            self.storage_digest = self.write_storage_fixture()
            self.transfer(tty, 32768)
        finally:
            for imported in self.client.cli("list", "--imports"):
                self.client.cli("disconnect", str(imported["port"]))
            eventually(lambda: self.host.cli("unshare", self.id))
        require(not self.client.cli("list", "--imports"), "raw detach left an import")

    def pair(self):
        self.profile("WAN")
        self.host.cli("share", "--managed", self.id)
        code = self.host.cli("pair", "invite")["code"]
        self.peer = self.client.cli("pair", PEER, code)["id"]

    def profile_run(self, name):
        self.profile(name)
        before = self.drops()
        log(self.client.run(f"ping -q -c 12 -i 0.2 -W 2 {HOST}", allowed=(0, 1)))
        try:
            self.transfer(self.connect(), 65536)
        finally:
            host, client = (end - start for start, end in zip(before, self.drops()))
            log(f"egress packet drops during profile: host {host}, client {client}")
            self.profile("clean")
            self.disconnect()

    def replug(self):
        self.host.run(f"printf '\\n' >{GADGET}/UDC")
        eventually(self.lost)
        started = time.monotonic()
        self.host.run(f"ls /sys/class/udc | head -n 1 >{GADGET}/UDC")
        self.transfer(eventually(self.attached, 30), 4096)
        self.verify_storage_fixture()
        device = self.present()
        require(device["id"] == self.id and device["incarnation"] != self.device["incarnation"],
                f"unexpected USB incarnation: {device}")
        self.device = device
        log(f"USB replug recovery: {elapsed(started)} ms to verified bytes")

    def short_outage(self):
        tty = self.attached()
        incarnation = self.import_incarnation()
        self.client.run(f"(/lab/ttycheck check {tty} 262144 990 45s; echo $? >/tmp/inflight.rc) "
                        "</dev/null >/tmp/inflight.log 2>&1 &")
        eventually(lambda: self.client.run("grep 'received initial 4096 bytes' /tmp/inflight.log"), 15)
        self.client.run("test ! -e /tmp/inflight.rc")
        self.profile("blackhole")
        try:
            time.sleep(3)
            self.client.run("test ! -e /tmp/inflight.rc")
        finally:
            started = time.monotonic()
            self.profile("mobile")
        status = eventually(lambda: self.client.run("cat /tmp/inflight.rc"), 45)
        output = self.client.run("cat /tmp/inflight.log")
        require(status == "0", f"in-flight transfer failed after short outage: {output}")
        completion = elapsed(started)
        time.sleep(1)
        self.attached()
        require(self.import_incarnation() == incarnation, "short outage replaced the original USB import")
        self.verify_storage_fixture()
        log(f"short outage: {output}; completed {completion} ms after restoration on the original import")

    def partition(self):
        self.profile("blackhole")
        time.sleep(12)
        log(f"partition state before restoration: {self.lost()}")
        started = time.monotonic()
        self.profile("mobile")
        self.transfer(eventually(self.attached, 30), 4096)
        self.verify_storage_fixture()
        log(f"partition recovery: {elapsed(started)} ms to verified bytes")

    def reset_tcp(self):
        self.attached()
        old = self.import_incarnation()
        started = time.monotonic()
        sockets = self.client.run(f"ss -K -nt dst {HOST} dport = :47443")
        require("ESTAB" in sockets, f"no TCP socket was reset: {sockets}")
        log(sockets)
        def replaced():
            tty = self.attached()
            require(self.import_incarnation() != old, "waiting for a fresh USB import")
            return tty

        self.transfer(eventually(replaced, 30), 4096)
        self.verify_storage_fixture()
        log(f"TCP reset recovery: {elapsed(started)} ms to verified bytes")

    def import_incarnation(self):
        self.tty()
        bus = self.client.cli("list", "--imports")[0]["local_bus_id"]
        return bus, self.client.run("cat " + shlex.quote(f"/sys/bus/usb/devices/{bus}/devnum"))

    def expiry(self):
        old = self.connection
        self.profile("blackhole")
        time.sleep(40)
        def expired():
            imports = self.client.cli("list", "--imports")
            links = self.client.cli("list", "--connections")
            shared = self.host.cli("list", "--shared")
            require(not imports and len(links) == 1 and links[0]["id"] == old and links[0]["state"] == "reconnecting"
                    and len(shared) == 1 and not shared[0].get("lease_owner") and not shared[0].get("exported", False),
                    f"expiry cleanup: {imports=}, {links=}, {shared=}")
        eventually(expired, 10)
        log("40 s partition: remote lease, host export and native import cleaned up; connection intent kept retrying")
        self.profile("mobile")
        self.transfer(eventually(self.attached, 30), 4096)
        self.verify_storage_fixture()
        require(old == self.connection, "automatic recovery replaced the saved connection intent")
        log("automatic recovery after expiry obtained a new remote lease without operator input")

    def case(self, name, action):
        log(f"CASE {name}")
        try:
            action()
            log(f"PASS {name}")
        except (RuntimeError, json.JSONDecodeError) as error:
            self.failures.append(name)
            log(f"FAIL {name}: {error}")

    def run(self, smoke):
        self.prepare()
        self.case("stock USB/IP across two kernels", self.raw)
        self.pair()
        profiles = ("WAN",) if smoke else ("clean", "LAN", "WAN", "mobile", "severe", "asymmetric")
        for name in profiles:
            self.case(f"managed transfer on {name}", lambda: self.profile_run(name))
        if not smoke:
            self.profile("mobile")
            self.connect()
            for cycle in range(1, 4):
                self.case(f"USB replug on mobile #{cycle}", self.replug)
            self.case("three-second blackhole during transfer", self.short_outage)
            self.case("twelve-second blackhole with reattach", self.partition)
            self.case("TCP connection reset", self.reset_tcp)
            self.case("forty-second blackhole and lease expiry", self.expiry)
            self.profile("clean")
            self.disconnect()
        self.host.cli("unshare", "--managed", self.id)
        require(not self.failures, f"failed cases: {self.failures}")


def elapsed(started):
    return round(1000 * (time.monotonic() - started))


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    for name in ("qemu", "image", "logs"):
        parser.add_argument("--" + name, type=Path, required=True)
    parser.add_argument("--smoke", action="store_true")
    args = parser.parse_args()
    args.logs.mkdir(parents=True, exist_ok=True)
    udp, controls = local_ports(socket.SOCK_DGRAM), local_ports(socket.SOCK_STREAM)
    with ExitStack() as stack:
        host = Guest(args, "host", "52:54:00:67:00:01", udp[0], udp[1], controls[0])
        stack.callback(host.close)
        client = Guest(args, "client", "52:54:00:67:00:02", udp[1], udp[0], controls[1])
        stack.callback(client.close)
        host.ready()
        client.ready()
        try:
            Matrix(host, client).run(args.smoke)
        except Exception:
            for guest in (host, client):
                try:
                    log(f"{guest.role} diagnostics:\n" + guest.run(
                        "tc -s qdisc show dev eth0; tail -20 /tmp/agent.log; dmesg | tail -20", timeout=5))
                except (OSError, RuntimeError) as error:
                    log(f"{guest.role} diagnostics unavailable: {error}")
            raise
    log("AnyUSB NETWORK LAB RESULT=0")


if __name__ == "__main__":
    main()
