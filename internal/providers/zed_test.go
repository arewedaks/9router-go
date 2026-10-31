package providers

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"strings"
	"testing"
)

// The whole Zed flow rests on one property: the token zed.dev encrypts with the
// public key we published must decrypt with the private key we kept, in both
// padding formats the client accepts (OAEP-SHA256 first, PKCS#1 v1.5 fallback).
func TestZedSessionDecryptsBothPaddingFormats(t *testing.T) {
	ResetZedSessions()
	t.Cleanup(ResetZedSessions)

	for _, tc := range []struct {
		name    string
		encrypt func(*testing.T, *rsa.PublicKey, []byte) []byte
	}{
		{"oaep-sha256", func(t *testing.T, pub *rsa.PublicKey, msg []byte) []byte {
			out, err := rsa.EncryptOAEP(sha256.New(), rand.Reader, pub, msg, nil)
			if err != nil {
				t.Fatalf("encrypt oaep: %v", err)
			}
			return out
		}},
		{"pkcs1v15", func(t *testing.T, pub *rsa.PublicKey, msg []byte) []byte {
			out, err := rsa.EncryptPKCS1v15(rand.Reader, pub, msg)
			if err != nil {
				t.Fatalf("encrypt pkcs1: %v", err)
			}
			return out
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, err := ZedNewSession()
			if err != nil {
				t.Fatalf("new session: %v", err)
			}
			der, err := base64.RawURLEncoding.DecodeString(s.PublicKey)
			if err != nil {
				t.Fatalf("public key is not base64url: %v", err)
			}
			pub, err := x509.ParsePKCS1PublicKey(der)
			if err != nil {
				t.Fatalf("public key is not PKCS#1 DER: %v", err)
			}
			// 2048 bits is what the Zed client generates.
			if pub.N.BitLen() != 2048 {
				t.Errorf("key is %d bits, want 2048", pub.N.BitLen())
			}

			cipherText := base64.RawURLEncoding.EncodeToString(tc.encrypt(t, pub, []byte("zed-access-token")))
			if err := s.ZedComplete("user-123", cipherText); err != nil {
				t.Fatalf("complete: %v", err)
			}
			if s.Token != "zed-access-token" || s.UserID != "user-123" {
				t.Fatalf("session = %q/%q", s.UserID, s.Token)
			}
		})
	}
}

// A token encrypted to a different keypair must be rejected rather than stored.
func TestZedSessionRejectsForeignToken(t *testing.T) {
	ResetZedSessions()
	t.Cleanup(ResetZedSessions)

	s, err := ZedNewSession()
	if err != nil {
		t.Fatalf("new session: %v", err)
	}
	other, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("key: %v", err)
	}
	sealed, err := rsa.EncryptOAEP(sha256.New(), rand.Reader, &other.PublicKey, []byte("token"), nil)
	if err != nil {
		t.Fatalf("encrypt: %v", err)
	}
	if err := s.ZedComplete("user-1", base64.RawURLEncoding.EncodeToString(sealed)); err == nil {
		t.Fatal("a token sealed to another key was accepted")
	}
	if s.Token != "" {
		t.Fatalf("token stored after a failed decrypt: %q", s.Token)
	}
}

// The sign-in URL must carry the loopback port and the key, which is what
// zed.dev reads.
func TestZedSignInLinkCarriesKeyAndPort(t *testing.T) {
	s, err := ZedNewSession()
	if err != nil {
		t.Fatalf("new session: %v", err)
	}
	link := ZedSignInLink(53682, s.PublicKey, "sys-1")
	for _, want := range []string{"https://zed.dev/native_app_signin?", "native_app_port=53682", "native_app_public_key=", "system_id=sys-1"} {
		if !strings.Contains(link, want) {
			t.Errorf("sign-in link missing %q: %s", want, link)
		}
	}
}

// Pasting the address the browser landed on must yield both fields, whether the
// operator copies the whole URL or only the query.
func TestZedParseCallbackAcceptsURLOrQuery(t *testing.T) {
	for _, raw := range []string{
		"http://127.0.0.1:53682/?user_id=u1&access_token=tok%2Benc",
		"http://localhost:53682/?user_id=u1&access_token=tok%2Benc",
		"user_id=u1&access_token=tok%2Benc",
	} {
		userID, token, err := ZedParseCallback(raw)
		if err != nil {
			t.Fatalf("%q: %v", raw, err)
		}
		if userID != "u1" || token != "tok+enc" {
			t.Errorf("%q → %q/%q", raw, userID, token)
		}
	}
	if _, _, err := ZedParseCallback("http://127.0.0.1:53682/"); err == nil {
		t.Error("a callback without parameters was accepted")
	}
}
