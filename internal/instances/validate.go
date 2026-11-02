package instances

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/longkeyy/sqlx-mcp-server/internal/dsn"
)

const (
	maxIDLength   = 64
	maxNameLength = 256
	maxMemoLength = 4096
	maxDSNLength  = 8192
)

// supportedSchemes is repeated in errors instead of echoing a rejected DSN.
const supportedSchemes = "mysql://, mariadb://, percona://, tidb://, doris://, oceanbase://, starrocks://, postgres://, postgresql://, cockroach://, greenplum://, opengauss://, mogdb://, sqlite://, sqlserver://, clickhouse://"

var idPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]*$`)

// invalidError marks a client-supplied value that cannot be accepted. It maps
// to HTTP 400 and its message is safe to return (it never contains a DSN).
type invalidError struct{ message string }

func (e invalidError) Error() string { return e.message }

func invalidf(format string, args ...any) error {
	return invalidError{message: fmt.Sprintf(format, args...)}
}

// instanceInput is a validated create request.
type instanceInput struct {
	ID   string
	Name string
	Memo string
	DSN  string
}

// normalizeID canonicalises an ID: surrounding space is removed and the result
// is lowercased, because IDs are matched exactly and must be stable.
func normalizeID(raw string) (string, error) {
	id := strings.ToLower(strings.TrimSpace(raw))
	if id == "" {
		return "", invalidf("instance id is required")
	}
	if len(id) > maxIDLength {
		return "", invalidf("instance id must be at most %d characters", maxIDLength)
	}
	if !idPattern.MatchString(id) {
		return "", invalidf("instance id must contain only lowercase letters, numbers, underscores, or hyphens and must start with a letter or number")
	}
	return id, nil
}

// lookupID canonicalises an ID taken from a URL path. A value that cannot be
// a stored ID is reported as not found rather than as a validation failure.
func lookupID(raw string) (string, bool) {
	id := strings.ToLower(strings.TrimSpace(raw))
	if id == "" || len(id) > maxIDLength || !idPattern.MatchString(id) {
		return "", false
	}
	return id, true
}

func normalizeName(raw, id string) (string, error) {
	name := strings.TrimSpace(raw)
	if name == "" {
		name = id
	}
	if len(name) > maxNameLength {
		return "", invalidf("instance name must be at most %d characters", maxNameLength)
	}
	return name, nil
}

func normalizeMemo(raw string) (string, error) {
	memo := strings.TrimSpace(raw)
	if len(memo) > maxMemoLength {
		return "", invalidf("instance memo must be at most %d characters", maxMemoLength)
	}
	return memo, nil
}

// validateDSN checks that raw is a syntactically usable, supported DSN and
// returns it unchanged. Reachability is deliberately not checked: an
// unreachable instance must still be storable so it can be corrected later.
// Error messages never echo the DSN, which may contain credentials.
func validateDSN(raw string) (string, error) {
	value := strings.TrimSpace(raw)
	if value == "" {
		return "", invalidf("dsn is required")
	}
	if len(value) > maxDSNLength {
		return "", invalidf("dsn must be at most %d characters", maxDSNLength)
	}
	cfg, err := dsn.Parse(value)
	if err != nil {
		if strings.Contains(err.Error(), "unsupported DSN scheme") {
			return "", invalidf("unsupported DSN scheme; supported: %s", supportedSchemes)
		}
		return "", invalidf("invalid DSN: malformed connection URL")
	}
	switch cfg.Driver {
	case dsn.DriverSQLite:
		path := cfg.ConnStr
		if idx := strings.IndexByte(path, '?'); idx >= 0 {
			path = path[:idx]
		}
		if strings.TrimSpace(path) == "" {
			return "", invalidf("sqlite DSN must include a database file path")
		}
	default:
		if strings.TrimSpace(cfg.Host) == "" {
			return "", invalidf("dsn must include a host")
		}
	}
	return value, nil
}

// parseCreate validates a create request body.
func parseCreate(req createRequest) (instanceInput, error) {
	id, err := normalizeID(req.ID)
	if err != nil {
		return instanceInput{}, err
	}
	value, err := validateDSN(req.DSN)
	if err != nil {
		return instanceInput{}, err
	}
	name, err := normalizeName(req.Name, id)
	if err != nil {
		return instanceInput{}, err
	}
	memo, err := normalizeMemo(req.Memo)
	if err != nil {
		return instanceInput{}, err
	}
	return instanceInput{ID: id, Name: name, Memo: memo, DSN: value}, nil
}

// parsePatch validates a partial update for the instance at pathID. An
// omitted or empty dsn leaves the stored secret untouched.
func parsePatch(pathID string, req patchRequest) (instancePatch, error) {
	var patch instancePatch
	if req.ID != nil {
		id, err := normalizeID(*req.ID)
		if err != nil {
			return instancePatch{}, err
		}
		if id != pathID {
			return instancePatch{}, invalidf("instance id cannot be changed")
		}
	}
	if req.Name != nil {
		name := strings.TrimSpace(*req.Name)
		if name == "" {
			return instancePatch{}, invalidf("instance name must not be empty")
		}
		if len(name) > maxNameLength {
			return instancePatch{}, invalidf("instance name must be at most %d characters", maxNameLength)
		}
		patch.Name = &name
	}
	if req.Memo != nil {
		memo, err := normalizeMemo(*req.Memo)
		if err != nil {
			return instancePatch{}, err
		}
		patch.Memo = &memo
	}
	if req.DSN != nil && strings.TrimSpace(*req.DSN) != "" {
		value, err := validateDSN(*req.DSN)
		if err != nil {
			return instancePatch{}, err
		}
		patch.DSN = &value
	}
	return patch, nil
}
