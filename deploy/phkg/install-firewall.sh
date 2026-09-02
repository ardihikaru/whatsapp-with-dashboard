#!/bin/sh
# Pasang pembatas akses dashboard + buat ia bertahan setelah reboot.
#
#   sudo sh install-firewall.sh
#
# TIDAK menyentuh port 22: sesi ssh Anda — termasuk yang lewat FortiClient —
# tidak mungkin terputus oleh skrip ini. Yang disaring hanya port dashboard.
set -eu
HERE=$(cd "$(dirname "$0")" && pwd)

install -m 0755 "$HERE/firewall.sh"               /usr/local/sbin/wa-phkg-firewall.sh
install -m 0644 "$HERE/wa-phkg-firewall.service"  /etc/systemd/system/wa-phkg-firewall.service
systemctl daemon-reload
systemctl enable --now wa-phkg-firewall.service

echo
echo "=== unit ==="
systemctl is-enabled wa-phkg-firewall.service | sed 's/^/  enabled: /'
systemctl is-active  wa-phkg-firewall.service | sed 's/^/  active : /'
echo
echo "=== isi chain DOCKER-USER sekarang ==="
iptables -S DOCKER-USER | sed 's/^/  /'
