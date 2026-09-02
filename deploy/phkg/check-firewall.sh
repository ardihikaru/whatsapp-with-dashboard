#!/bin/sh
# Bukti bahwa aturannya benar-benar DILEWATI paket, bukan sekadar terpasang.
#
#   sudo sh check-firewall.sh
#
# Yang penting dibaca adalah kolom pkts. Kalau baris RETURN untuk jaringan Anda
# tetap 0 setelah Anda membuka dashboard, berarti lalu lintasnya tidak melewati
# DOCKER-USER sama sekali — dan aturan DROP di bawahnya pun tidak menjaga apa
# pun. Aturan yang terpasang tapi tidak pernah dilewati adalah keamanan palsu.
set -eu
echo "=== DOCKER-USER, dengan penghitung paket ==="
iptables -L DOCKER-USER -v -n --line-numbers
