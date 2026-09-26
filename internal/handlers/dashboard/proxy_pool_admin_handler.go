package dashboard

import (
	"context"
	"encoding/base64"
	json "encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"9router/proxy/internal/db"
	"9router/proxy/internal/handlerutil"
)

// proxyTestTimeout bounds a single connectivity check. Long enough for a slow
// upstream to answer a CONNECT, short enough that the dashboard's Test button
// does not look hung.
const proxyTestTimeout = 12 * time.Second

// HandleGetProxyPool returns one pool for the edit form.
func (h *Handler) HandleGetProxyPool(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if id == "" {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, "Missing pool ID")
		return
	}
	pool, err := h.repo.GetProxyPoolDetail(id)
	if err != nil {
		handlerutil.WriteJSONError(w, http.StatusNotFound, "Proxy pool not found")
		return
	}
	handlerutil.WriteJSON(w, http.StatusOK, pool)
}

// proxyPoolUpdatePayload is the body of PUT /proxy-pools/{id}. Every field is a
// pointer so an omitted field means "leave as stored" rather than "clear it" —
// the inline active toggle sends only isActive.
type proxyPoolUpdatePayload struct {
	Name        *string   `json:"name"`
	ProxyURL    *string   `json:"proxyUrl"`
	List        *string   `json:"list"`
	URLs        *[]string `json:"urls"`
	NoProxy     *string   `json:"noProxy"`
	Type        *string   `json:"type"`
	IsActive    *bool     `json:"isActive"`
	StrictProxy *bool     `json:"strictProxy"`
}

// HandleUpdateProxyPool edits a pool, accepting either a single proxyUrl or a
// pasted list, whichever the caller has.
func (h *Handler) HandleUpdateProxyPool(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if id == "" {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, "Missing pool ID")
		return
	}

	body, err := io.ReadAll(io.LimitReader(r.Body, maxProxyListBytes+1))
	if err != nil {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, "Failed to read body")
		return
	}
	if len(body) > maxProxyListBytes {
		handlerutil.WriteJSONError(w, http.StatusRequestEntityTooLarge, "Proxy list is too large (limit 2 MiB)")
		return
	}
	var payload proxyPoolUpdatePayload
	if err := json.Unmarshal(body, &payload); err != nil {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, "Invalid JSON: "+err.Error())
		return
	}

	// Resolve whichever form of the proxy value was sent into one URL list.
	var urls []string
	urlsProvided := false
	switch {
	case payload.List != nil:
		urlsProvided = true
		entries, _ := db.ParseProxyList(*payload.List)
		urls = entries
	case payload.URLs != nil:
		urlsProvided = true
		for _, e := range *payload.URLs {
			if u, ok := db.NormalizeProxyURL(e); ok {
				urls = append(urls, u)
			}
		}
	case payload.ProxyURL != nil:
		urlsProvided = true
		if u, ok := db.NormalizeProxyURL(*payload.ProxyURL); ok {
			urls = append(urls, u)
		}
	}
	if urlsProvided && len(urls) == 0 {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, "No usable proxy entries found (expected user:pass@host:port or host:port)")
		return
	}

	if err := h.repo.UpdateProxyPool(id, payload.Name, payload.ProxyURL, payload.NoProxy, payload.Type, urlsIfProvided(urlsProvided, urls), payload.IsActive, payload.StrictProxy); err != nil {
		handlerutil.WriteJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}

	pool, err := h.repo.GetProxyPoolDetail(id)
	if err != nil {
		handlerutil.WriteJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	handlerutil.WriteJSON(w, http.StatusOK, pool)
}

// urlsIfProvided distinguishes "the caller sent no proxy value" (nil, leave as
// stored) from "the caller sent an empty list" (non-nil empty, which the repo
// treats as a clear).
func urlsIfProvided(provided bool, urls []string) []string {
	if !provided {
		return nil
	}
	if urls == nil {
		return []string{}
	}
	return urls
}

// HandleDeleteProxyPool removes a pool, refusing while any provider still
// selects it. A 409 with the binding count lets the UI explain the refusal
// instead of showing a generic failure.
func (h *Handler) HandleDeleteProxyPool(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if id == "" {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, "Missing pool ID")
		return
	}
	bound, err := h.repo.DeleteProxyPool(id)
	if errors.Is(err, db.ErrProxyPoolNotFound) {
		handlerutil.WriteJSONError(w, http.StatusNotFound, "Proxy pool not found")
		return
	}
	if err != nil {
		handlerutil.WriteJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if bound > 0 {
		handlerutil.WriteJSON(w, http.StatusConflict, map[string]any{
			"error":                fmt.Sprintf("%d provider(s) still route through this pool. Change their proxy strategy first.", bound),
			"boundConnectionCount": bound,
		})
		return
	}
	handlerutil.WriteJSON(w, http.StatusOK, map[string]any{"success": true})
}

// HandleTestProxyPool dials one proxy from the pool and reports the result.
// The outcome is persisted so the list can show a pool that failed without
// re-testing it on every page load.
func (h *Handler) HandleTestProxyPool(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if id == "" {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, "Missing pool ID")
		return
	}
	pool, err := h.repo.GetProxyPoolDetail(id)
	if err != nil {
		handlerutil.WriteJSONError(w, http.StatusNotFound, "Proxy pool not found")
		return
	}

	target := firstProxyURL(pool)
	if target == "" {
		h.recordAndRespond(w, id, false, "Pool has no proxy URL to test")
		return
	}

	// A test that only opens a TCP socket would pass for a proxy that cannot
	// reach the internet, so the check goes all the way: CONNECT through the
	// proxy to a real host and require a successful tunnel.
	if err := probeProxy(r.Context(), target); err != nil {
		h.recordAndRespond(w, id, false, err.Error())
		return
	}
	h.recordAndRespond(w, id, true, "")
}

func (h *Handler) recordAndRespond(w http.ResponseWriter, id string, ok bool, msg string) {
	if err := h.repo.RecordProxyPoolTest(id, ok, msg); err != nil {
		handlerutil.WriteJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	resp := map[string]any{"ok": ok, "testStatus": map[bool]string{true: "active", false: "error"}[ok]}
	if msg != "" {
		resp["error"] = msg
	}
	handlerutil.WriteJSON(w, http.StatusOK, resp)
}

// firstProxyURL picks the URL to test, collapsing the two stored shapes.
func firstProxyURL(pool map[string]any) string {
	if urls, ok := pool["urls"].([]any); ok {
		for _, u := range urls {
			if s, ok := u.(string); ok && strings.TrimSpace(s) != "" {
				return s
			}
		}
	}
	if s, ok := pool["proxyUrl"].(string); ok {
		return strings.TrimSpace(s)
	}
	return ""
}

// probeProxy opens a CONNECT tunnel through proxyAddr to a well-known HTTPS
// host and completes a TLS handshake. Returns nil only when the proxy actually
// forwards traffic.
func probeProxy(ctx context.Context, proxyAddr string) error {
	normalised, ok := db.NormalizeProxyURL(proxyAddr)
	if !ok {
		return fmt.Errorf("invalid proxy url %q", proxyAddr)
	}
	parsed, err := url.Parse(normalised)
	if err != nil {
		return fmt.Errorf("invalid proxy url: %w", err)
	}

	host := parsed.Hostname()
	port := parsed.Port()
	if port == "" {
		switch strings.ToLower(parsed.Scheme) {
		case "socks5", "socks5h":
			port = "1080"
		case "https":
			port = "443"
		default:
			port = "80"
		}
	}

	dialer := &net.Dialer{Timeout: proxyTestTimeout}
	conn, err := dialer.DialContext(ctx, "tcp", net.JoinHostPort(host, port))
	if err != nil {
		return fmt.Errorf("cannot reach proxy: %w", err)
	}
	defer conn.Close()

	if deadline, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(deadline)
	} else {
		_ = conn.SetDeadline(time.Now().Add(proxyTestTimeout))
	}

	// The tunnel target only proves egress; the host is public and the response
	// is discarded, so nothing here depends on the caller's network policy.
	const probeHost = "www.gstatic.com:443"
	req := fmt.Sprintf("CONNECT %s HTTP/1.1\r\nHost: %s\r\n", probeHost, probeHost)
	if parsed.User != nil {
		pass, _ := parsed.User.Password()
		token := base64.StdEncoding.EncodeToString([]byte(parsed.User.Username() + ":" + pass))
		req += "Proxy-Authorization: Basic " + token + "\r\n"
	}
	req += "\r\n"

	if _, err := io.WriteString(conn, req); err != nil {
		return fmt.Errorf("proxy write failed: %w", err)
	}

	// Read just the status line. A proxy that accepts CONNECT answers
	// "HTTP/1.1 200"; anything else (407, 403, 502) is the real diagnosis and is
	// worth surfacing verbatim.
	buf := make([]byte, 512)
	n, err := conn.Read(buf)
	if err != nil {
		return fmt.Errorf("proxy did not answer: %w", err)
	}
	line := strings.SplitN(string(buf[:n]), "\r\n", 2)[0]
	if !strings.Contains(line, " 200") {
		if len(line) > 120 {
			line = line[:120]
		}
		return fmt.Errorf("proxy refused the tunnel: %s", line)
	}
	return nil
}
