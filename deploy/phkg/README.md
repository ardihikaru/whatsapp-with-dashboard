# Deploy whatsapp-with-dashboard di `wa-phkg`

Bundle deployment untuk `sim@192.168.20.10:/home/sim/devel/projects/whatsapp-service`.
**Tidak ada source code di host ini** — yang berjalan adalah image yang persis
sama dengan produksi, dipindahkan sebagai `tar.gz`. Tidak ada `git clone`,
tidak ada build, tidak ada `migrations/*.sql` yang perlu disalin (file SQL-nya
sudah tertanam di image `migrate`).

Host ini juga tidak punya `make`, `go`, `mysql`, maupun `nginx` — semua
langkah di bawah hanya butuh `docker`, `docker compose`, dan `/bin/sh`.

## Isi bundle

| Berkas | Fungsi |
|---|---|
| `docker-compose.yml` | Satu file datar, tanpa `build:`, tanpa network external. |
| `.env.template` | Semua variabel yang dibaca app; baris `ISI` sengaja kosong. |
| `load-images.sh` | Verifikasi checksum → `docker load` → alias tag bertanggal. |
| `bootstrap-db.sql.tmpl` | Satu schema + satu user least-privilege. Dipakai dua kali. |
| `images/` | `wsm-app-20260901.tar.gz`, `wsm-migrate-20260901.tar.gz`, `waxum-0.12.2-uploaded-media.tar.gz`, `SHA256SUMS`. |

## Urutan

### 1. Muat image — bisa dikerjakan sekarang

```sh
./load-images.sh
```

Cetakan terakhirnya harus memunculkan tiga image dengan ID yang identik dengan
produksi: `d7b40a798051` (app), `7c09964d975f` (migrate), `e7c51aa15c99`
(waxum). Kalau ID berbeda, image yang dimuat bukan build yang sedang jalan di
produksi — jangan diteruskan.

### 2. Isi `.env` — SUDAH LENGKAP 2026-09-01, DIPINDAH KE INFRA INTERNAL 2026-09-02

```sh
cp .env.template .env && chmod 600 .env
```

Lalu isi setiap baris yang ditandai `ISI` di `.env.template`. Semuanya sudah
terisi; yang tercatat di bawah tinggal riwayat kenapa masing-masing dipilih:

- ~~**MySQL** — `WSM_MYSQL_*` dan `WAXUM_MYSQL_*`.~~ Sudah diisi. Sejak
  2026-09-02 menunjuk `192.168.20.15:3306` (kredensial di
  `~/.config/waxum/mysql-internal.json`), bukan lagi `202.10.39.46:31006`.
- ~~**MinIO** — `WSM_MINIO_ENDPOINT/ACCESS_KEY/SECRET_KEY/BUCKET`.~~ Sudah
  diisi. Sejak 2026-09-02 menunjuk `http://192.168.20.12:9000` (kredensial di
  `~/.config/waxum/minio-internal.json`), bukan lagi
  `https://s3.mfardiansyah.id`. Bucket tetap `whatsapp-phkg` dan
  `whatsapp-phkg-temp`. Ini **bukan** setelan opsional: app melakukan cek
  jaringan hidup ke MinIO saat boot dan `log.Fatalf` kalau tak terjangkau atau
  kredensialnya salah. Tidak ada mode degradasi, dan `WSM_MEDIA_ARCHIVE=false`
  tidak melonggarkannya.

**Tiga penyimpangan sadar dari house rule repo ini, hasil pemindahan
2026-09-02. Semuanya keputusan, bukan kelalaian — jangan "dirapikan" tanpa
membaca alasannya.**

1. **Satu user MySQL untuk dua schema.** Di `192.168.20.15` hanya ada satu
   kredensial yang kita pegang, `wa_user`, dengan `ALL PRIVILEGES` pada persis
   `wa_dashboard` dan `waxum` plus `USAGE` global. Tidak ada root di server itu,
   jadi dua nama database itu satu-satunya yang bisa dibuat, dan
   `bootstrap-db.sql.tmpl` (satu schema + satu user least-privilege) tidak bisa
   dijalankan di sana. Bocornya `.env` menjangkau data app dan registry waxum
   sekaligus. Perbaikannya butuh root di `.15`.
2. **`WSM_MINIO_ACCESS_KEY` adalah key ADMIN MinIO**, bukan user ber-scope
   seperti sebelumnya, dan `192.168.20.12` juga memegang bucket produk lain
   (`dev-his-v3`, `hisv3-stag`, `temp`, …). Blast radius-nya seluruh server itu.
3. **`WSM_MINIO_TEMP_BUCKET` wajib tetap eksplisit.** Default
   `config.DefaultMinioTempBucket` adalah `temp` — dan `temp` di `.12` adalah
   bucket milik produk lain yang saat cutover berisi 19.784 objek. Digabung
   dengan poin 2, fallback ke default berarti menulis media temporer WhatsApp ke
   bucket orang lain memakai key yang juga bisa menghapus isinya. Dijaga oleh
   `TestPHKGEnvTemplateScopesTheMinioTempBucket`.

Dua hal lagi yang dicatat, bukan dimitigasi: drive `.12` ada di 85,1% dari
238 GiB (~35 GiB sisa) sementara archiving aktif dengan cap 100 MiB per media,
dan lalu lintas ke `.15` maupun `.12` **tanpa TLS** — kredensial dan media lewat
LAN PHKG sebagai plaintext.

`WSM_TRUSTED_PROXIES` dan `WA_PLATFORM_URL` sengaja tetap kosong: belum ada
reverse proxy di depan host ini, dan belum ada URL platform untuk ditautkan.
Keduanya opsional — kosong berarti "tidak percaya proxy mana pun" dan
"sembunyikan tautannya".

Plus kredensial yang **dibuat baru untuk PHKG, bukan disalin dari produksi**:
`WAXUM_TOKEN`, `WSM_ADMIN_USER`, `WSM_ADMIN_PASSWORD_HASH`. Hash bcrypt dibuat
di mesin yang punya repo + Go (`go run ./cmd/hashpw`, baca stdin) lalu ditempel
di sini dengan tanda kutip tunggal.

Cek parsing tanpa membocorkan apa pun:

```sh
docker compose config --quiet     # HANYA bentuk --quiet, lihat catatan di bawah
```

### 3. Buat database — SUDAH DILAKUKAN 2026-09-02 di `192.168.20.15`

`wa-phkg` tidak punya mysql client, jadi ini dijalankan dari mesin lain yang
punya, diarahkan ke `WSM_MYSQL_HOST:WSM_MYSQL_PORT` yang sama dengan yang
dipakai app.

Yang ada di `192.168.20.15:3306` sekarang: schema **`wa_dashboard`** (app) dan
**`waxum`** (registry waxum) — tanpa sufiks `_phkg`, dan **keduanya diakses
satu user yang sama, `wa_user`**. `bootstrap-db.sql.tmpl` TIDAK dipakai di
server ini: templat itu butuh root untuk `CREATE USER`/`GRANT`, dan di `.15`
kita hanya memegang `wa_user`. Grant-nya sudah ada sejak awal
(`ALL PRIVILEGES ON wa_dashboard.*` dan `ON waxum.*`), dan grant level-database
sudah memuat `CREATE`, jadi `wa_user` bisa membuat kedua database itu sendiri —
tapi hanya dua nama itu, karena hak globalnya cuma `USAGE`:

```sh
CREATE DATABASE IF NOT EXISTS `wa_dashboard`
  CHARACTER SET utf8mb4 COLLATE utf8mb4_0900_ai_ci;
CREATE DATABASE IF NOT EXISTS `waxum`
  CHARACTER SET utf8mb4 COLLATE utf8mb4_0900_ai_ci;
```

Collation dipin agar cocok dengan `migrations/0001_app_tables.sql`, yang
mendeklarasikan `utf8mb4_0900_ai_ci` di setiap tabel — bukan meninggalkan
default schema yang tak dipakai satu tabel pun.

Schema `waxum` **tidak** perlu dimigrasi tangan: `bootstrap-db.sql.tmpl` tidak
membuat satu tabel pun, dan waxum memigrasi schema-nya sendiri saat boot
pertama. Sesudah `docker compose up -d`, `SHOW TABLES` di `waxum` berisi 10
tabel.

Riwayat: sampai 2026-09-02 ini adalah `wa_dashboard_phkg` dan `waxum_phkg` di
`202.10.39.46:31006`, dengan dua user least-privilege terpisah sesuai
`bootstrap-db.sql.tmpl`. Keduanya sengaja **tidak dihapus** setelah cutover —
itu yang membuat rollback cukup `cp .env.bak-20260902 .env && docker compose up -d`.

### 4. Migrasi schema — SUDAH DILAKUKAN, versi 6 (diulang 2026-09-02 di `.15`)

Wajib sebelum app dipakai. Binary-nya tidak pernah migrate saat boot: perubahan
schema adalah tindakan operator, bukan efek samping start-up dari service yang
hard singleton. App akan boot mulus di atas database kosong lalu gagal pada
query pertama.

Bentuk normalnya lewat compose:

```sh
docker compose --profile migrate run --rm migrate up
docker compose --profile migrate run --rm migrate status
```

**Tapi itu belum bisa dipakai selama `.env` belum lengkap.** compose
meng-interpolasi SELURUH file sebelum menjalankan satu service pun, jadi guard
`${WSM_MINIO_*:?}` di service `app` ikut memblokir migrasi — padahal migrasi
justru datang lebih dulu daripada MinIO. Image `migrate` sendiri tidak butuh
apa pun selain `WSM_MYSQL_*`, jadi jalankan langsung:

```sh
docker run --rm --env-file .env whatsapp-with-dashboard-migrate:phkg-20260901 status
docker run --rm --env-file .env whatsapp-with-dashboard-migrate:phkg-20260901 up
```

`migrations/*.sql` tertanam di dalam image itu — tidak ada file SQL yang perlu
disalin ke host ini.

### 5. Jalankan

```sh
docker compose up -d
docker compose logs -f app
```

### 6. Pairing handset

Dashboard hanya mendengar di loopback (host ini belum punya reverse proxy dan
TLS). Dari laptop:

```sh
ssh -L 18801:127.0.0.1:18801 wa-phkg
```

lalu buka `http://localhost:18801`, login, dan scan QR di halaman sesi.
Pairing ini **baru** — identitas perangkat produksi tidak disalin ke sini, dan
memang tidak boleh: sesi WhatsApp adalah hard singleton, dua proses atas satu
store membuat WhatsApp me-logout sesi itu sendiri.

### 7. Sehat?

```sh
curl -sf http://127.0.0.1:18801/api/v1/health
```

---

## Siapa yang boleh membuka dashboard

App mendengar di `192.168.20.10:18801` (bukan `0.0.0.0` — itu sekalian
menyerahkannya ke `docker0` dan setiap antarmuka lain di host ini). Akses
dibatasi di chain `DOCKER-USER`:

| Sumber | |
|---|---|
| `172.18.0.0/16` | antar container di network yang sama |
| `192.168.20.0/24` | LAN PHKG |
| `10.212.134.0/24` | pool yang dibagikan FortiClient |
| selain itu | DROP |

```sh
sudo sh install-firewall.sh    # pasang + aktifkan unit systemd
sudo sh check-firewall.sh      # lihat penghitung paketnya
```

**`ufw` tidak bisa dipakai untuk ini, meskipun aktif di host ini.** Port yang
di-publish Docker tidak pernah melewati chain `INPUT`: docker memasang DNAT di
`PREROUTING` dan paketnya diteruskan lewat `FORWARD`. `ufw deny 18801` akan
terlihat terpasang sementara port-nya tetap terbuka untuk semua orang — gagal
diam-diam. `DOCKER-USER` adalah hook yang memang disediakan Docker untuk ini.

Aturannya mencocokkan port CONTAINER (`172.18.0.0/16` port 8800), bukan
`192.168.20.10:18801` yang diketik orang, karena paketnya sudah ter-DNAT saat
sampai di chain itu. Mencocokkan seluruh subnet dan bukan IP container membuat
aturan ini tetap benar setelah `docker compose up -d` membuat container baru.

**Setelah mengubahnya, baca kolom `pkts`, jangan cuma daftar aturannya.**
Aturan yang terpasang tapi tidak pernah dilewati paket adalah keamanan palsu.
Baris RETURN untuk jaringan Anda harus naik dari nol setelah Anda membuka
dashboard.

---

## Yang gampang merusak host ini

**Jangan pernah `docker compose down -v`.** Volume
`whatsapp-service-phkg-waxum` berisi SQLite per-sesi milik waxum plus
instance-lock — itu identitas perangkat yang di-pair di langkah 6, dan tidak
ada salinan keduanya di mana pun. Menghapusnya berarti scan QR ulang dengan
handset fisik. `docker compose down` (tanpa `-v`) aman.

**`docker compose config` tanpa `--quiet` mencetak seluruh `.env`** — untuk
setiap service, karena semuanya pakai `env_file`. Bentuk itu sudah pernah
menaruh password root MySQL ke log lebih dari sekali. Hanya `--quiet` yang
boleh dipakai di sini; kalau butuh satu nilai, filter satu key saja.

**waxum tidak boleh di-publish ke host.** `SUPERADMIN_TOKEN`-nya adalah
kredensial root untuk setiap sesi WhatsApp yang dipegangnya, dibandingkan
constant-time dan melewati JWT sepenuhnya. Karena itu service `waxum` sengaja
tidak punya blok `ports:`.

**Kalau nanti dipasang nginx di depan**, `client_max_body_size` harus **di
atas** `WSM_MEDIA_MAX_BYTES` (100 MiB). Default nginx 1 MiB menjawab 413
telanjang **sebelum** request diteruskan, jadi app tidak menulis satu baris log
pun dan tidak ada baris `wa_messages` — gejalanya terbaca seperti "upload saya
diabaikan aplikasi". Contoh konfig ada di
`docs/deploy-nginx-wa.conf.example` di repo.

**waxum hanya boleh satu proses.** Instance lock melarang dua proses menunjuk
satu storage dir, jadi tidak ada cerita rolling restart untuk service ini —
dan `restart`, bukan `up -d --force-recreate`, adalah cara memulihkan socket
waxum yang macet di `connecting`.
