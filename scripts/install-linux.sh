#!/bin/sh
set -eu

if [ "$(id -u)" -ne 0 ] || [ "$#" -lt 1 ]; then
    echo "Usage: sudo sh scripts/install-linux.sh PATH_TO_LINUX_BUILD [--client-only] [--upgrade]" >&2
    exit 2
fi
bin_dir=$(cd "$1" && pwd)
shift
script_dir=$(CDPATH='' cd -- "$(dirname -- "$0")" && pwd)
role=host
upgrade=0
for arg in "$@"; do
    case "$arg" in
        --client-only) role=--client-only ;;
        --upgrade) upgrade=1 ;;
        *) echo "Unknown option $arg" >&2; exit 2 ;;
    esac
done
for binary in anyusbd anyusb; do
    test -f "$bin_dir/$binary" || { echo "Missing $bin_dir/$binary" >&2; exit 2; }
done
if systemctl is-active --quiet anyusbd && [ "$upgrade" -ne 1 ]; then
    echo "anyusbd is already running. Close active jobs and pass --upgrade to replace it." >&2
    exit 1
fi

echo "Install USB/IP tools for this kernel and the AnyUSB systemd agent."
echo "USB/IP modules will be loaded now and at boot. The peer agent listens on TCP 47443."
if [ "$role" = host ]; then
    echo "Install the stock USB/IP backend with systemd loopback IP restrictions on TCP 3240."
    if ss -ltn 'sport = :3240' | sed '1d' | grep -q . && ! systemctl is-active --quiet anyusb-usbipd; then
        echo "TCP 3240 is already in use by another process. Stop it or install --client-only." >&2
        exit 1
    fi
fi
printf 'Type INSTALL to continue: '
read -r consent
[ "$consent" = INSTALL ] || exit 1

if [ "$upgrade" -eq 1 ] && systemctl is-active --quiet anyusbd; then
    systemctl stop anyusbd
fi

. /etc/os-release
case "${ID:-}" in
    debian) apt-get install -y usbip ;;
    ubuntu)
        apt-get install -y linux-tools-common "linux-tools-$(uname -r)"
        ;;
    *) echo "This installer supports Debian and Ubuntu." >&2; exit 2 ;;
esac
command -v usbip >/dev/null
if [ "$role" = host ]; then command -v usbipd >/dev/null; fi

ensure_module() {
    modprobe "$1" 2>/dev/null && return 0
    if [ "${ID:-}" = ubuntu ]; then
        echo "Module $1 missing; installing linux-modules-extra-$(uname -r)..."
        apt-get install -y "linux-modules-extra-$(uname -r)" && modprobe "$1" && return 0
    fi
    echo "Failed to load kernel module $1. Verify kernel headers/extra modules for $(uname -r)." >&2
    return 1
}

if [ "$role" = host ]; then
    ensure_module usbip-host
    printf '%s\n' usbip-host vhci-hcd > /etc/modules-load.d/anyusb.conf
else
    printf '%s\n' vhci-hcd > /etc/modules-load.d/anyusb.conf
fi
ensure_module vhci-hcd

install -m 0755 "$bin_dir/anyusbd" "$bin_dir/anyusb" /usr/local/bin/
install -m 0644 "$script_dir/systemd/anyusbd.service" /etc/systemd/system/
if [ "$role" = host ]; then
    install -m 0644 "$script_dir/systemd/anyusb-usbipd.service" /etc/systemd/system/
fi
systemctl daemon-reload
if [ "$role" = host ]; then systemctl enable --now anyusb-usbipd; fi
systemctl enable --now anyusbd

if command -v ufw >/dev/null 2>&1 && ufw status 2>/dev/null | grep -q "Status: active"; then
    if ! ufw status | grep -q "47443"; then
        echo "Note: UFW firewall is active. Allow peer access with: sudo ufw allow 47443/tcp"
    fi
fi
echo "Ready. Run: sudo anyusb doctor"
echo "Open UI URL with: sudo anyusb ui"
echo "Verify TCP 3240 is blocked from other machines before using managed shares."
