#!/bin/busybox sh
set -eu
export PATH=/bin:/sbin:/usr/bin:/usr/sbin
/bin/busybox mkdir -p /bin /sbin /usr/bin /usr/sbin
/bin/busybox --install -s
mount -t devtmpfs devtmpfs /dev
mount -t proc proc /proc
mount -t sysfs sysfs /sys

case " $(cat /proc/cmdline) " in
    *' anyusb.lab=host '*) address=10.67.0.1 ;;
    *' anyusb.lab=client '*) address=10.67.0.2 ;;
    *) echo 'Expected a role on an isolated lab VM'; poweroff -f; exit 2 ;;
esac

ln -sf /bin/kmod /sbin/depmod
ln -sf /bin/kmod /sbin/modprobe
depmod -a
for driver in e1000 sch_netem inet_diag tcp_diag dummy_hcd libcomposite usb_f_acm usb_f_hid usb_f_mass_storage cdc_acm usbhid hid_generic usb_storage sd_mod usbip_host vhci_hcd; do
    modprobe "$driver"
done
mount -t configfs configfs /sys/kernel/config
ip link set lo up
ip addr add "$address/24" dev eth0
ip link set eth0 up
uname -r
usbip version
tc -Version
stty -F /dev/ttyS1 raw -echo
echo 'AnyUSB lab control ready'
exec sh -s </dev/ttyS1 >/dev/ttyS1 2>&1
