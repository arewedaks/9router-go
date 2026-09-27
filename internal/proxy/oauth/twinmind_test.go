package oauth

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestRefreshTwinmindExchangesRefreshTokenForIDToken(t *testing.T) {
	var gotBody, gotCT string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			t.Errorf("parse form: %v", err)
		}
		gotBody = r.PostForm.Get("refresh_token")
		gotCT = r.Header.Get("Content-Type")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id_token":"eyJ.new-id-token","refresh_token":"rotated-refresh","expires_in":"3600"}`))
	}))
	defer srv.Close()

	old := twinmindTokenURLForTest
	twinmindTokenURLForTest = srv.URL
	defer func() { twinmindTokenURLForTest = old }()

	res, err := RefreshTwinmind(context.Background(), &Params{
		RefreshToken: "stored-refresh-token",
		Client:       srv.Client(),
	})
	if err != nil {
		t.Fatalf("RefreshTwinmind: %v", err)
	}
	if gotBody != "stored-refresh-token" {
		t.Errorf("sent refresh_token = %q", gotBody)
	}
	if !strings.Contains(gotCT, "application/x-www-form-urlencoded") {
		t.Errorf("Content-Type = %q, want the form encoding securetoken requires", gotCT)
	}
	// The ID token is the chat credential, so it lands in AccessToken.
	if res.AccessToken != "eyJ.new-id-token" {
		t.Errorf("AccessToken = %q, want the new id_token", res.AccessToken)
	}
	// Google may rotate the refresh token; the result must carry it so the
	// store keeps the credential alive.
	if res.RefreshToken != "rotated-refresh" {
		t.Errorf("RefreshToken = %q, want the rotated value", res.RefreshToken)
	}
	if res.ExpiresIn <= 0 {
		t.Errorf("ExpiresIn = %d, want the one-hour lifetime", res.ExpiresIn)
	}
}

func TestRefreshTwinmindKeepsStoredRefreshTokenWhenNotRotated(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"id_token":"t2","expires_in":"3600"}`))
	}))
	defer srv.Close()
	old := twinmindTokenURLForTest
	twinmindTokenURLForTest = srv.URL
	defer func() { twinmindTokenURLForTest = old }()

	res, err := RefreshTwinmind(context.Background(), &Params{
		RefreshToken: "keep-me",
		Client:       srv.Client(),
	})
	if err != nil {
		t.Fatalf("RefreshTwinmind: %v", err)
	}
	// BuildConnectionUpdate drops an empty RefreshToken, so leaving it empty is
	// what preserves the stored credential across a non-rotating exchange.
	if res.RefreshToken != "" {
		t.Errorf("RefreshToken = %q, want empty so the stored token is kept", res.RefreshToken)
	}
}

func TestRefreshTwinmindRejectsMissingCredential(t *testing.T) {
	if _, err := RefreshTwinmind(context.Background(), &Params{}); err == nil {
		t.Fatal("expected an error when no refresh token is available")
	}
}
