package api

import (
	"crypto/subtle"
	"net/http"
)

// APIKeyAuth returns middleware that validates the X-API-Key header.
// If key is empty, the middleware is a no-op (all requests pass through).
func APIKeyAuth(key string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		if key == "" {
			return next
		}
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			provided := r.Header.Get("X-API-Key")
			if subtle.ConstantTimeCompare([]byte(provided), []byte(key)) != 1 {
				writeError(w, http.StatusUnauthorized, "invalid or missing API key")
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}
