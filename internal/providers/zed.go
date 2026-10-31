package providers

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"fmt"
	"net/url"
	"strings"
	"sync"
	"time"
)

// Zed's sign-in is not OAuth in the RFC sense: the desktop mints an RSA keypair,
// hands the base64url PKCS#1 public key to zed.dev in the browser URL, and
// zed.dev redirects back to a loopback port with
// `?user_id=…&access_token=<token encrypted to that public key>`. The client then
// decrypts with its private key and authenticates every RPC as
// `Authorization: <user_id> <token>`.
//
// Source of truth: crates/client/src/client.rs (native_app_signin) and
// crates/rpc/src/auth.rs (keypair, OAEP-SHA256 with PKCS#1 v1.5 fallback,
// BASE64_URL_SAFE = unpadded) in zed-industries/zed.
const (
	ZedSignInURL       = "https://zed.dev/native_app_signin"
	ZedSignInDoneURL   = "https://zed.dev/native_app_signin_succeeded"
	ZedSessionLifetime = 10 * time.Minute
)

// ZedSession is one in-flight sign-in: the keypair that will decrypt the token
// zed.dev encrypts, plus whatever the callback already delivered.
type ZedSession struct {
	ID        string
	PublicKey string // base64url PKCS#1 DER, as zed.dev expects
	UserID    string
	Token     string
	CreatedAt time.Time

	privateKey *rsa.PrivateKey
}

var (
	zedSessionsMu sync.Mutex
	zedSessions   = map[string]*ZedSession{}
)

// ZedNewSession mints a keypair and registers the pending sign-in.
func ZedNewSession() (*ZedSession, error) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		return nil, fmt.Errorf("zed: generate keypair: %w", err)
	}
	der := x509.MarshalPKCS1PublicKey(&key.PublicKey)
	idBytes := make([]byte, 24)
	if _, err := rand.Read(idBytes); err != nil {
		return nil, err
	}
	s := &ZedSession{
		ID:         base64.RawURLEncoding.EncodeToString(idBytes),
		PublicKey:  base64.RawURLEncoding.EncodeToString(der),
		CreatedAt:  time.Now(),
		privateKey: key,
	}
	zedSessionsMu.Lock()
	for id, old := range zedSessions {
		if time.Since(old.CreatedAt) > ZedSessionLifetime {
			delete(zedSessions, id)
		}
	}
	zedSessions[s.ID] = s
	zedSessionsMu.Unlock()
	return s, nil
}

// ZedGetSession returns a pending sign-in, if it is still alive.
func ZedGetSession(id string) *ZedSession {
	zedSessionsMu.Lock()
	defer zedSessionsMu.Unlock()
	s, ok := zedSessions[id]
	if !ok || time.Since(s.CreatedAt) > ZedSessionLifetime {
		delete(zedSessions, id)
		return nil
	}
	return s
}

// ResetZedSessions drops every pending sign-in (tests).
func ResetZedSessions() {
	zedSessionsMu.Lock()
	zedSessions = map[string]*ZedSession{}
	zedSessionsMu.Unlock()
}

// ZedSignInLink is the browser URL the operator opens. native_app_port is the
// loopback port zed.dev will redirect to; system_id is optional telemetry the
// desktop passes along.
func ZedSignInLink(port int, publicKey, systemID string) string {
	q := url.Values{}
	q.Set("native_app_port", fmt.Sprintf("%d", port))
	q.Set("native_app_public_key", publicKey)
	if systemID != "" {
		q.Set("system_id", systemID)
	}
	return ZedSignInURL + "?" + q.Encode()
}

// ZedComplete accepts the callback query (or the whole redirect URL an operator
// pasted from a browser that could not reach this host) and decrypts the token.
// zed.dev switched formats mid-flight, so OAEP-SHA256 is tried first and PKCS#1
// v1.5 second — exactly the order in crates/rpc/src/auth.rs.
func (s *ZedSession) ZedComplete(userID, encryptedToken string) error {
	userID = strings.TrimSpace(userID)
	encryptedToken = strings.TrimSpace(encryptedToken)
	if userID == "" || encryptedToken == "" {
		return fmt.Errorf("zed: callback is missing user_id or access_token")
	}
	raw, err := base64.RawURLEncoding.DecodeString(encryptedToken)
	if err != nil {
		// Tolerate padded base64 from a copy-pasted URL.
		raw, err = base64.URLEncoding.DecodeString(encryptedToken)
		if err != nil {
			return fmt.Errorf("zed: access_token is not base64url: %w", err)
		}
	}
	plain, err := rsa.DecryptOAEP(sha256.New(), rand.Reader, s.privateKey, raw, nil)
	if err != nil {
		plain, err = rsa.DecryptPKCS1v15(randomReader{}, s.privateKey, raw)
		if err != nil {
			return fmt.Errorf("zed: could not decrypt access_token with this sign-in's key: %w", err)
		}
	}
	s.UserID = userID
	s.Token = string(plain)
	return nil
}

// ZedParseCallback pulls user_id/access_token out of a redirect URL or a bare
// query string.
func ZedParseCallback(raw string) (userID, token string, err error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", "", fmt.Errorf("zed: empty callback")
	}
	q := ""
	switch {
	case strings.Contains(raw, "?"):
		u, perr := url.Parse(raw)
		if perr != nil {
			return "", "", fmt.Errorf("zed: could not parse the pasted URL: %w", perr)
		}
		q = u.RawQuery
	case strings.Contains(raw, "="):
		q = raw
	default:
		return "", "", fmt.Errorf("zed: paste the address the browser landed on")
	}
	values, perr := url.ParseQuery(q)
	if perr != nil {
		return "", "", fmt.Errorf("zed: could not parse the callback query: %w", perr)
	}
	userID = values.Get("user_id")
	token = values.Get("access_token")
	if userID == "" || token == "" {
		return "", "", fmt.Errorf("zed: callback has no user_id/access_token — copy the full 127.0.0.1 address")
	}
	return userID, token, nil
}

// randomReader adapts crypto/rand for DecryptPKCS1v15's blinding step while
// keeping the signature small.
type randomReader struct{}

func (randomReader) Read(p []byte) (int, error) { return rand.Read(p) }
