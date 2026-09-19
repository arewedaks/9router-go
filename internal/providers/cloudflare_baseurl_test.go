package providers

import "testing"

// Cloudflare's chat URL contains the account id, so it can only be built from
// connection data. The registry previously interpolated the CLOUDFLARE_ACCOUNT_ID
// environment variable, which is empty on a normal install: requests went to
// /accounts//ai/v1/chat/completions and 404ed even though the operator had
// stored the account id on the connection.
func TestAccountScopedBaseURLCloudflare(t *testing.T) {
	cases := []struct {
		name string
		prov string
		data map[string]any
		want string
	}{
		{
			name: "camelCase accountId",
			prov: "cloudflare-ai",
			data: map[string]any{"accountId": "abc123"},
			want: "https://api.cloudflare.com/client/v4/accounts/abc123/ai/v1/chat/completions",
		},
		{
			name: "snake_case account_id",
			prov: "cloudflare-ai",
			data: map[string]any{"account_id": "snake"},
			want: "https://api.cloudflare.com/client/v4/accounts/snake/ai/v1/chat/completions",
		},
		{
			name: "padded value is trimmed",
			prov: "cloudflare-ai",
			data: map[string]any{"accountId": "  pad  "},
			want: "https://api.cloudflare.com/client/v4/accounts/pad/ai/v1/chat/completions",
		},
		{
			name: "missing account id yields no URL",
			prov: "cloudflare-ai",
			data: map[string]any{},
			want: "",
		},
		{
			name: "nil data yields no URL",
			prov: "cloudflare-ai",
			data: nil,
			want: "",
		},
		{
			name: "other providers are untouched",
			prov: "openai",
			data: map[string]any{"accountId": "abc"},
			want: "",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := AccountScopedBaseURL(tc.prov, tc.data); got != tc.want {
				t.Errorf("got %q, want %q", got, tc.want)
			}
		})
	}
}

// An empty base URL must never be produced: that is what made the old
// env-var-derived URL silently hit /accounts//ai/... instead of failing loudly.
func TestAccountScopedBaseURLNeverEmptyString(t *testing.T) {
	if got := AccountScopedBaseURL("cloudflare-ai", map[string]any{"accountId": ""}); got != "" {
		t.Errorf("empty account id should return \"\", got %q", got)
	}
	if got := KnownProviders["cloudflare-ai"].BaseURL; got != "" {
		t.Errorf("registry base URL for cloudflare-ai = %q, want empty (built per connection)", got)
	}
}
