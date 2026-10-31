package executor

import (
	"fmt"
	"net/http"
	"strings"
)

// ForwardZed forwards to Zed's cloud completions endpoint. Two things differ
// from a normal OpenAI provider:
//
//   - the credential is `<user_id> <access_token>`, not a bearer token (crates/
//     client/src/client.rs builds exactly that header), so the registry entry is
//     declared "raw" and the two halves are joined here;
//   - the sign-in stores the user id next to the token, because the numeric id is
//     half of the credential and cannot be recovered from the token.
func ForwardZed(w http.ResponseWriter, req *Request) error {
	if req.Config == nil {
		return fmt.Errorf("zed: no provider config")
	}
	userID := strings.TrimSpace(req.ZedUserID)
	token := strings.TrimSpace(req.APIKey)
	if userID == "" || token == "" {
		return fmt.Errorf("zed: connection is missing the user id or access token — sign in again from the dashboard")
	}
	inner := *req
	inner.APIKey = userID + " " + token
	return ForwardOpenAI(w, &inner)
}
