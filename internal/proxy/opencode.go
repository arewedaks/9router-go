package proxy

import (
	"crypto/rand"
	"encoding/hex"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	// DefaultOpenCodeUA is the official OpenCode fingerprint User-Agent.
	//
	// v1 ("opencode/1.18.31 ai-sdk/...") still answered 200 when measured on
	// 2026-10-22, but the v2 shape is what the official 2.0.x CLI sends and what
	// the gate has been tightening towards. The v2 gate matches this string
	// whole — any prefix or suffix (an attribution UA, a runtime tag) stops it
	// being "a v2 client" — so it is emitted verbatim and alone.
	DefaultOpenCodeUA = "opencode/latest/2.0.29/cli"

	// DefaultOpenCodeV1UA is the pre-v2 identity kept by the keyed lanes
	// (opencode-zen, opencode-go), which upstream still fingerprints as v1.
	DefaultOpenCodeV1UA = "opencode/1.18.31 ai-sdk/provider-utils/4.0.46 runtime/bun/1.3.14"

	base62Chars = "0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz"
)

// OpenCodeSessionRegex matches canonical OpenCode session IDs: ses_ + 12 hex + 14 Base62 (30 chars).
var OpenCodeSessionRegex = regexp.MustCompile(`^ses_[0-9a-f]{12}[0-9A-Za-z]{14}$`)

// openCodeDescendingTimePart matches the encoding half of a canonical session
// id: the 12 hex digits must carry the descending timestamp layout
// ~(ms<<12 | counter) with counter 1-63, so the low 12 bits land in 0xfc0-0xfff.
// An ascending ts<<12 value or a sha256 digest is refused even when the ses_
// shape is otherwise correct.
var openCodeDescendingTimePart = regexp.MustCompile(`^[0-9a-f]{9}f[c-f][0-9a-f]$`)

// OpenCodeRequestRegex matches canonical OpenCode request IDs: msg_ + 12 hex + 14 Base62 (30 chars).
var OpenCodeRequestRegex = regexp.MustCompile(`^msg_[0-9a-f]{12}[0-9A-Za-z]{14}$`)

var (
	sessionMu      sync.Mutex
	lastTimestamp  int64
	sessionCounter uint64
)

func unstableRandomBase62(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	var sb strings.Builder
	sb.Grow(n)
	for _, v := range b {
		sb.WriteByte(base62Chars[int(v)%62])
	}
	return sb.String()
}

// GenerateOpenCodeSessionID generates a descending canonical OpenCode session ID (30 chars).
func GenerateOpenCodeSessionID() string {
	sessionMu.Lock()
	now := time.Now().UnixMilli()
	if now != lastTimestamp {
		lastTimestamp = now
		sessionCounter = 0
	}
	sessionCounter++
	counter := sessionCounter
	sessionMu.Unlock()

	// The 12 hex chars are the low 48 bits of ~(ms*4096 + counter); Zen decodes
	// exactly that, so the mask has to be applied before the bytes are split.
	current := uint64(now)*0x1000 + (counter & 0xfff)
	value := ^current & 0xffffffffffff
	var timeBytes [6]byte
	for i := range 6 {
		timeBytes[i] = byte((value >> (40 - 8*uint64(i))) & 0xff)
	}
	return "ses_" + hex.EncodeToString(timeBytes[:]) + unstableRandomBase62(14)
}

// GenerateOpenCodeRequestID generates a canonical OpenCode request ID (30 chars).
func GenerateOpenCodeRequestID() string {
	now := time.Now().UnixMilli()
	current := uint64(now)*0x1000 + 1
	var timeBytes [6]byte
	for i := range 6 {
		timeBytes[i] = byte((current >> (40 - 8*uint64(i))) & 0xff)
	}
	return "msg_" + hex.EncodeToString(timeBytes[:]) + unstableRandomBase62(14)
}

// GenerateOpenCodeProjectID generates a canonical 40-char hex project ID (PR #4111).
func GenerateOpenCodeProjectID() string {
	b := make([]byte, 20)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// TranslateOpenCodeSessionID deterministically maps foreign sessions to valid 30-char canonical IDs.
//
// The 12 hex prefix of a ses_ id is not free-form: Zen decodes it back to
// `~(ms*4096 + n)` and checks that the timestamp is recent. Deriving those 12
// hex from a hash of the downstream id (which is what this function used to do)
// fails that check and answers 403 FreeTierError, which is why oc/* free models
// failed for every non-OpenCode client even with perfect tools and headers
// (upstream PR #4791). Mint a canonical id instead and reuse it per downstream
// session so a conversation keeps one stable session.
func TranslateOpenCodeSessionID(sessionID, clientTool string) string {
	trimmed := strings.TrimSpace(sessionID)
	if OpenCodeSessionRegex.MatchString(trimmed) {
		return trimmed
	}
	key := clientTool + "\x00" + trimmed
	sessionCacheMu.Lock()
	defer sessionCacheMu.Unlock()
	if e, ok := translatedSessions[key]; ok && time.Since(e.at) < sessionCacheTTL {
		return e.id
	}
	// Drop expired entries off the front rather than growth-bounding by scan.
	for k, e := range translatedSessions {
		if time.Since(e.at) >= sessionCacheTTL {
			delete(translatedSessions, k)
		}
	}
	if len(translatedSessions) >= sessionCacheMax {
		for k := range translatedSessions {
			delete(translatedSessions, k)
			break
		}
	}
	id := GenerateOpenCodeSessionID()
	translatedSessions[key] = translatedSession{id: id, at: time.Now()}
	return id
}

var (
	sessionCacheMu     sync.Mutex
	translatedSessions = map[string]translatedSession{}
)

const (
	sessionCacheTTL = time.Hour
	sessionCacheMax = 1000
)

type translatedSession struct {
	id string
	at time.Time
}

var opencodeUARegex = regexp.MustCompile(`(?i)opencode/(\d+)\.(\d+)(?:\.(\d+))?`)

// HasValidOpenCodeVersion checks if a User-Agent is opencode >= 1.17.0.
func HasValidOpenCodeVersion(ua string) bool {
	matches := opencodeUARegex.FindStringSubmatch(ua)
	if len(matches) < 3 {
		return false
	}
	major, err := strconv.Atoi(matches[1])
	if err != nil {
		return false
	}
	minor, err := strconv.Atoi(matches[2])
	if err != nil {
		return false
	}
	return major > 1 || (major == 1 && minor >= 17)
}

// BuildOpenCodeHeaders generates the official OpenCode fingerprint headers to prevent 403 FreeTierError.
func BuildOpenCodeHeaders(rawHeaders map[string]string, sessionID string, isStream bool) map[string]string {
	var session string
	if sessionID != "" {
		session = TranslateOpenCodeSessionID(sessionID, "")
	} else {
		session = GenerateOpenCodeSessionID()
	}

	// Do NOT add x-api-key: public. The gate rejects a request that carries it:
	// measured 2026-10-15, identical headers and body answered 200 without the
	// header and 403 FreeTierError with it added back. The officia client sends
	// only Authorization.
	//
	// v2 sends the session id under four twin header names and defaults the
	// project to the literal "global". x-opencode-request is a v1-only header
	// and is not emitted here: a v2 fingerprint does not carry it.
	res := map[string]string{
		"Content-Type":          "application/json",
		"Authorization":         "Bearer public",
		"User-Agent":            DefaultOpenCodeUA,
		"x-opencode-client":     "cli",
		"x-opencode-session":    session,
		"x-opencode-session-id": session,
		"x-session-affinity":    session,
		"x-session-id":          session,
		"x-opencode-project":    "global",
	}
	if isStream {
		res["Accept"] = "text/event-stream"
	} else {
		res["Accept"] = "*/*"
	}
	for k, v := range rawHeaders {
		lk := strings.ToLower(k)
		if lk == "user-agent" && HasValidOpenCodeVersion(v) {
			res["User-Agent"] = v
		}
		if lk == "x-opencode-session" {
			trimmed := strings.TrimSpace(v)
			if OpenCodeSessionRegex.MatchString(trimmed) {
				// Keep the caller's id, but re-spread it over all four twin
				// headers so a v2 request never carries a half-set session.
				res["x-opencode-session"] = trimmed
				res["x-opencode-session-id"] = trimmed
				res["x-session-affinity"] = trimmed
				res["x-session-id"] = trimmed
			} else if trimmed != "" {
				translated := TranslateOpenCodeSessionID(trimmed, "")
				res["x-opencode-session"] = translated
				res["x-opencode-session-id"] = translated
				res["x-session-affinity"] = translated
				res["x-session-id"] = translated
			}
		}
		// x-opencode-request is deliberately not forwarded: it is a v1-only
		// header and has no place in a v2 fingerprint.
		if lk == "x-opencode-client" || lk == "x-relay-target" || lk == "x-relay-path" {
			res[k] = v
		}
		if lk == "x-opencode-project" {
			trimmed := strings.TrimSpace(v)
			if trimmed != "" && trimmed != "global" {
				res["x-opencode-project"] = trimmed
			}
		}
	}
	return res
}
