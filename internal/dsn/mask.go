package dsn

import (
	"net/url"
	"sort"
	"strings"
)

// minSecretLength is the shortest value RedactError will treat as a secret.
// Shorter values would match ordinary words and mangle unrelated messages.
const minSecretLength = 3

// sensitiveQueryParams are query parameters whose values are credentials. The
// comparison is case-insensitive; keys are matched after percent-decoding but
// kept exactly as written when the DSN is rebuilt.
var sensitiveQueryParams = map[string]bool{
	"password":      true,
	"passwd":        true,
	"pass":          true,
	"pwd":           true,
	"sslpassword":   true,
	"secret":        true,
	"client_secret": true,
	"token":         true,
	"access_token":  true,
	"auth_token":    true,
	"api_key":       true,
	"apikey":        true,
}

// Mask returns a copy of a DSN safe to log. It masks the password in the
// userinfo section and the values of credential-bearing query parameters while
// leaving everything else untouched. SQLite paths carry no credentials and are
// returned as-is. Mask never fails and never returns a partially-masked result:
// if the DSN cannot be parsed with net/url, it is masked by hand instead.
func Mask(raw string) string {
	if raw == "" || strings.HasPrefix(raw, "sqlite://") {
		return raw
	}

	scheme, rest, hasScheme := strings.Cut(raw, "://")
	if !hasScheme {
		return maskPlainDSN(raw)
	}

	base, query, hasQuery := strings.Cut(rest, "?")
	base = maskUserinfo(base)
	result := scheme + "://" + base
	if hasQuery {
		result += "?" + maskQuery(query)
	}
	return result
}

// RedactError removes the raw DSN, its password, and credential query values
// from an error's message. It preserves Unwrap, so errors.Is and errors.As keep
// working, and returns nil for a nil error.
func RedactError(err error, raw string) error {
	if err == nil {
		return nil
	}

	message := err.Error()
	for _, secret := range secretsFor(raw) {
		message = strings.ReplaceAll(message, secret, "***")
	}
	if message == err.Error() {
		return err
	}
	return &redactedError{message: message, cause: err}
}

type redactedError struct {
	message string
	cause   error
}

func (e *redactedError) Error() string { return e.message }

func (e *redactedError) Unwrap() error { return e.cause }

// maskUserinfo masks the password portion of scheme://user:pass@host, keeping
// the username readable. The userinfo ends at the last "@" before the query
// string, which matches how the driver parsers read a password containing "@".
// Everything after the first ":" in the userinfo is treated as the password.
func maskUserinfo(base string) string {
	at := strings.LastIndex(base, "@")
	if at < 0 {
		return base
	}
	userinfo, host := base[:at], base[at+1:]
	colon := strings.Index(userinfo, ":")
	if colon < 0 {
		return base
	}
	return userinfo[:colon+1] + "***@" + host
}

// maskPlainDSN handles values without a :// scheme, such as the
// "user:pass@tcp(host:3306)/db" form passed to the MySQL driver. Only a
// password-looking segment before "@" is replaced.
func maskPlainDSN(raw string) string {
	at := strings.LastIndex(raw, "@")
	if at < 0 {
		return raw
	}
	userinfo := raw[:at]
	colon := strings.Index(userinfo, ":")
	if colon < 0 {
		return raw
	}
	return userinfo[:colon+1] + "***@" + raw[at+1:]
}

func maskQuery(query string) string {
	parts := strings.Split(query, "&")
	for i, part := range parts {
		key, _, hasValue := strings.Cut(part, "=")
		if !hasValue || !isSensitiveKey(key) {
			continue
		}
		parts[i] = key + "=***"
	}
	return strings.Join(parts, "&")
}

func isSensitiveKey(rawKey string) bool {
	key, err := url.QueryUnescape(rawKey)
	if err != nil {
		key = rawKey
	}
	return sensitiveQueryParams[strings.ToLower(key)]
}

// secretsFor lists the exact strings that must not appear in an error: the raw
// DSN, its password in both decoded and percent-encoded form, and the values of
// credential query parameters. Longest first, so the full DSN is replaced
// before its fragments.
func secretsFor(raw string) []string {
	if raw == "" {
		return nil
	}

	secrets := []string{raw}
	if password := userinfoPassword(raw); password != "" {
		secrets = append(secrets, password, url.QueryEscape(password), url.PathEscape(password))
	}
	if _, rest, hasScheme := strings.Cut(raw, "://"); hasScheme {
		if _, query, hasQuery := strings.Cut(rest, "?"); hasQuery {
			for _, part := range strings.Split(query, "&") {
				key, value, hasValue := strings.Cut(part, "=")
				if !hasValue || value == "" || !isSensitiveKey(key) {
					continue
				}
				decoded, err := url.QueryUnescape(value)
				if err != nil {
					decoded = value
				}
				secrets = append(secrets, decoded)
			}
		}
	}

	unique := map[string]bool{}
	filtered := make([]string, 0, len(secrets))
	for _, secret := range secrets {
		if len(secret) < minSecretLength || unique[secret] {
			continue
		}
		unique[secret] = true
		filtered = append(filtered, secret)
	}
	sort.Slice(filtered, func(i, j int) bool {
		if len(filtered[i]) != len(filtered[j]) {
			return len(filtered[i]) > len(filtered[j])
		}
		return filtered[i] < filtered[j]
	})
	return filtered
}

// userinfoPassword extracts the decoded password from a DSN, or "" when it has
// none. It mirrors the split used by Mask so both agree on the userinfo range.
func userinfoPassword(raw string) string {
	_, rest, hasScheme := strings.Cut(raw, "://")
	if !hasScheme {
		rest = raw
	}
	base, _, _ := strings.Cut(rest, "?")
	at := strings.LastIndex(base, "@")
	if at < 0 {
		return ""
	}
	userinfo := base[:at]
	colon := strings.Index(userinfo, ":")
	if colon < 0 {
		return ""
	}
	password := userinfo[colon+1:]
	if decoded, err := url.QueryUnescape(password); err == nil {
		return decoded
	}
	return password
}
