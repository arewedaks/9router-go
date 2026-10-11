package middleware

import "net/http"

// MaxBodySize limits the maximum size of request bodies to prevent OOM attacks.
//
// 10MB was too small for the requests this proxy actually serves: a vision or
// agent harness sends screenshots inline as base64, and a single PNG can be
// several megabytes, so a multi-image turn blew the cap and surfaced as a bare
// 400. 64MB holds a realistic multi-image turn with headroom while still
// bounding one connection's memory.
const DefaultMaxBodySize = 64 * 1024 * 1024 // 64MB

// MaxBody returns a middleware that limits request body size.
func MaxBody(maxBytes int64) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			r.Body = http.MaxBytesReader(w, r.Body, maxBytes)
			next.ServeHTTP(w, r)
		})
	}
}
