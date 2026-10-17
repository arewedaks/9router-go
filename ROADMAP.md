# 9Router Go — Future Roadmap & Feature Proposals

Dokumen ini mencatat rencana pengembangan fitur dan peningkatan kapabilitas untuk rilis mendatang pada **9Router Go**.

> **Status per v1.9.35.** Item yang sudah dikirim dipindahkan ke bagian *Sudah Dikirim* di bawah, dengan rujukan paketnya, supaya tidak ada yang membangun ulang. Hanya item yang masih terbuka yang tetap tinggal di daftar prioritas.

---

## 🎯 Prioritas & Kategori Fitur (masih terbuka)

### 1. 🔀 Dynamic Cost-Aware Model Routing

* **Deskripsi**: Routing cerdas otomatis berdasarkan ukuran token dan kompleksitas instruksi pengguna.
* **Mekanisme**:
  * **Small / Simple Prompts** (< 2.000 tokens): Diarahkan ke model murah & ultra cepat (seperti `gemini-2.5-flash` atau `claude-haiku-4.5`).
  * **Complex / Heavy Coding** (> 20.000 tokens atau ada instruksi reasoning/tool complex): Diarahkan ke model flagship (`claude-sonnet-4.6`, `gemini-3-flash-agent`, atau `gpt-5`).
* **Manfaat**:
  * Mengurangi biaya token hingga 60-80% tanpa mengorbankan kualitas pada tugas-tugas berat.

---

### 2. 🔔 Webhook & Instant Alerting System
* **Deskripsi**: Sistem notifikasi real-time ke channel chat tim jika terjadi anomali atau kuota menipis.
* **Integrasi**: Telegram Bot, Discord Webhook, Slack Incoming Webhook.
* **Triggers**:
  * Upstream `429 Rate Limit` atau kuota provider habis.
  * Akumulasi biaya harian/bulanan melewati batas ambang (threshold budget).
  * OAuth token refresh failure atau koneksi upstream down.

---

### 3. 👥 Multi-Tenant Quota & Budget Enforcement
* **Deskripsi**: Kontrol akses dan kuota berbasis API Key atau User ID internal.
* **Fitur**:
  * Batas pengeluaran harian/bulanan per API Key (misal: max $20/bulan per user).
  * Auto-rate limiting jika request per menit (RPM) melebihi batas.
  * Dashboard view untuk memantau konsumsi token per tim / proyek.

---

### 4. 🛡️ PII & Secret Redaction Filter (Data Privacy)
* **Deskripsi**: Filter otomatis pada request body untuk menyamarkan (*masking*) data sensitif sebelum dikirim ke server AI publik.
* **Deteksi**:
  * API Keys (AWS, OpenAI, GitHub token, private keys).
  * Data pribadi (Nomor kartu kredit, email, IP address publik).

---

## ✅ Sudah Dikirim (jangan dibangun ulang)

### Exact Prompt Caching — *selesai*
`internal/promptcache`. Key dari `HashRequest(model, body)`; request yang tidak cacheable mengembalikan `ok=false`. Tiga batas berlaku sekaligus: jumlah entri, TTL, dan **byte budget** (`enforceBudgetLocked`), plus `MaxEntryBytes()` untuk menolak respons kebesaran sebelum disimpan. Lihat ARCHITECTURE.md → *Prompt Cache*.

### Token Saving (RTK / Caveman / Ponytail) — *selesai*
`internal/tokensaver` (`compress.go`, `levels.go`, `injection.go`, `prompts.go`, `headroom.go`) plus `internal/headroom` untuk lifecycle layanan kompresi. Level aktif terlihat di log boot (`token savers — rtk=… caveman=… ponytail=…`).

### Latency Percentile p50/p95/p99 per Provider+Model — *selesai*
`internal/tracing` melaporkan `LatencyStats{P50Ms, P95Ms, P99Ms}`, diekspor lewat HTTP. Yang **belum** ada hanyalah lapisan visualisasinya di dashboard (heatmap, grafik token efficiency) — kalau mau dikerjakan, sisakan item itu saja.

### Sisa item terbuka
Cost-aware routing (#1), webhook/alerting (#2), multi-tenant quota per API key (#3), PII & secret redaction (#4), dan visualisasi analytics di atas data `internal/tracing`.
