# Cloudflare Mode — Panduan Pemakaian

> Panduan ini menjelaskan **cara memakai** fitur Cloudflare di dashboard
> (Settings → 🌐 Cloudflare / Reverse Proxy), bukan cara deploy dari nol.
> Untuk deploy server lengkap lihat [DEPLOY.md](DEPLOY.md).
>
> Semua nilai di bawah dibaca langsung dari kode
> (`internal/handlers/dashboard/ui/index.html`, `dashboard.go`,
> `auth_handler.go`, `auth_limiter.go`, `internal/db/settings.go`)
> dan sudah diuji pada server yang berjalan.

---

## 1. Ini untuk apa?

Server 9Router **tidak punya TLS sendiri** — ia hanya berbicara HTTP polos.
Cloudflare (Tunnel atau proxy biasa) yang menyediakan HTTPS-nya.

Artinya di mata server, **semua request datang dari `127.0.0.1`**, bukan dari
IP pengguna asli. Dua masalah muncul dari situ:

| Masalah | Akibatnya kalau dibiarkan |
|---|---|
| IP klien palsu | Semua request terlihat dari satu IP (IP Cloudflare). **5 kali salah password dari siapa pun = lockout global** untuk semua pengguna. |
| Origin tidak tahu ia HTTPS | Cookie sesi dikirim tanpa flag `Secure`, sehingga bisa bocor lewat hop HTTP polos. |

**Cloudflare mode** menyalakan dua switch yang memperbaiki keduanya sekaligus.

---

## 2. Cara mengaktifkan — 3 langkah

### Langkah 1 — Buka kartunya

Dashboard → sidebar → **Settings** → cari kartu **🌐 Cloudflare / Reverse Proxy**.

Di dalamnya ada tombol:

```
[ ⚡ Enable Cloudflare mode ]   [ 📋 Copy config ]
```

### Langkah 2 — Klik "Enable Cloudflare mode"

Satu klik menyalakan **keduanya** sekaligus:

- ✅ **Trust proxy headers** → `trustProxy = true`
- ✅ **Secure session cookie** → `authCookieSecure = true`

Perubahan **langsung berlaku, tanpa restart**. Server membaca setting ini dari
database tiap request, jadi tidak perlu mematikan proses.

Kalau berhasil, muncul pesan di kotak `cf-msg`.

### Langkah 3 — Betulkan `HOST` (perlu restart)

**Ini satu-satunya langkah manual.** `HOST` adalah alamat bind socket, dan itu
**tidak bisa diubah saat proses berjalan** — harus restart.

Lihat kartu **Listen address** tepat di atas tombol. Kalau tertulis:

```
HOST   0.0.0.0
```

…maka port Anda **masih terbuka langsung dari internet**, dan Cloudflare bisa
dilewati sepenuhnya. Restart proses dengan:

```bash
HOST=127.0.0.1 ./9router-go --port 20128
```

Kalau tertulis `127.0.0.1` atau `::1`, Anda sudah aman — tidak ada yang perlu
dilakukan.

> **Catatan:** tombol *Enable Cloudflare mode* akan memperingatkan Anda kalau
> `HOST` masih `0.0.0.0`/`::`. Peringatan itu **tidak** menggagalkan
> penyimpanan — dua switch tetap tersimpan, hanya `HOST` yang butuh restart.

---

## 3. Tombol "Copy config"

Menyalin file `~/.cloudflared/config.yml` yang siap pakai ke clipboard.

Isinya dibuat **dari data nyata**, bukan placeholder yang harus Anda tebak:

| Baris | Diambil dari |
|---|---|
| `hostname:` | Textbox **Domain** di menu Endpoint & Key (kalau kosong → `router.example.com`) |
| `service: http://127.0.0.1:<port>` | Port aktif proses |
| `--db-path ...` | Hanya ditulis bila Anda memang mengatur DB path sendiri |

Contoh hasilnya:

```yaml
# ~/.cloudflared/config.yml
tunnel: <TUNNEL-ID>
credentials-file: /root/.cloudflared/<TUNNEL-ID>.json
ingress:
  - hostname: router.example.com
    service: http://127.0.0.1:20128
  - service: http_status:404
```

> **Kenapa `--db-path` kadang tidak muncul?** Karena menyebut path karangan di
> file config adalah cara copy-paste berakhir di database kosong yang terlihat
> seperti kehilangan data. Flag itu hanya ditulis kalau Anda memang set sendiri.

Kalau clipboard diblokir browser (halaman non-HTTPS), teksnya muncul di dialog
untuk disalin manual — tetap berfungsi.

---

## 4. Langkah di sisi Cloudflare

Di **Cloudflare Dashboard**, bukan di 9Router:

**SSL/TLS → Overview → pilih `Full`**.

Jangan pakai `Flexible`. Mode `Flexible` membuat Cloudflare↔origin tetap HTTP
polos, sehingga flag `Secure` pada cookie menjadi tidak berarti dan trafik
antara Cloudflare dan server Anda bisa dibaca di jaringan.

---

## 5. Verifikasi bahwa semuanya bekerja

Cek satu per satu:

**a. Dua switch tersimpan.** Buka Settings → Cloudflare. Keduanya harus
menyala. (Kalau Anda refresh halaman, nilainya diambil dari server — bukan dari
memori browser.)

**b. `HOST` sudah loopback.** Kartu *Listen address* harus menunjukkan
`127.0.0.1`. Dari terminal:

```bash
ss -ltn | grep 20128
```

Kalau muncul `*:20128` atau `0.0.0.0:20128`, port masih terbuka ke internet —
artinya Cloudflare bisa dilewati. Yang benar: `127.0.0.1:20128`.

**c. Cookie punya flag `Secure`.** Login ke dashboard, lalu di DevTools →
Application → Cookies, cari cookie sesi. Kolom **Secure** harus ✓.

**d. Rate limit bekerja per-IP, bukan global.** Coba salah password 5 kali dari
satu perangkat — hanya perangkat itu yang diblokir sementara, pengguna lain
tetap bisa masuk. Kalau semua orang ikut terblokir, `trustProxy` belum aktif.

Lockout bertingkat: **30 detik → 2 menit → 10 menit → 30 menit** (langkah
terakhir berulang). Batas awalnya **5 kegagalan**, dan hitungannya reset setelah
1 jam tanpa kegagalan. Gejalanya HTTP **429**, bukan 401.

---

## 6. Kalau bermasalah

| Gejala | Penyebab | Solusi |
|---|---|---|
| Semua pengguna ter-lockout setelah satu orang salah password | `trustProxy` mati | Klik *Enable Cloudflare mode* |
| Tidak bisa login, browser tidak mengirim cookie | `authCookieSecure` aktif tapi Anda akses lewat HTTP polos | Akses via HTTPS Cloudflare, atau matikan switch ini sementara |
| Masih bisa diakses lewat `IP:port` langsung | `HOST` masih `0.0.0.0` | Restart dengan `HOST=127.0.0.1` |
| Peringatan "Reachable directly on 0.0.0.0" | Sama seperti di atas | Sama |
| Tombol Copy tidak menyalin apa pun | Clipboard butuh HTTPS | Salin manual dari dialog yang muncul |

---

## 7. Hubungan dengan textbox "Domain"

Textbox **Domain** di menu Endpoint & Key dan fitur ini **saling melengkapi**:

- **Domain** menyimpan alamat publik Anda (dipakai tombol *Copy config* untuk
  mengisi `hostname:` secara otomatis)
- **Cloudflare mode** menyalakan perilaku reverse-proxy yang benar

**Urutan yang disarankan:** isi Domain dulu, baru klik *Copy config* — supaya
file yang dihasilkan langsung memakai domain Anda yang sebenarnya, bukan
placeholder `router.example.com`.

---

## 8. Ringkasan satu paragraf

Isi **Domain** di menu Endpoint, klik **Enable Cloudflare mode** di Settings,
lalu restart sekali dengan `HOST=127.0.0.1`. Selesai. Tiga hal itu membuat
cloudflared bisa menghubungi origin, rate-limit menghitung per-IP asli, dan
cookie sesi tidak bocor lewat hop HTTP.

---

## Referensi kode

| Perilaku | Lokasi |
|---|---|
| Tombol & kartu Cloudflare | `internal/handlers/dashboard/ui/index.html` (`applyCloudflarePreset`, `showCloudflaredConfig`) |
| Resolusi setting (UI menang atas env) | `internal/handlers/dashboard/dashboard.go` (`effectiveTrustProxy`, `effectiveCookieSecure`) |
| `X-Forwarded-For` dipercaya hanya bila diaktifkan | `internal/handlers/dashboard/auth_limiter.go` (`clientIP`) |
| Flag `Secure` pada cookie | `internal/handlers/dashboard/auth_handler.go` (`shouldUseSecureCookie`) |
| Field tersimpan | `internal/db/settings.go` (`TrustProxy`, `AuthCookieSecure`) |
