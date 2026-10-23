package oauth

import "testing"

// A stored Perch connection goes stale one hour after sign-in. Without a
// refresher the model call answers
//
//	401 {"ok":false,"error":"Unauthorized","errorCode":"not_authenticated"}
//
// and the harness reports it as a rejected API key, which points the operator
// at the wrong cause. Every spelling a connection can be stored under must
// resolve to the refresher.
func TestPerchRefresherRegisteredForEveryAlias(t *testing.T) {
	for _, p := range []string{"perch", "pch", "perchai"} {
		if Get(p) == nil {
			t.Errorf("no refresher registered for %q", p)
		}
	}
}

// Supabase rotates the refresh token on every exchange, so storing the old one
// would make the very next refresh fail with "invalid refresh token". Guards the
// one field that is easy to drop when copying a token response.
func TestPerchRefresherRequiresRefreshToken(t *testing.T) {
	if _, err := RefreshPerch(nil, &Params{}); err == nil {
		t.Error("an empty params must not silently succeed")
	}
	if _, err := RefreshPerch(t.Context(), &Params{}); err == nil {
		t.Error("a missing refresh token must be reported, not exchanged")
	}
}
