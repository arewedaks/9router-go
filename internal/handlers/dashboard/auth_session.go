// Package dashboard: dashboard login/session support ported from VansRouter.
//
// VansRouter replaces the "paste an API key" flow with a password + cookie
// session: the password is a bcrypt hash in settings, and a successful login
// mints a signed JWT stored in an httpOnly cookie. This file ports that model to
// Go without pulling in a JWT library — the token format is a compact
// `base64(payload).base64(hmac)` pair, which is functionally equivalent for a
// server-side session cookie (the client never inspects it) and keeps the
// dashboard dependency-light.
//
// API keys are deliberately NOT removed: LLM clients still authenticate to the
// proxy with `Authorization: Bearer sk-...`. Only the *dashboard* login changes.
package dashboard

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	json "encoding/json/v2"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"golang.org/x/crypto/bcrypt"
)

const (
	// authCookieName is the session cookie, named to match VansRouter so an
	// operator switching between the two dashboards keeps the same habit.
	authCookieName = "auth_token"
	// authSessionTTL bounds how long a minted cookie stays valid.
	authSessionTTL = 24 * time.Hour
	// DefaultDashboardPassword is the fallback used when no password has been
	// set. VansRouter uses the same literal, and so does this port — changing it
	// would silently lock out anyone following the original docs.
	DefaultDashboardPassword = "123456"
	// jwtSecretFileName is the on-disk secret location under the data dir.
	jwtSecretFileName = "jwt-secret"
)

// ErrInvalidCredentials is returned for both a wrong password and a malformed
// session, so callers cannot distinguish them (and neither can an attacker).
var ErrInvalidCredentials = errors.New("invalid credentials")

// sessionClaims is the payload embedded in the session token. It mirrors the
// subset of VansRouter's JWT claims that matter here.
type sessionClaims struct {
	Authenticated bool  `json:"authenticated"`
	IssuedAt      int64 `json:"iat"`
	ExpiresAt     int64 `json:"exp"`
}

// PasswordHasher hashes and verifies dashboard passwords. It is a field on the
// Handler (rather than a bare function call) so tests can substitute a cheap
// hash without weakening the production cost factor.
type PasswordHasher interface {
	Hash(password string) (string, error)
	Compare(hash, password string) bool
}

// bcryptHasher is the production hasher (bcrypt cost 10, same as VansRouter).
type bcryptHasher struct{}

func (bcryptHasher) Hash(password string) (string, error) {
	b, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	return string(b), err
}

func (bcryptHasher) Compare(hash, password string) bool {
	return bcrypt.CompareHashAndPassword([]byte(hash), []byte(password)) == nil
}

// VerifyPassword checks a candidate password against the stored hash, falling
// back to the default (or INITIAL_PASSWORD) when no hash has been set yet.
func VerifyPassword(hasher PasswordHasher, storedHash, candidate string) bool {
	if storedHash != "" {
		return hasher.Compare(storedHash, candidate)
	}
	initial := strings.TrimSpace(os.Getenv("INITIAL_PASSWORD"))
	if initial == "" {
		initial = DefaultDashboardPassword
	}
	// Constant-time compare so the fallback path does not leak length/prefix.
	return subtle.ConstantTimeCompare([]byte(candidate), []byte(initial)) == 1
}

// LoadSessionSecret returns the HMAC key used to sign session cookies.
// Precedence mirrors VansRouter: env JWT_SECRET wins, otherwise a generated
// secret is persisted under the data dir with 0600 permissions so restarts do
// not invalidate every session.
func LoadSessionSecret(dataDir string) ([]byte, error) {
	if env := strings.TrimSpace(os.Getenv("JWT_SECRET")); env != "" {
		return []byte(env), nil
	}
	if dataDir == "" {
		return nil, fmt.Errorf("session: no data dir and no JWT_SECRET")
	}
	path := filepath.Join(dataDir, jwtSecretFileName)
	if b, err := os.ReadFile(path); err == nil {
		if s := strings.TrimSpace(string(b)); s != "" {
			return []byte(s), nil
		}
	}
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		return nil, fmt.Errorf("session: create data dir: %w", err)
	}
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return nil, fmt.Errorf("session: generate secret: %w", err)
	}
	secret := []byte(hexEncode(buf))
	if err := os.WriteFile(path, secret, 0o600); err != nil {
		return nil, fmt.Errorf("session: persist secret: %w", err)
	}
	return secret, nil
}

// hexEncode avoids importing encoding/hex just for one call site.
func hexEncode(b []byte) string {
	const digits = "0123456789abcdef"
	out := make([]byte, 0, len(b)*2)
	for _, c := range b {
		out = append(out, digits[c>>4], digits[c&0x0f])
	}
	return string(out)
}

// CreateSessionToken mints a signed session token valid for authSessionTTL.
func CreateSessionToken(secret []byte, now time.Time) (string, error) {
	claims := sessionClaims{
		Authenticated: true,
		IssuedAt:      now.Unix(),
		ExpiresAt:     now.Add(authSessionTTL).Unix(),
	}
	payload, err := json.Marshal(claims)
	if err != nil {
		return "", fmt.Errorf("session: marshal claims: %w", err)
	}
	encPayload := base64.RawURLEncoding.EncodeToString(payload)
	return encPayload + "." + signSessionPayload(secret, encPayload), nil
}

// VerifySessionToken reports whether a token is well formed, correctly signed,
// and unexpired.
func VerifySessionToken(secret []byte, token string, now time.Time) bool {
	encPayload, sig, ok := strings.Cut(token, ".")
	if !ok || encPayload == "" || sig == "" {
		return false
	}
	expected := signSessionPayload(secret, encPayload)
	if subtle.ConstantTimeCompare([]byte(sig), []byte(expected)) != 1 {
		return false
	}
	payload, err := base64.RawURLEncoding.DecodeString(encPayload)
	if err != nil {
		return false
	}
	var claims sessionClaims
	if err := json.Unmarshal(payload, &claims); err != nil {
		return false
	}
	if !claims.Authenticated {
		return false
	}
	return now.Unix() < claims.ExpiresAt
}

func signSessionPayload(secret []byte, encPayload string) string {
	mac := hmac.New(sha256.New, secret)
	mac.Write([]byte(encPayload))
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}
