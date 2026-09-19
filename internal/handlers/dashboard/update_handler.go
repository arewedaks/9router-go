// Self-update endpoints for the dashboard.
//
// The chat handler already exposes /api/version/* for programmatic clients, but
// those sit outside the dashboard session group and return the raw updater
// shape. The browser needs three things the raw endpoints do not give it: a
// cheap cached status to poll (so the sidebar button can appear without hitting
// the releases API every time), a way to force a fresh check when the operator
// opens the dialog, and an apply step that is gated on the dashboard password
// because it replaces the running binary and restarts the process.
package dashboard

import (
	"net/http"
	"time"

	"9router/proxy/internal/handlerutil"
	"9router/proxy/internal/updater"
)

// HandleUpdateStatus returns the cached updater state for the sidebar button.
//
// Deliberately does not hit the network: the background check already refreshes
// the cache on its own interval, and the browser polls this on every page load.
//
// The reply is flattened rather than returning updater.GetStatus() verbatim.
// That struct nests the version details under cachedInfo, which would force the
// button code to guess whether a field lives at the top level or one down — the
// exact confusion that made a refused release render as "up to date".
func (h *Handler) HandleUpdateStatus(w http.ResponseWriter, r *http.Request) {
	st := updater.GetStatus()
	if st == nil {
		handlerutil.WriteJSON(w, http.StatusOK, map[string]any{"hasUpdate": false})
		return
	}

	out := map[string]any{
		"currentVersion":    st.CurrentVersion,
		"latestVersion":     st.LatestVersion,
		"hasUpdate":         st.HasUpdate,
		"autoUpdateEnabled": st.AutoUpdateEnabled,
		"updateInProgress":  st.UpdateInProgress,
		"lastCheckTime":     st.LastCheckTime,
	}
	// Only meaningful when the cache exists; a status without one means the
	// background check has not run yet.
	if st.CachedInfo != nil {
		out["releaseNotes"] = st.CachedInfo.ReleaseNotes
		out["untrustedSource"] = st.CachedInfo.UntrustedSource
		if st.LatestVersion == "" {
			out["latestVersion"] = st.CachedInfo.LatestVersion
		}
	}
	handlerutil.WriteJSON(w, http.StatusOK, out)
}

// HandleUpdateCheck forces a fresh check against the release source.
//
// Used when the operator opens the update dialog: the cached status may be
// older than the interval, and a stale "already latest" would be the one thing
// most likely to make this feature look broken.
func (h *Handler) HandleUpdateCheck(w http.ResponseWriter, r *http.Request) {
	info, err := updater.CheckUpdate(r.Context())
	if err != nil {
		handlerutil.WriteJSONError(w, http.StatusBadGateway, "check update failed: "+err.Error())
		return
	}
	handlerutil.WriteJSON(w, http.StatusOK, info)
}

// HandleUpdateApply downloads, verifies, and installs the pending release, then
// restarts the process.
func (h *Handler) HandleUpdateApply(w http.ResponseWriter, r *http.Request) {
	// Replacing the running binary is as destructive as importing a database, so
	// it is not authorised by the session cookie alone. Without this, any page
	// that can reach the dashboard origin could trigger a restart.
	if !h.confirmBackupPassword(r) {
		handlerutil.WriteJSONError(w, http.StatusUnauthorized, "Invalid password")
		return
	}

	info, err := updater.CheckUpdate(r.Context())
	if err != nil {
		handlerutil.WriteJSONError(w, http.StatusBadGateway, "check update failed: "+err.Error())
		return
	}
	if !info.HasUpdate {
		handlerutil.WriteJSON(w, http.StatusOK, map[string]any{
			"status":  "up_to_date",
			"message": "9router-go is already on the latest version",
			"version": info.CurrentVersion,
		})
		return
	}
	if err := updater.PerformSelfUpdate(info.DownloadURL, info.SHA256); err != nil {
		// Surfaced verbatim: the common failures (read-only install directory,
		// untrusted source) are actionable only if the operator sees the reason.
		handlerutil.WriteJSONError(w, http.StatusInternalServerError, "self-update failed: "+err.Error())
		return
	}

	handlerutil.WriteJSON(w, http.StatusOK, map[string]any{
		"status":  "updated",
		"message": "Update installed. The process is restarting.",
		"version": info.LatestVersion,
	})

	// Restart after the response is flushed, otherwise the browser sees a
	// connection reset instead of the success payload.
	go func() {
		time.Sleep(1 * time.Second)
		updater.RestartSelf()
	}()
}
