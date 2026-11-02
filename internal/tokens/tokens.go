// Package tokens provides persistent API token management and per-tool
// authorization for HTTP transports.
//
// Tokens are stored as SHA-256 digests inside tokens.sqlite in the configured
// data directory; the plaintext secret is returned only once when it is
// created. The package has no dependency on the router, MCP server or instance
// registry: callers mount Middleware() around transport HTTP handlers, mount
// Handler() on the management API outside that middleware, and call
// Authorize(tokens.TokenFromContext(ctx), toolName) before every tool call.
package tokens

import (
	"context"
	"errors"
)

var (
	// ErrUnauthorized reports a missing, unknown or revoked credential.
	ErrUnauthorized = errors.New("tokens: unauthorized")
	// ErrForbidden reports an authenticated credential that may not invoke the
	// requested tool (for example, execute without allow_execute).
	ErrForbidden = errors.New("tokens: forbidden")
	// ErrClosed reports that the manager was closed and can no longer serve
	// requests. Authorization fails closed in that state.
	ErrClosed = errors.New("tokens: manager closed")
)

type tokenContextKey struct{}

// TokenFromContext returns the credential stored by Middleware. It returns an
// empty string when the context is nil, has no credential, or the credential
// was stored as empty (anonymous bootstrap access).
func TokenFromContext(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	token, _ := ctx.Value(tokenContextKey{}).(string)
	return token
}

func withToken(ctx context.Context, token string) context.Context {
	return context.WithValue(ctx, tokenContextKey{}, token)
}
