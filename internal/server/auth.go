package server

import (
	"crypto/subtle"
	"net/http"
	"strings"
)

const bearerPrefix = "Bearer "

func AuthMiddleware(token string) func(http.Handler) http.Handler {
	token = strings.TrimSpace(token)
	return func(next http.Handler) http.Handler {
		if token == "" {
			return next
		}
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if isAuthorizedRequest(r, token) {
				next.ServeHTTP(w, r)
				return
			}
			w.Header().Set("WWW-Authenticate", `Bearer realm="sql-mcp"`)
			http.Error(w, http.StatusText(http.StatusUnauthorized), http.StatusUnauthorized)
		})
	}
}

func isAuthorizedRequest(r *http.Request, token string) bool {
	auth := strings.TrimSpace(r.Header.Get("Authorization"))
	if len(auth) > len(bearerPrefix) && strings.EqualFold(auth[:len(bearerPrefix)], bearerPrefix) &&
		constantTimeEqual(strings.TrimSpace(auth[len(bearerPrefix):]), token) {
		return true
	}
	return constantTimeEqual(r.URL.Query().Get("token"), token)
}

func constantTimeEqual(got, want string) bool {
	if got == "" || want == "" || len(got) != len(want) {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(got), []byte(want)) == 1
}
