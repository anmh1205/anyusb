#!/bin/sh
set -eu

if [ "$(id -u)" -ne 0 ] || [ "$#" -lt 1 ] || [ "$#" -gt 2 ]; then
    echo "Usage: sudo sh scripts/install-linux.sh PATH_TO_LINUX_BUILD [--client-only]" >&2
    exit 2
fi
bin_dir=$(cd "$1" && pwd)
script_dir=$(CDPATH='' cd -- "$(dirname -- "$0")" && pwd)
role=${2:-host}
case "$role" in
    host|--client-only) ;;
    *) echo "Unknown mode $role" >&2; exit 2 ;;
esac
for binary in anyusbd anyusb; do
    test -f "$bin_dir/$binary" || { echo "Missing $bin_dir/$binary" >&2; exit 2; }
done
if systemctl is-active --quiet anyusbd; then
    echo "Close active jobs and stop anyusbd before replacing it." >&2
    exit 1
fi

echo "Install USB/IP tools for this kernel, esptool 5.2.0, and the AnyUSB systemd agent."
echo "USB/IP modules will be loaded now and at boot. The peer agent listens on TCP 47443."
if [ "$role" = host ]; then
    echo "Install the stock USB/IP backend with systemd loopback IP restrictions on TCP 3240."
    if ss -ltn 'sport = :3240' | sed '1d' | grep -q .; then
        echo "TCP 3240 is already in use. Stop the existing backend or install --client-only." >&2
        exit 1
    fi
fi
printf 'Type INSTALL to continue: '
read -r consent
[ "$consent" = INSTALL ] || exit 1

. /etc/os-release
case "${ID:-}" in
    debian) apt-get install usbip python3-venv ;;
    ubuntu)
        apt-get install linux-tools-common "linux-tools-$(uname -r)" python3-venv
        ;;
    *) echo "This installer supports Debian and Ubuntu." >&2; exit 2 ;;
esac
command -v usbip >/dev/null
if [ "$role" = host ]; then command -v usbipd >/dev/null; fi
if [ "$role" = host ]; then
    modprobe usbip-host
    printf '%s\n' usbip-host vhci-hcd > /etc/modules-load.d/anyusb.conf
else
    printf '%s\n' vhci-hcd > /etc/modules-load.d/anyusb.conf
fi
modprobe vhci-hcd

install -d -m 0755 /opt/anyusb
python3 -m venv /opt/anyusb/esp
/opt/anyusb/esp/bin/python -m pip install -r "$script_dir/esptool-requirements.txt"
install -m 0755 "$bin_dir/anyusbd" "$bin_dir/anyusb" /usr/local/bin/
install -m 0644 "$script_dir/systemd/anyusbd.service" /etc/systemd/system/
if [ ! -e /etc/default/anyusbd ]; then
    printf '%s\n' 'ANYUSB_AGENT_ARGS="-python /opt/anyusb/esp/bin/python"' > /etc/default/anyusbd
fi
if [ "$role" = host ]; then
    install -m 0644 "$script_dir/systemd/anyusb-usbipd.service" /etc/systemd/system/
fi
systemctl daemon-reload
if [ "$role" = host ]; then systemctl enable --now anyusb-usbipd; fi
systemctl enable --now anyusbd
echo "Run sudo anyusb doctor. Verify TCP 3240 is blocked from other machines before using managed shares."
