package executor

import (
	"fmt"
	"net/http"

	"9router/proxy/internal/providers"
)

// ForwardAdCode forwards to ADCode's included-AI gateway. The connection holds
// the account's Firebase refresh token; the gateway wants a one-hour ID token in
// the Authorization header, so every request resolves one first (cached inside
// providers.AdCodeIDToken until shortly before expiry).
//
// The upstream is OpenAI-compatible, so the forwarding itself is ForwardOpenAI
// with the minted token as the API key; the UA and the x-adcode-* headers come
// from the registry entry's StaticHeaders.
func ForwardAdCode(w http.ResponseWriter, req *Request) error {
	if req.Config == nil {
		return fmt.Errorf("adcode: no provider config")
	}
	token, err := providers.AdCodeIDToken(req.Ctx, req.Client, req.APIKey)
	if err != nil {
		return err
	}
	inner := *req
	inner.APIKey = token
	return ForwardOpenAI(w, &inner)
}
