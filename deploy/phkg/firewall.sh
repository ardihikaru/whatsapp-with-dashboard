#!/bin/sh
# Batasi siapa yang boleh membuka dashboard di wa-phkg.
#
#   sudo /usr/local/sbin/wa-phkg-firewall.sh
#
# KENAPA BUKAN ufw. Port yang di-publish Docker TIDAK pernah melewati chain
# INPUT milik ufw: docker memasang DNAT di PREROUTING, lalu paketnya diteruskan
# lewat FORWARD. `ufw deny 18801` karena itu akan terlihat aktif sementara
# port-nya tetap terbuka untuk semua orang — gagal diam-diam, kelas kesalahan
# yang paling mahal. DOCKER-USER adalah satu-satunya hook yang memang
# disediakan Docker untuk penyaringan seperti ini, dan ia diperiksa SEBELUM
# aturan DOCKER sendiri.
#
# Paketnya sudah ter-DNAT saat sampai di sini, jadi yang dicocokkan adalah
# alamat dan port CONTAINER (172.18.0.0/16 port 8800), bukan 192.168.20.10:18801
# yang diketik orang. Mencocokkan seluruh subnet, bukan IP container, supaya
# aturan ini tetap benar setelah `docker compose up -d` membuat container baru
# dengan IP lain.
#
# Idempoten: dijalankan berkali-kali tidak menumpuk aturan.
set -eu

CHAIN=DOCKER-USER
NET=172.18.0.0/16          # network whatsapp-service_default
PORT=8800                  # port container di balik 192.168.20.10:18801
TAG="wa-phkg dashboard"

LAN=192.168.20.0/24        # jaringan lokal PHKG
VPN=10.212.134.0/24        # pool yang dibagikan FortiClient
DOCKERNET=$NET             # lalu lintas antar container di network yang sama

# Docker membuat chain ini sendiri, tapi jangan berasumsi urutan boot.
iptables -N "$CHAIN" 2>/dev/null || true

# Buang salinan lama milik kita — dikenali dari komentarnya, jadi aturan orang
# lain di chain yang sama tidak ikut tersapu.
while iptables -S "$CHAIN" | grep -q -- "--comment \"$TAG\""; do
    n=$(iptables -S "$CHAIN" | grep -n -- "--comment \"$TAG\"" | head -1 | cut -d: -f1)
    iptables -D "$CHAIN" $((n - 1))
done

# Disisipkan terbalik: yang terakhir disisipkan berada paling atas.
# Urutan akhir di chain: docker-net, LAN, VPN, lalu DROP untuk sisanya.
iptables -I "$CHAIN" 1 -d "$NET" -p tcp --dport "$PORT" \
    -m comment --comment "$TAG" -j DROP
iptables -I "$CHAIN" 1 -s "$VPN" -d "$NET" -p tcp --dport "$PORT" \
    -m comment --comment "$TAG" -j RETURN
iptables -I "$CHAIN" 1 -s "$LAN" -d "$NET" -p tcp --dport "$PORT" \
    -m comment --comment "$TAG" -j RETURN
# Antar container di network yang sama. Hari ini tidak ada yang memanggil
# app:8800 dari dalam, tapi tanpa baris ini aturan DROP di atas akan memutusnya
# diam-diam kalau suatu saat ada.
iptables -I "$CHAIN" 1 -s "$DOCKERNET" -d "$NET" -p tcp --dport "$PORT" \
    -m comment --comment "$TAG" -j RETURN

echo "aturan aktif di $CHAIN:"
iptables -S "$CHAIN" | grep -- "--comment \"$TAG\"" | sed 's/^/  /'
