package executor

import (
	"9router/proxy/internal/proxy"
	"time"
)

// opencodeSessionHint resolves the upstream session a free-lane request
// travels with. The client's own session wins — without one, every request would
// mint a fresh upstream session, and the free tier accounts its quota per session:
// a fresh id per request burns through that budget and surfaces as 429
// FreeUsageLimitError with a growing retry-after, while the official CLI reuses
// one long-lived session per conversation.
func opencodeSessionHint(req *Request, free bool) string {
	if !free || req.SessionID != "" {
		return req.SessionID
	}
	key := "opencode:default"
	if req.ConnectionID != "" {
		key = "opencode:conn:" + req.ConnectionID
	}
	return stableOpenCodeSessionID(key)
}

// opencodeStableSessionKey reports the stable-session key a free-lane request
// resolves to when the client sent no session of its own, or "" — the marker
// that the upstream session is the router's to rotate when its budget is spent.
// The free flag must be the same one passed to opencodeSessionHint: a keyed lane's
// quota is accounted per key, so its session is never rotated.
func opencodeStableSessionKey(req *Request, free bool) string {
	if !free || req.SessionID != "" {
		return ""
	}
	if req.ConnectionID != "" {
		return "opencode:conn:" + req.ConnectionID
	}
	return "opencode:default"
}

// rotateOpencodeStableSession builds the rotation hook forwardOpenCodeRotating
// calls when the gate reports the stable session's budget spent: expire the
// session that just failed and install a freshly minted one via install. A nil key
// (a client-supplied session) yields a nil hook, so no rotation is attempted.
func rotateOpencodeStableSession(stableKey, failed string, install func(session string)) func() {
	if stableKey == "" {
		return nil
	}
	return func() {
		expireStableOpenCodeSession(stableKey, failed)
		if fresh := stableOpenCodeSessionID(stableKey); fresh != failed {
			install(fresh)
		}
	}
}

// --- Stable session store (executor-local, matching luqman-v1 proxy package API) ---

var (
	ocStableMu  = &ocStableMuType{}
	ocStableIDs = map[string]stableOCEntry{}
)

type ocStableMuType struct{ ch chan struct{} }

func init()                       { ocStableMu.ch = make(chan struct{}, 1); ocStableMu.ch <- struct{}{} }
func (m *ocStableMuType) Lock()   { <-m.ch }
func (m *ocStableMuType) Unlock() { m.ch <- struct{}{} }

type stableOCEntry struct {
	id      string
	expires int64
}

const ocStableTTL = 20 * 60 // 20 minutes

func ocNow() int64 { return time.Now().Unix() }

func stableOpenCodeSessionID(key string) string {
	now := ocNow()
	ocStableMu.Lock()
	defer ocStableMu.Unlock()
	if e, ok := ocStableIDs[key]; ok && e.expires > now && e.id != "" {
		e.expires = now + ocStableTTL
		ocStableIDs[key] = e
		return e.id
	}
	id := proxy.GenerateOpenCodeSessionID()
	ocStableIDs[key] = stableOCEntry{id: id, expires: now + ocStableTTL}
	return id
}

func expireStableOpenCodeSession(key, id string) bool {
	ocStableMu.Lock()
	defer ocStableMu.Unlock()
	if e, ok := ocStableIDs[key]; ok && e.id == id {
		delete(ocStableIDs, key)
		return true
	}
	return false
}
