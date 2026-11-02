package tokens

import (
	"context"
	"errors"
	"net/http"
	"strings"
)

const (
	bearerScheme   = "Bearer"
	authChallenge  = `Bearer realm="sql-mcp"`
	queryTokenName = "token"
)

// Middleware authenticates MCP transport requests using an
// "Authorization: Bearer <token>" header or the legacy "?token=" query
// parameter, then stores the accepted credential in the request context for
// TokenFromContext. Missing or invalid credentials receive 401; storage
// failures or a closed manager receive 503 (fail closed).
//
// Middleware only authenticates the HTTP request. Tool permissions are not
// checked here: every tool call must still invoke
// Authorize(TokenFromContext(ctx), tool) so that revocations and the execute
// flag apply to already-established sessions. The token query parameter is
// left untouched because SSE transports propagate it to message endpoints.
func (m *Manager) Middleware(next http.Handler) http.Handler {
	if next == nil {
		panic("tokens: Middleware requires a non-nil handler")
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), queryBudget)
		token, presented, err := m.authenticateRequest(ctx, r)
		cancel()
		if err != nil {
			writeAuthFailure(w, err, presented)
			return
		}
		next.ServeHTTP(w, r.WithContext(withToken(r.Context(), token)))
	})
}

// authenticateRequest returns the first credential (Bearer, then query) that
// authenticates. During bootstrap (no authentication configured) requests are
// accepted and the first presented credential, if any, is propagated.
func (m *Manager) authenticateRequest(ctx context.Context, r *http.Request) (token string, presented bool, err error) {
	candidates := requestCredentials(r)
	if len(candidates) == 0 {
		if _, err := m.authenticate(ctx, ""); err != nil {
			return "", false, err
		}
		return "", false, nil
	}

	var firstErr error
	for _, candidate := range candidates {
		_, err := m.authenticate(ctx, candidate)
		if err == nil {
			return candidate, true, nil
		}
		if !errors.Is(err, ErrUnauthorized) {
			return "", true, err // Storage failure or closed manager: fail closed.
		}
		if firstErr == nil {
			firstErr = err
		}
	}
	return "", true, firstErr
}

// requestCredentials extracts candidate credentials in priority order.
func requestCredentials(r *http.Request) []string {
	var candidates []string
	if token, ok := bearerToken(r.Header.Get("Authorization")); ok {
		candidates = append(candidates, token)
	}
	if token := strings.TrimSpace(r.URL.Query().Get(queryTokenName)); token != "" {
		if len(candidates) == 0 || candidates[0] != token {
			candidates = append(candidates, token)
		}
	}
	return candidates
}

// bearerToken parses an RFC 6750 Authorization header; the scheme is
// case-insensitive.
func bearerToken(header string) (string, bool) {
	scheme, value, ok := strings.Cut(strings.TrimSpace(header), " ")
	if !ok || !strings.EqualFold(scheme, bearerScheme) {
		return "", false
	}
	value = strings.TrimSpace(value)
	return value, value != ""
}

func writeAuthFailure(w http.ResponseWriter, err error, presented bool) {
	if errors.Is(err, ErrUnauthorized) {
		challenge := authChallenge
		if presented {
			challenge += `, error="invalid_token"`
		}
		w.Header().Set("WWW-Authenticate", challenge)
		writeJSONError(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	writeJSONError(w, http.StatusServiceUnavailable, "authorization unavailable")
}
