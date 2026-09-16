package dashboard

import (
	"encoding/base64"
	"strings"
	"testing"
	"time"
)

func TestCreateAndVerifySessionToken(t *testing.T) {
	secret := []byte("test-secret")
	now := time.Unix(1_700_000_000, 0)

	token, err := CreateSessionToken(secret, now)
	if err != nil {
		t.Fatalf("CreateSessionToken: %v", err)
	}
	if !VerifySessionToken(secret, token, now.Add(time.Hour)) {
		t.Fatal("a freshly minted token must verify within its TTL")
	}
}

func TestVerifySessionTokenRejectsExpired(t *testing.T) {
	secret := []byte("test-secret")
	now := time.Unix(1_700_000_000, 0)
	token, _ := CreateSessionToken(secret, now)

	if VerifySessionToken(secret, token, now.Add(authSessionTTL+time.Minute)) {
		t.Fatal("an expired token must not verify")
	}
}

func TestVerifySessionTokenRejectsTampering(t *testing.T) {
	secret := []byte("test-secret")
	now := time.Unix(1_700_000_000, 0)
	token, _ := CreateSessionToken(secret, now)

	// Tamper with the payload while keeping the original signature: the HMAC
	// check must catch it.
	payload, sig, _ := strings.Cut(token, ".")
	forged := base64.RawURLEncoding.EncodeToString([]byte(`{"authenticated":true,"exp":9999999999}`))
	if VerifySessionToken(secret, forged+"."+sig, now) {
		t.Fatal("a token with a mismatched signature must not verify")
	}
	// And a wrong secret must not validate a genuine token either.
	if VerifySessionToken([]byte("other-secret"), payload+"."+sig, now) {
		t.Fatal("a token signed with another secret must not verify")
	}
}

func TestVerifySessionTokenRejectsGarbage(t *testing.T) {
	for _, tok := range []string{"", "no-dot", ".", "a.", ".b", "a.b.c"} {
		if VerifySessionToken([]byte("s"), tok, time.Now()) {
			t.Errorf("token %q must not verify", tok)
		}
	}
}

func TestVerifyPasswordWithHash(t *testing.T) {
	hasher := bcryptHasher{}
	hash, err := hasher.Hash("hunter2")
	if err != nil {
		t.Fatalf("Hash: %v", err)
	}
	if !VerifyPassword(hasher, hash, "hunter2") {
		t.Fatal("the correct password must verify against its hash")
	}
	if VerifyPassword(hasher, hash, "wrong") {
		t.Fatal("a wrong password must not verify")
	}
}

func TestVerifyPasswordFallsBackToDefault(t *testing.T) {
	hasher := bcryptHasher{}
	// No stored hash: the documented default applies.
	if !VerifyPassword(hasher, "", DefaultDashboardPassword) {
		t.Fatalf("the default password %q must work when no hash is set", DefaultDashboardPassword)
	}
	if VerifyPassword(hasher, "", "definitely-not-it") {
		t.Fatal("a wrong password must not pass the fallback path")
	}
}

func TestVerifyPasswordHonoursInitialPasswordEnv(t *testing.T) {
	t.Setenv("INITIAL_PASSWORD", "from-env")
	hasher := bcryptHasher{}
	if !VerifyPassword(hasher, "", "from-env") {
		t.Fatal("INITIAL_PASSWORD must override the built-in default")
	}
	if VerifyPassword(hasher, "", DefaultDashboardPassword) {
		t.Fatal("the built-in default must not work once INITIAL_PASSWORD is set")
	}
}
