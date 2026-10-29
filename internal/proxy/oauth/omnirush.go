package oauth

import (
	"context"

	"9router/proxy/internal/providers"
)

func init() {
	Register("omnirush", refreshOmniRush)
}

// refreshOmniRush rotates the device tokens. The standard OAuth2 refresher does
// not fit: OmniRush takes a JSON body carrying only `refresh_token` and answers
// with a rotated pair — the presented refresh token stops working, so the new
// one has to be persisted or the next refresh is refused.
func refreshOmniRush(ctx context.Context, p *Params) (*TokenResult, error) {
	access, refresh, expiresIn, err := providers.OmniRushRefresh(ctx, p.Client, p.RefreshToken)
	if err != nil {
		return nil, err
	}
	if expiresIn <= 0 {
		// The device access token has no documented lifetime; an hour is the
		// conservative reading and keeps the refresh well before any expiry.
		expiresIn = 3600
	}
	return &TokenResult{AccessToken: access, RefreshToken: refresh, ExpiresIn: expiresIn}, nil
}
