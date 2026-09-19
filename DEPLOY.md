# Panduan Deploy 9Router-Go ke VPS

> Disusun dari pembacaan langsung kode di repo ini (`cmd/9router-go/main.go`,
> `internal/config/config.go`, `internal/handlers/dashboard/auth_session.go`,
> `Dockerfile`, `docker-compose.yml`, `.env.example`). Semua nilai di bawah
> sudah diverifikasi, bukan asumsi.

---

## 0. Fakta penting sebelum mulai (baca dulu)

| Hal | Kenyataan di kode |
|---|---|
| Port default | **20128** (`internal/config/config.go`) |
| Bind address | Server listen di `$HOST:$PORT`. `HOST` kosong → **0.0.0.0** (semua interface). Set `HOST=127.0.0.1` bila di belakang reverse proxy / Cloudflare Tunnel. |
| Data dir default | `$DATA_DIR`, jika kosong → `~/.9router` |
| DB default | `$DB_PATH`, jika kosong → `$DATA_DIR/db/data.sqlite` |
| `JWT_SECRET` | Jika tak di-set, di-generate acak & disimpan ke `$DATA_DIR/jwt-secret` (0600). **Persisten antar restart.** |
| **Password dashboard** | **⚠️ Default `123456`** (`DefaultDashboardPassword`) bila `INITIAL_PASSWORD` kosong & belum ada hash tersimpan. |
| `INITIAL_PASSWORD` | Env var **paling penting** — override default `123456`. |
| Auto-update | `AUTO_UPDATE=true` mengunduh binary baru dari GitHub Releases. **Risiko di VPS produksi.** |
| Token saver | `RTK_ENABLED` default **on**; `CAVEMAN_ENABLED`/`PONYTAIL_ENABLED` default off. |
| MITM | Generate root CA di data dir; butuh hak install CA di OS (lihat §6). |

> ⚠️ **KRITIS:** Tanpa `INITIAL_PASSWORD`, dashboard bisa dibuka dengan `123456`,
> dan server listen di `0.0.0.0`. Kombinasi ini = **dashboard terbuka untuk
> siapa saja di internet**. Wajib set password + firewall/reverse proxy.

## Deploy di belakang Cloudflare (Tunnel atau proxy)

Server **tidak punya TLS sendiri** — ia hanya berbicara HTTP. Cloudflare
menyediakan HTTPS-nya. Tiga hal yang wajib benar, karena semuanya adalah
konsekuensi dari "proxy itu berasal dari localhost":

| Env | Nilai | Alasan |
|---|---|---|
| `HOST` | `127.0.0.1` | `cloudflared`/nginx berjalan di host yang sama dan menghubungi origin dari `127.0.0.1`. Tanpa ini, port Anda **juga terbuka langsung dari internet**, sehingga penyerang bisa melewati Cloudflare sepenuhnya. |
| `TRUST_PROXY` | `true` | Tanpa ini, semua request terlihat berasal dari satu IP proxy, sehingga 5 kali salah password dari siapa pun = **lockout global** untuk semua pengguna. Header yang dipercaya: `X-Forwarded-For` (Cloudflare Tunnel mengisinya). **Bisa juga di-set dari UI** (Settings → Cloudflare → *Trust proxy headers*), dan setting di UI menang atas env. |
| `AUTH_COOKIE_SECURE` | `true` | Cookie sesi diberi flag `Secure` sehingga tidak dikirim lewat HTTP polos. **Bisa juga di-set dari UI** (Settings → Cloudflare → *Secure session cookie*). |

> 💡 **Tidak perlu edit env.** Dua baris terakhir di tabel di atas punya
> tombol di **Settings → 🌐 Cloudflare / Reverse Proxy**: *Enable Cloudflare
> mode* menyalakan keduanya sekaligus dan langsung berlaku tanpa restart.
> Hanya `HOST` yang tetap butuh restart, karena socket listen tidak bisa di-bind
> ulang saat berjalan — UI menampilkan nilai aktifnya dan memperingatkan bila
> masih `0.0.0.0`. Tombol **Copy config** menyalin file `cloudflared` siap pakai.

> 🔒 **Wajib baca — endpoint reset password.**
> `POST /api/dashboard/auth/reset-password` **memerlukan password saat ini**.
> Sebelumnya endpoint ini hanya dijaga oleh cek "request dari localhost".
> Guard itu **tidak berguna di belakang proxy**: karena `cloudflared` memanggil
> origin dari `127.0.0.1`, setiap request dari internet terlihat "lokal", dan
> siapa pun yang tahu domain Anda bisa mengosongkan password lalu login dengan
> default `123456` — **takeover penuh tanpa kredensial**. Jangan pernah turunkan
> kembali versi yang memakai guard berbasis IP.

Contoh `cloudflared`:

```yaml
# ~/.cloudflared/config.yml
tunnel: <TUNNEL-ID>
credentials-file: /root/.cloudflared/<TUNNEL-ID>.json
ingress:
  - hostname: router.example.com
    service: http://127.0.0.1:20127
  - service: http_status:404
```

Jalankan server dengan:

```bash
HOST=127.0.0.1 \
TRUST_PROXY=true \
AUTH_COOKIE_SECURE=true \
INITIAL_PASSWORD='<password-panjang>' \
  ./9router-go --port 20127 --db-path /var/lib/9router/data.sqlite
```

Di Cloudflare dashboard, set **SSL/TLS → Overview → Full** (bukan Flexible),
sehingga Cloudflare↔origin tetap di jaringan privat/loopback.

---

## 1. Pilih metode deploy

Ada 3 cara. Ringkas:

| Metode | Kapan dipakai | Kelebihan |
|---|---|---|
| **A. Binary + systemd** | VPS kecil, ingin kontrol penuh | Ringan, startup cepat, mudah di-update |
| **B. Docker Compose** | Ingin isolasi & mudah di-redeploy | Paling mudah diulang, cocok multi-service |
| **C. Reverse proxy (Nginx/Caddy) + TLS** | Wajib jika diekspos ke internet | HTTPS, domain, keamanan |

Rekomendasi: **B atau A untuk prosesnya + C selalu** jika ada domain publik.

---

## 2. Opsi A — Binary + systemd

### 2.1 Build / unduh binary

**Build di VPS** (butuh Go ≥ versi di `go.mod`; proyek ini pakai `GOEXPERIMENT=jsonv2`):

```bash
git clone <repo> 9router-go && cd 9router-go
GOEXPERIMENT=jsonv2 CGO_ENABLED=0 go build -ldflags="-s -w" -o 9router-go ./cmd/9router-go/
```

> ⚠️ **Jangan** build target `android/arm64` dengan Go 1.27 (catatan proyek).
> Untuk VPS x86_64/arm64 linux, build biasa sudah benar.

**ATAU cross-compile dari mesin lokal** (paling rapi — VPS tak perlu Go):

```bash
GOEXPERIMENT=jsonv2 CGO_ENABLED=0 GOOS=linux GOARCH=amd64 \
  go build -ldflags="-s -w" -o 9router-go ./cmd/9router-go/
# untuk ARM VPS (Hetzner Ampere, Oracle ARM, dsb): GOARCH=arm64
scp ./9router-go user@VPS:/usr/local/bin/9router-go
```

### 2.2 Siapkan user & direktori (hardening)

```bash
sudo useradd --system --home /var/lib/9router --shell /usr/sbin/nologin 9router
sudo mkdir -p /var/lib/9router
sudo chown -R 9router:9router /var/lib/9router
sudo chmod 700 /var/lib/9router
sudo install -m 0755 ./9router-go /usr/local/bin/9router-go
```

### 2.3 File environment (JANGAN taruh rahasia di unit file)

```bash
sudo tee /etc/9router/9router.env >/dev/null <<'EOF'
PORT=20128
DATA_DIR=/var/lib/9router

# WAJIB: ganti dengan password kuat. Tanpa ini dashboard = "123456".
INITIAL_PASSWORD=GANTI-DENGAN-PASSWORD-PANJANG

# WAJIB di produksi: set agar sesi tidak invalid saat data dir di-reset.
JWT_SECRET=GANTI-DENGAN-64-HEX-ACAK
EOF
sudo chown root:9router /etc/9router/9router.env
sudo chmod 640 /etc/9router/9router.env
```

Generate `JWT_SECRET` acak:
```bash
openssl rand -hex 32      # tempel hasilnya ke JWT_SECRET
```

> `API_KEY_SECRET` dan `MACHINE_ID_SALT` punya default statis
> (`endpoint-proxy-api-key-secret`, `endpoint-proxy-salt`). Untuk produksi
> serius, set juga keduanya senilai acak.

### 2.4 Unit systemd

```bash
sudo tee /etc/systemd/system/9router.service >/dev/null <<'EOF'
[Unit]
Description=9Router-Go AI API proxy gateway
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
User=9router
Group=9router
EnvironmentFile=/etc/9router/9router.env
ExecStart=/usr/local/bin/9router-go
Restart=on-failure
RestartSec=3

# --- Hardening ---
NoNewPrivileges=true
PrivateTmp=true
ProtectSystem=strict
ProtectHome=true
ReadWritePaths=/var/lib/9router
ProtectKernelTunables=true
ProtectControlGroups=true
RestrictSUIDSGID=true
LockPersonality=true
# Catatan: MITM (opsional) butuh lebih longgar; lihat §6.

[Install]
WantedBy=multi-user.target
EOF

sudo systemctl daemon-reload
sudo systemctl enable --now 9router
sudo systemctl status 9router
```

### 2.5 Verifikasi

```bash
curl -s http://127.0.0.1:20128/health
# → {"status":"ok"}
journalctl -u 9router -f        # log
```

---

## 3. Opsi B — Docker Compose

### 3.1 Compose minimal (produksi)

```yaml
# docker-compose.yml
services:
  9router-go:
    image: luqmenul/9router-go:latest
    # atau build lokal: build: .
    container_name: 9router-go
    ports:
      # ⚠️ Bind ke localhost saja; reverse proxy (Nginx/Caddy) yang hadap publik.
      - "127.0.0.1:20128:20128"
    environment:
      - PORT=20128
      - DATA_DIR=/data
      - INITIAL_PASSWORD=GANTI-DENGAN-PASSWORD-PANJANG
      - JWT_SECRET=GANTI-DENGAN-64-HEX-ACAK
      - RTK_ENABLED=true
      - AUTO_UPDATE=false        # ⚠️ JANGAN auto-update di container
    volumes:
      - ./data:/data
    restart: unless-stopped
```

> Ganti `INITIAL_PASSWORD`/`JWT_SECRET` inline dengan file `.env` bila ingin
> lebih rapi: letakkan `9router.env` sebelah compose, lalu tambahkan
> `env_file: [./9router.env]` dan hapus baris `environment:` yang sensitif.

### 3.2 Jalankan

```bash
docker compose up -d
docker compose logs -f
curl -s http://127.0.0.1:20128/health
```

> **Catatan port:** `docker-compose.yml` di repo memakai `20128:20128` dan
> `DATA_DIR=/data`. README menyebut `20130:20130` di beberapa contoh — pakai
> **20128** yang sesuai default kode, atau ubah konsisten di kedua sisi.

### 3.3 Update image

```bash
docker compose pull && docker compose up -d
```

---

## 4. Opsi C — Reverse proxy + TLS (WAJIB bila publik)

Karena app tidak punya TLS/domain sendiri dan bind `0.0.0.0`, taruh di
belakang Nginx/Caddy.

### 4.1 Caddy (paling mudah, TLS otomatis)

```caddyfile
# /etc/caddy/Caddyfile
router.example.com {
    reverse_proxy 127.0.0.1:20128

    # SSE (Console Log) butuh buffering dimatikan — Caddy sudah default ok,
    # tapi bila bermasalah tambahkan:
    # flush_interval -1
}
```
```bash
sudo systemctl reload caddy
```

### 4.2 Nginx

```nginx
server {
    listen 443 ssl http2;
    server_name router.example.com;

    ssl_certificate     /etc/letsencrypt/live/router.example.com/fullchain.pem;
    ssl_certificate_key /etc/letsencrypt/live/router.example.com/privkey.pem;

    # Batas ukuran body (proxy ini memakai MaxBody; sesuaikan bila perlu).
    client_max_body_size 128m;

    location / {
        proxy_pass http://127.0.0.1:20128;
        proxy_http_version 1.1;
        proxy_set_header Host              $host;
        proxy_set_header X-Real-IP         $remote_addr;
        proxy_set_header X-Forwarded-For   $proxy_add_x_forwarded_for;
        proxy_set_header X-Forwarded-Proto $scheme;

        # ⚠️ PENTING untuk SSE Console Log: matikan buffering.
        proxy_buffering off;
        proxy_cache off;
        proxy_read_timeout 3600s;
    }
}
```

Peroleh sertifikat:
```bash
sudo certbot --nginx -d router.example.com
```

> ⚠️ **SSE:** endpoint `/api/translator/console-logs/stream` adalah
> Server-Sent Events. `proxy_buffering off` **wajib**, jika tidak log tidak
> akan streaming.

---

## 5. Firewall & jaringan

```bash
# Jangan buka 20128 ke publik bila ada reverse proxy.
sudo ufw default deny incoming
sudo ufw allow OpenSSH
sudo ufw allow 80,443/tcp
sudo ufw enable
sudo ufw status verbose
```

Akses langsung tanpa reverse proxy (kurang ideal):
```bash
sudo ufw allow 20128/tcp   # hanya bila benar-benar perlu
```
Karena server bind `0.0.0.0`, **firewall adalah satu-satunya kontrol** bila
tidak pakai reverse proxy.

---

## 6. Fitur opsional: MITM

`mitm enable` memasang **root CA** untuk mencegat trafik CLI tool. Di VPS:

- Butuh tulis ke data dir (`/var/lib/9router`).
- Untuk mempercayai CA di OS: instal `rootCA.pem` yang di-generate ke
  trust store sistem (mis. `/usr/local/share/ca-certificates/` lalu
  `update-ca-certificates`).
- Unit systemd `ProtectSystem=strict` + `ProtectHome=true` di §2.4 mungkin
  perlu dilonggarkan bila MITM menulis ke lokasi lain.
- **Jangan** aktifkan MITM bila Anda tidak benar-benar mengerti implikasinya
  (semua trafik TLS bisa didekripsi).

Jika tak butuh, abaikan sepenuhnya.

---

## 7. Backup & restore

- **Backup dashboard**: gunakan UI (Export) atau endpoint
  `/api/dashboard/database`. Import = **REPLACE penuh** dalam satu transaksi —
  hati-hati.
- **Backup manual** (paling aman): salin `$DATA_DIR` **saat server berhenti**,
  atau pakai `.backup` SQLite:
  ```bash
  sqlite3 /var/lib/9router/db/data.sqlite ".backup '/backup/data-$(date +%F).sqlite'"
  ```
- Simpan juga `$DATA_DIR/jwt-secret` — bila hilang dan `JWT_SECRET` tak di-set,
  semua sesi login invalid (bukan kehilangan data, hanya harus login ulang).

---

## 8. Checklist keamanan (wajib sebelum online)

- [ ] `INITIAL_PASSWORD` di-set ke password kuat (**bukan `123456`**).
- [ ] `JWT_SECRET` di-set (atau `jwt-secret` di data dir di-backup).
- [ ] `API_KEY_SECRET` & `MACHINE_ID_SALT` di-set acak (opsional tapi disarankan).
- [ ] `AUTO_UPDATE=false` (kontrol update manual).
- [ ] Port **tidak** terbuka ke publik bila ada reverse proxy.
- [ ] TLS aktif (Caddy/Nginx + certbot) bila ada domain.
- [ ] File env ber-perm `640`, owner `root:9router` — bukan world-readable.
- [ ] Service berjalan sebagai user non-root (`9router`).
- [ ] `proxy_buffering off` untuk SSE.
- [ ] Backup `$DATA_DIR` + config berkala.

---

## 9. Update & rollback

**Binary (systemd):**
```bash
sudo systemctl stop 9router
sudo cp /usr/local/bin/9router-go /usr/local/bin/9router-go.bak   # rollback point
# taruh binary baru, lalu:
sudo systemctl start 9router
curl -s http://127.0.0.1:20128/health
# bila gagal: sudo cp /usr/local/bin/9router-go.bak /usr/local/bin/9router-go && sudo systemctl restart 9router
```

**Docker:**
```bash
docker compose pull && docker compose up -d
# rollback: pin versi spesifik image di compose, lalu up -d
```

**Self-update built-in** (opsional): `9router-go update` mengunduh dari GitHub
Releases. Aman-ish karena memverifikasi SHA256 (`PerformSelfUpdate`), tapi di
VPS produksi lebih baik update manual + rollback point.

---

## 10. Pemecahan masalah

| Gejala | Penyebab & solusi |
|---|---|
| `/health` connection refused | Service mati / port salah. `systemctl status 9router`, cek `PORT`. |
| Dashboard bisa dibuka `123456` | `INITIAL_PASSWORD` kosong & belum ada hash. Set env, restart, **segera ganti password**. |
| Console Log tidak stream | `proxy_buffering` masih on di Nginx; matikan. Cek juga cookie sesi valid. |
| Sesi login hilang tiap restart | `JWT_SECRET` tak di-set & data dir tidak persisten (container tanpa volume). Set `JWT_SECRET` atau mount volume. |
| 401 di semua `/v1/*` | Butuh API key (Bearer) — bukan cookie sesi. Engine `/v1/*` sengaja menolak cookie. |
| Container jalan tapi data hilang | `DATA_DIR` tidak di-mount ke volume. Pastikan `./data:/data`. |
| Port bentrok | Ubah `PORT` (env) + `EXPOSE`/mapping compose. |

---

## 11. Ringkasan cepat (TL;DR)

```bash
# Cara tercepat & aman: Docker + Caddy
# 1. Build image lokal
cd 9router-go && docker compose up -d --build

# 2. Set password + JWT (di compose environment / .env)
INITIAL_PASSWORD=<kuat>   JWT_SECRET=$(openssl rand -hex 32)

# 3. Pulihkan di belakang Caddy dengan domain + TLS otomatis
router.example.com { reverse_proxy 127.0.0.1:20128 }
```

Jangan pernah online dengan `INITIAL_PASSWORD` kosong dan port terbuka.
