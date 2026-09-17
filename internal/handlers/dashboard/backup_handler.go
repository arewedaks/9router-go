package dashboard

import (
	"encoding/json"
	"io"
	"net/http"

	"9router/proxy/internal/dbbackup"
	"9router/proxy/internal/handlerutil"
)

// maxBackupBytes caps an uploaded backup. The real database is a few megabytes
// with tokens included; 64 MiB leaves generous headroom while still refusing a
// body that would otherwise be read into memory without limit.
const maxBackupBytes = 64 << 20

// backupPasswordHeader carries the confirmation password. It matches the
// reference's `x-9r-password` so the same client habits work, and keeping it a
// header means the value never lands in a URL or a log line.
const backupPasswordHeader = "x-9r-password"

// confirmBackupPassword re-checks the dashboard password before a destructive or
// token-revealing action.
//
// A valid session is not enough here. Export hands over every stored credential,
// and import overwrites the whole configuration — both are worth a deliberate
// confirmation rather than riding on a cookie that a tab left open still holds.
// The reference gates the same two endpoints the same way.
func (h *Handler) confirmBackupPassword(r *http.Request) bool {
	settings, _ := h.repo.GetSettings()
	stored := ""
	if settings != nil {
		stored = settings.PasswordHash
	}
	return VerifyPassword(h.auth.hasher, stored, r.Header.Get(backupPasswordHeader))
}

// HandleExportDatabase streams the whole database as a JSON backup.
func (h *Handler) HandleExportDatabase(w http.ResponseWriter, r *http.Request) {
	if !h.confirmBackupPassword(r) {
		handlerutil.WriteJSONError(w, http.StatusUnauthorized, "Invalid password")
		return
	}

	doc, err := dbbackup.Export(h.repo.RawDB())
	if err != nil {
		handlerutil.WriteJSONError(w, http.StatusInternalServerError, "Failed to export database: "+err.Error())
		return
	}

	body, err := json.Marshal(doc)
	if err != nil {
		handlerutil.WriteJSONError(w, http.StatusInternalServerError, "Failed to encode backup")
		return
	}

	name := "9router-backup-" + backupTimestamp(doc.ExportedAt) + ".json"
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Content-Disposition", `attachment; filename="`+name+`"`)
	// The file contains live tokens; keep it out of any intermediary cache.
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(body)
}

// HandleImportDatabase replaces the database with an uploaded backup.
//
// Import is destructive by design: restoring a known-good state means anything
// missing from the file has to disappear. It runs in one transaction so a
// failure part-way leaves the previous configuration intact.
func (h *Handler) HandleImportDatabase(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(io.LimitReader(r.Body, maxBackupBytes))
	if err != nil {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, "Failed to read body")
		return
	}

	var doc dbbackup.Document
	if err := json.Unmarshal(body, &doc); err != nil {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, "Invalid backup file: "+err.Error())
		return
	}

	// The password travels in the body for import (it is a POST payload, so the
	// header is not always convenient), but accept the header too.
	password := r.Header.Get(backupPasswordHeader)
	if password == "" {
		var probe struct {
			Password string `json:"password"`
		}
		_ = json.Unmarshal(body, &probe)
		password = probe.Password
	}
	settings, _ := h.repo.GetSettings()
	stored := ""
	if settings != nil {
		stored = settings.PasswordHash
	}
	if !VerifyPassword(h.auth.hasher, stored, password) {
		handlerutil.WriteJSONError(w, http.StatusUnauthorized, "Invalid password")
		return
	}

	if err := dbbackup.Import(h.repo.RawDB(), &doc); err != nil {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, "Failed to import database: "+err.Error())
		return
	}

	// The in-memory token saver and any cached view still reflect the old rows,
	// so reload them before serving the next request.
	h.reloadAfterImport()

	handlerutil.WriteJSON(w, http.StatusOK, map[string]any{
		"success": true,
		"summary": dbbackup.Summarize(&doc),
	})
}

// HandleInspectBackup reports what a file contains without importing it, so the
// UI can show the operator what they are about to overwrite.
func (h *Handler) HandleInspectBackup(w http.ResponseWriter, r *http.Request) {
	if !h.confirmBackupPassword(r) {
		handlerutil.WriteJSONError(w, http.StatusUnauthorized, "Invalid password")
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, maxBackupBytes))
	if err != nil {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, "Failed to read body")
		return
	}
	var doc dbbackup.Document
	if err := json.Unmarshal(body, &doc); err != nil {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, "Invalid backup file: "+err.Error())
		return
	}
	// Surface the same checks Import will apply, so a file that cannot be
	// restored is reported here rather than after the operator commits to it.
	if err := dbbackup.ValidatePasswordHash(doc.Settings); err != nil {
		handlerutil.WriteJSON(w, http.StatusOK, map[string]any{
			"valid":  false,
			"reason": err.Error(),
		})
		return
	}
	handlerutil.WriteJSON(w, http.StatusOK, map[string]any{
		"valid":       true,
		"exportedAt":  doc.ExportedAt,
		"app":         doc.App,
		"format":      doc.FormatVersion,
		"summary":     dbbackup.Summarize(&doc),
		"hasSettings": len(doc.Settings) > 0,
	})
}

// reloadAfterImport re-reads the settings that the router keeps in memory. An
// import can flip any of them, and leaving the old values in place would make
// the restore look half-applied.
func (h *Handler) reloadAfterImport() {
	if h.tokenSaver == nil {
		return
	}
	s, err := h.repo.GetSettings()
	if err != nil || s == nil {
		return
	}
	// Only the settings the database actually stores. Injection Guard is a
	// token-saver field with no persisted counterpart, so an import cannot
	// change it and touching it here would invent state.
	h.tokenSaver.SetRTK(s.RTKEnabled)
	h.tokenSaver.SetCaveman(s.CavemanEnabled, s.CavemanLevel)
	h.tokenSaver.SetPonytail(s.PonytailEnabled, s.PonytailLevel)
}

// backupTimestamp turns the RFC3339 export time into a filename-safe stamp.
// Falls back to a stable literal if the value is missing, so the download name
// is never empty.
func backupTimestamp(exportedAt string) string {
	if exportedAt == "" {
		return "unknown"
	}
	out := make([]rune, 0, len(exportedAt))
	for _, r := range exportedAt {
		switch r {
		case ':', '.':
			out = append(out, '-')
		case '+':
			out = append(out, 'Z') // stop at the zone offset
			return string(out)
		default:
			out = append(out, r)
		}
	}
	return string(out)
}
