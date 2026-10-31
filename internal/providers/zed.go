package providers

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	json "encoding/json/v2"
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
	ZedCloudURL        = "https://cloud.zed.dev"
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

// Zed's hosted models are not reachable with the sign-in token directly. The
// ladder, straight from crates/cloud_api_client and
// crates/language_models_cloud:
//
//	GET  /client/users/me      `<user_id> <access_token>` → organizations
//	POST /client/llm_tokens    `<user_id> <access_token>` + {"organization_id":…} → token
//	GET  /models               `Bearer <llm token>`      → models[]
//
// Sending the sign-in token to /models answers 401, which is exactly the error a
// naive implementation reports as "the account may not have hosted models".
var (
	ZedCloudBase = ZedCloudURL

	zedLLMMu    sync.Mutex
	zedLLMToken = map[string]zedLLMEntry{}
)

type zedLLMEntry struct {
	token     string
	expiresAt time.Time
}

// zedLLMTokenKey scopes the cache to one account, without keeping the raw token
// in a map key.
func zedLLMTokenKey(userID, accessToken string) string {
	sum := sha256.Sum256([]byte(userID + "\x00" + accessToken))
	return userID + ":" + base64.RawURLEncoding.EncodeToString(sum[:12])
}

// ResetZedLLMTokens drops cached LLM tokens (tests).
func ResetZedLLMTokens() {
	zedLLMMu.Lock()
	zedLLMToken = map[string]zedLLMEntry{}
	zedLLMMu.Unlock()
}

// ZedLLMToken returns the short-lived token the LLM API accepts, minting it from
// the sign-in credential when the cache is cold or stale.
func ZedLLMToken(ctx context.Context, client *http.Client, userID, accessToken string) (string, error) {
	userID = strings.TrimSpace(userID)
	accessToken = strings.TrimSpace(accessToken)
	if userID == "" || accessToken == "" {
		return "", fmt.Errorf("zed: sign in again — the connection has no user id or token")
	}
	key := zedLLMTokenKey(userID, accessToken)
	zedLLMMu.Lock()
	if e, ok := zedLLMToken[key]; ok && time.Now().Before(e.expiresAt) {
		zedLLMMu.Unlock()
		return e.token, nil
	}
	zedLLMMu.Unlock()

	auth := userID + " " + accessToken
	orgID, err := zedOrganizationID(ctx, client, auth)
	if err != nil {
		return "", err
	}
	body, err := json.Marshal(map[string]string{"organization_id": orgID})
	if err != nil {
		return "", err
	}
	raw, status, err := zedCall(ctx, client, http.MethodPost, ZedCloudBase+"/client/llm_tokens", auth, body)
	if err != nil {
		return "", err
	}
	if status != http.StatusOK {
		return "", fmt.Errorf("zed: llm token HTTP %d: %s", status, zedSnippet(raw))
	}
	var parsed struct {
		Token string `json:"token"`
	}
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return "", fmt.Errorf("zed: decode llm token: %w", err)
	}
	if parsed.Token == "" {
		return "", fmt.Errorf("zed: the server returned no llm token")
	}
	// Zed does not publish the lifetime; an hour is what the client assumes and
	// a 401 triggers a refresh anyway.
	zedLLMMu.Lock()
	zedLLMToken[key] = zedLLMEntry{token: parsed.Token, expiresAt: time.Now().Add(55 * time.Minute)}
	zedLLMMu.Unlock()
	return parsed.Token, nil
}

// ZedInvalidateLLMToken drops the cached token so the next call mints a new one.
func ZedInvalidateLLMToken(userID, accessToken string) {
	zedLLMMu.Lock()
	delete(zedLLMToken, zedLLMTokenKey(strings.TrimSpace(userID), strings.TrimSpace(accessToken)))
	zedLLMMu.Unlock()
}

// zedOrganizationID picks the organization the LLM token is minted for: the
// account's default when the server names one, otherwise the personal org.
func zedOrganizationID(ctx context.Context, client *http.Client, auth string) (string, error) {
	raw, status, err := zedCall(ctx, client, http.MethodGet, ZedCloudBase+"/client/users/me", auth, nil)
	if err != nil {
		return "", err
	}
	if status != http.StatusOK {
		return "", fmt.Errorf("zed: /client/users/me HTTP %d: %s", status, zedSnippet(raw))
	}
	var me struct {
		DefaultOrganizationID string `json:"default_organization_id"`
		Organizations         []struct {
			ID         string `json:"id"`
			IsPersonal bool   `json:"is_personal"`
		} `json:"organizations"`
	}
	if err := json.Unmarshal(raw, &me); err != nil {
		return "", fmt.Errorf("zed: decode account: %w", err)
	}
	if me.DefaultOrganizationID != "" {
		return me.DefaultOrganizationID, nil
	}
	for _, org := range me.Organizations {
		if org.IsPersonal && org.ID != "" {
			return org.ID, nil
		}
	}
	if len(me.Organizations) > 0 && me.Organizations[0].ID != "" {
		return me.Organizations[0].ID, nil
	}
	return "", fmt.Errorf("zed: the account has no organization to mint an LLM token for")
}

// zedCall performs one authenticated cloud request.
func zedCall(ctx context.Context, client *http.Client, method, url, auth string, body []byte) ([]byte, int, error) {
	if client == nil {
		client = &http.Client{Timeout: 30 * time.Second}
	}
	if ctx == nil {
		ctx = context.Background()
	}
	var reader io.Reader
	if body != nil {
		reader = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, url, reader)
	if err != nil {
		return nil, 0, err
	}
	req.Header.Set("Authorization", auth)
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	return raw, resp.StatusCode, nil
}

// zedSnippet keeps error text short and free of credentials.
func zedSnippet(raw []byte) string {
	s := strings.TrimSpace(string(raw))
	if len(s) > 160 {
		s = s[:160] + "…"
	}
	return s
}
