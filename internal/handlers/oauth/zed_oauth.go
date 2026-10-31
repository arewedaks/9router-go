package oauth

import (
	"io"
	"net"
	"net/http"
	"strings"
	"time"

	json "encoding/json/v2"

	"9router/proxy/internal/handlerutil"
	"9router/proxy/internal/providers"
)

// Zed signs in through zed.dev's native app flow (see providers/zed.go): the
// browser is sent to zed.dev with this server's public key, and zed.dev
// redirects to a loopback port with the access token encrypted to that key.
//
// Two ways in, because the real client assumes the browser runs on the same
// machine as the app:
//
//   - local install: this handler binds 127.0.0.1:<port> and completes by itself
//   - remote install (a managed host): the browser cannot reach the server's
//     loopback, so the operator pastes the address the browser landed on and the
//     decryption still happens here, with this sign-in's private key
const zedProviderID = "zed"

// HandleZedFlow reports the flow shape to the dashboard panel.
func (h *OAuthHandler) HandleZedFlow(w http.ResponseWriter, r *http.Request) {
	handlerutil.WriteJSON(w, http.StatusOK, map[string]any{
		"provider": zedProviderID,
		"kind":     "native_app",
		"startHint": "Sign in with your Zed account. The browser returns to a 127.0.0.1 address: " +
			"if this dashboard runs on the same machine it is captured automatically, otherwise paste that address below.",
	})
}

// HandleZedAuthorize mints the keypair, starts the loopback listener when it can,
// and returns the zed.dev URL.
//
// POST /api/oauth/zed/authorize  {"name":"…"}
func (h *OAuthHandler) HandleZedAuthorize(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name string `json:"name"`
	}
	body, _ := io.ReadAll(io.LimitReader(r.Body, 1<<16))
	_ = json.Unmarshal(body, &req)

	session, err := providers.ZedNewSession()
	if err != nil {
		handlerutil.WriteJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}

	// A loopback listener makes the local case hands-off. It is optional: on a
	// remote host the port is unreachable from the operator's browser, and the
	// paste path covers it.
	var port int
	var listener net.Listener
	if ln, lerr := net.Listen("tcp", "127.0.0.1:0"); lerr == nil {
		listener = ln
		port = ln.Addr().(*net.TCPAddr).Port
	} else {
		port = 0
	}
	if port == 0 {
		// zed.dev needs a port to redirect to; without a local listener the
		// operator's own machine answers instead, which is fine — the address is
		// pasted back here. 53682 mirrors the port ADCode's loopback uses.
		port = 53682
	}

	handlerutil.WriteJSON(w, http.StatusOK, map[string]any{
		"provider":    zedProviderID,
		"session":     session.ID,
		"port":        port,
		"signInUrl":   providers.ZedSignInLink(port, session.PublicKey, ""),
		"autoCapture": listener != nil,
	})

	if listener != nil {
		go serveZedCallback(listener, session)
	}
}

// serveZedCallback answers the one redirect zed.dev makes, records the result on
// the session and bounces the browser to the "sign-in succeeded" page.
func serveZedCallback(ln net.Listener, session *providers.ZedSession) {
	srv := &http.Server{
		ReadHeaderTimeout: 10 * time.Second,
		Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			q := r.URL.Query()
			if err := session.ZedComplete(q.Get("user_id"), q.Get("access_token")); err != nil {
				http.Error(w, "Zed sign-in could not be completed: "+err.Error(), http.StatusBadRequest)
				return
			}
			http.Redirect(w, r, providers.ZedSignInDoneURL, http.StatusFound)
		}),
	}
	// One redirect, then the port is no longer needed.
	go func() {
		time.Sleep(providers.ZedSessionLifetime)
		_ = srv.Close()
	}()
	_ = srv.Serve(ln)
}

// HandleZedStatus reports whether the callback already arrived, and stores the
// connection on the first successful poll.
//
// GET /api/oauth/zed/status?session=…
func (h *OAuthHandler) HandleZedStatus(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimSpace(r.URL.Query().Get("session"))
	session := providers.ZedGetSession(id)
	if session == nil {
		handlerutil.WriteJSONError(w, http.StatusGone, "this Zed sign-in expired — start again")
		return
	}
	if session.Token == "" {
		handlerutil.WriteJSON(w, http.StatusOK, map[string]any{"pending": true})
		return
	}
	// Claim the token before saving: the panel polls, and a second poll must not
	// create a second connection for the same account.
	token := session.Token
	session.Token = ""
	connID, err := h.saveZedConnection(session.UserID, token, "Zed "+session.UserID)
	if err != nil {
		session.Token = token
		handlerutil.WriteJSONError(w, http.StatusInternalServerError, "failed to save connection")
		return
	}
	handlerutil.WriteJSON(w, http.StatusOK, map[string]any{
		"success":    true,
		"connection": map[string]any{"id": connID, "provider": zedProviderID},
	})
}

// HandleZedExchange completes a sign-in from a pasted redirect address.
//
// POST /api/oauth/zed/exchange  {"session":"…","callback":"http://127.0.0.1:…/?user_id=…&access_token=…"}
func (h *OAuthHandler) HandleZedExchange(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Session  string `json:"session"`
		Callback string `json:"callback"`
	}
	body, _ := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err := json.Unmarshal(body, &req); err != nil {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	session := providers.ZedGetSession(strings.TrimSpace(req.Session))
	if session == nil {
		handlerutil.WriteJSONError(w, http.StatusGone, "this Zed sign-in expired — start again")
		return
	}
	userID, token, err := providers.ZedParseCallback(req.Callback)
	if err != nil {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := session.ZedComplete(userID, token); err != nil {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, err.Error())
		return
	}
	connID, err := h.saveZedConnection(session.UserID, session.Token, "Zed "+session.UserID)
	session.Token = ""
	if err != nil {
		handlerutil.WriteJSONError(w, http.StatusInternalServerError, "failed to save connection")
		return
	}
	handlerutil.WriteJSON(w, http.StatusOK, map[string]any{
		"success":    true,
		"connection": map[string]any{"id": connID, "provider": zedProviderID},
	})
}

// saveZedConnection stores what the RPC layer needs: the token, and the user id
// because Zed authenticates as `<user_id> <token>`, not as a bearer alone.
func (h *OAuthHandler) saveZedConnection(userID, token, name string) (string, error) {
	data, err := json.Marshal(map[string]any{
		"accessToken": token,
		// zedUserId is what the proxy lane reads; the executor joins it with the
		// token into the raw Authorization value.
		"zedUserId": userID,
	})
	if err != nil {
		return "", err
	}
	return h.insertOAuthConnection(zedProviderID, name, "", string(data))
}
