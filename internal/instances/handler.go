package instances

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log"
	"mime"
	"net/http"
	"net/url"
	"sort"
	"strings"

	"github.com/longkeyy/sqlx-mcp-server/internal/dsn"
)

// maxBodyBytes bounds every request body accepted by the handler.
const maxBodyBytes = 1 << 20

// createRequest is the JSON body of POST /api/instances.
type createRequest struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Memo string `json:"memo"`
	DSN  string `json:"dsn"`
}

// patchRequest is the JSON body of PATCH /api/instances/{id}. Absent fields
// are left untouched; an empty dsn also keeps the stored credential.
type patchRequest struct {
	ID   *string `json:"id"`
	Name *string `json:"name"`
	Memo *string `json:"memo"`
	DSN  *string `json:"dsn"`
}

// defaultRequest is the JSON body of PUT /api/settings/default.
type defaultRequest struct {
	Instance string `json:"instance"`
}

// instanceView is the HTTP representation of an instance. It deliberately has
// no DSN, host, user or password field, and its shape is also used for the
// create and patch responses.
type instanceView struct {
	ID           string `json:"id"`
	Name         string `json:"name"`
	Memo         string `json:"memo"`
	Driver       string `json:"driver"`
	Dialect      string `json:"dialect"`
	Source       string `json:"source"`
	Status       string `json:"status"`
	LastError    string `json:"last_error,omitempty"`
	TestedAt     string `json:"tested_at,omitempty"`
	IsDefault    bool   `json:"is_default"`
	MaxOpenConns int    `json:"max_open_conns"`
	MaxIdleConns int    `json:"max_idle_conns"`
	CreatedAt    string `json:"created_at,omitempty"`
	UpdatedAt    string `json:"updated_at,omitempty"`
}

type listResponse struct {
	Instances []instanceView `json:"instances"`
}

type defaultResponse struct {
	DefaultInstance string `json:"default_instance"`
}

type errorResponse struct {
	Error string `json:"error"`
}

// httpError is a handler-level failure with a safe message and status.
type httpError struct {
	status  int
	message string
}

func (e httpError) Error() string { return e.message }

// Handler returns the administration API for this manager. It can be mounted
// directly (http.Handle("/api/instances", m.Handler())) or nested under a
// larger mux; all routes are absolute and start with /api.
func (m *Manager) Handler() http.Handler {
	return http.HandlerFunc(m.serveHTTP)
}

func (m *Manager) serveHTTP(w http.ResponseWriter, r *http.Request) {
	// Administration responses contain metadata about configured databases;
	// keep them out of caches and stop content sniffing.
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")

	// Browsers send Origin on cross-site writes. Only same-origin writes are
	// honoured; requests without an Origin (CLI, curl) are allowed because the
	// admin listener is expected to be on a trusted network.
	if isWriteMethod(r.Method) && !originAllowed(r) {
		m.writeError(w, r, httpError{status: http.StatusForbidden, message: "cross-origin request denied"})
		return
	}

	route, id, found := parseRoute(r.URL.Path)
	if !found {
		m.writeError(w, r, httpError{status: http.StatusNotFound, message: "not found"})
		return
	}

	if m.externalRequest(w, r, route, id) {
		return
	}
	switch route {
	case "instances":
		switch r.Method {
		case http.MethodGet:
			m.serveList(w, r)
		case http.MethodPost:
			m.serveCreate(w, r)
		default:
			methodNotAllowed(w, http.MethodGet+", "+http.MethodPost)
		}
	case "item":
		id, valid := lookupID(id)
		if !valid {
			m.writeError(w, r, notFound(id))
			return
		}
		switch r.Method {
		case http.MethodGet:
			m.serveGet(w, r, id)
		case http.MethodPatch:
			m.servePatch(w, r, id)
		case http.MethodDelete:
			m.serveDelete(w, r, id)
		default:
			methodNotAllowed(w, http.MethodGet+", "+http.MethodPatch+", "+http.MethodDelete)
		}
	case "test":
		id, valid := lookupID(id)
		if !valid {
			m.writeError(w, r, notFound(id))
			return
		}
		if r.Method != http.MethodPost {
			methodNotAllowed(w, http.MethodPost)
			return
		}
		m.serveTest(w, r, id)
	case "settings":
		if r.Method != http.MethodGet {
			methodNotAllowed(w, http.MethodGet)
			return
		}
		m.serveSettings(w, r)
	case "default":
		if r.Method != http.MethodPut {
			methodNotAllowed(w, http.MethodPut)
			return
		}
		m.serveSetDefault(w, r)
	default:
		m.writeError(w, r, httpError{status: http.StatusNotFound, message: "not found"})
	}
}

// parseRoute matches the fixed administration routes. The returned id is not
// yet canonicalised; callers use lookupID for that.
func parseRoute(path string) (route, id string, ok bool) {
	if path != "/" {
		path = strings.TrimSuffix(path, "/")
	}
	switch path {
	case "/api/instances":
		return "instances", "", true
	case "/api/settings":
		return "settings", "", true
	case "/api/settings/default":
		return "default", "", true
	}
	const prefix = "/api/instances/"
	if !strings.HasPrefix(path, prefix) {
		return "", "", false
	}
	rest := strings.TrimPrefix(path, prefix)
	if rest == "" {
		return "", "", false
	}
	if tail, isTest := strings.CutSuffix(rest, "/test"); isTest {
		if tail == "" || strings.Contains(tail, "/") {
			return "", "", false
		}
		return "test", tail, true
	}
	if strings.Contains(rest, "/") {
		return "", "", false
	}
	return "item", rest, true
}

func isWriteMethod(method string) bool {
	switch method {
	case http.MethodPost, http.MethodPatch, http.MethodPut, http.MethodDelete:
		return true
	default:
		return false
	}
}

// originAllowed reports whether a write request comes from the admin origin.
func originAllowed(r *http.Request) bool {
	origin := strings.TrimSpace(r.Header.Get("Origin"))
	if origin == "" {
		return true
	}
	if strings.EqualFold(origin, "null") {
		return false
	}
	u, err := url.Parse(origin)
	if err != nil || u.Host == "" || u.User != nil || u.Path != "" || u.RawQuery != "" || u.Fragment != "" {
		return false
	}
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	return strings.EqualFold(u.Scheme, scheme) && strings.EqualFold(u.Host, r.Host)
}

func (m *Manager) serveList(w http.ResponseWriter, r *http.Request) {
	records, err := m.records(r.Context())
	if err != nil {
		m.writeError(w, r, err)
		return
	}
	views := make([]instanceView, 0, len(records))
	for _, rec := range records {
		if _, exists := m.external[rec.ID]; !exists {
			views = append(views, newInstanceView(rec))
		}
	}
	for _, c := range m.external {
		views = append(views, m.externalView(c))
	}
	sort.Slice(views, func(i, j int) bool { return views[i].ID < views[j].ID })
	writeJSON(w, http.StatusOK, listResponse{Instances: views})
}

func (m *Manager) serveGet(w http.ResponseWriter, r *http.Request, id string) {
	rec, err := m.get(r.Context(), id)
	if err != nil {
		m.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, newInstanceView(rec))
}

func (m *Manager) serveCreate(w http.ResponseWriter, r *http.Request) {
	var req createRequest
	if err := decodeBody(w, r, &req); err != nil {
		m.writeError(w, r, err)
		return
	}
	input, err := parseCreate(req)
	if err != nil {
		m.writeError(w, r, err)
		return
	}
	rec, err := m.create(r.Context(), input)
	if err != nil {
		m.writeError(w, r, err)
		return
	}
	w.Header().Set("Location", "/api/instances/"+url.PathEscape(rec.ID))
	writeJSON(w, http.StatusCreated, newInstanceView(rec))
}

func (m *Manager) servePatch(w http.ResponseWriter, r *http.Request, id string) {
	var req patchRequest
	if err := decodeBody(w, r, &req); err != nil {
		m.writeError(w, r, err)
		return
	}
	patch, err := parsePatch(id, req)
	if err != nil {
		m.writeError(w, r, err)
		return
	}
	rec, err := m.update(r.Context(), id, patch)
	if err != nil {
		m.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, newInstanceView(rec))
}

func (m *Manager) serveDelete(w http.ResponseWriter, r *http.Request, id string) {
	if err := m.remove(r.Context(), id); err != nil {
		m.writeError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (m *Manager) serveSettings(w http.ResponseWriter, r *http.Request) {
	id, err := m.Default(r.Context())
	if err != nil {
		m.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, defaultResponse{DefaultInstance: id})
}

func (m *Manager) serveSetDefault(w http.ResponseWriter, r *http.Request) {
	var req defaultRequest
	if err := decodeBody(w, r, &req); err != nil {
		m.writeError(w, r, err)
		return
	}
	id, err := normalizeID(req.Instance)
	if err != nil {
		m.writeError(w, r, err)
		return
	}
	setDefault := m.setDefault
	if _, ok := m.external[id]; ok {
		setDefault = m.externalDefault
	}
	if err := setDefault(r.Context(), id); err != nil {
		m.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, defaultResponse{DefaultInstance: id})
}

// newInstanceView converts a stored record to its secret-free HTTP form.
func newInstanceView(rec record) instanceView {
	driver, dialect := "unknown", "SQL"
	if cfg, err := dsn.Parse(rec.DSN); err == nil {
		driver, dialect = string(cfg.Driver), dialectName(cfg.Driver)
	}
	view := instanceView{
		ID:           rec.ID,
		Name:         rec.Name,
		Memo:         rec.Memo,
		Driver:       driver,
		Dialect:      dialect,
		Source:       sourceManaged,
		Status:       rec.Status,
		LastError:    sanitizeMessage(rec.StatusError, ""),
		IsDefault:    rec.IsDefault,
		MaxOpenConns: rec.MaxOpenConns,
		MaxIdleConns: rec.MaxIdleConns,
		CreatedAt:    formatUnix(rec.CreatedAt),
		UpdatedAt:    formatUnix(rec.UpdatedAt),
	}
	if rec.Status == statusUnknown && rec.StatusError == "" {
		view.LastError = ""
	}
	if rec.TestedAt > 0 {
		view.TestedAt = formatUnix(rec.TestedAt)
	}
	return view
}

// dialectName mirrors the product's display name for each supported driver.
func dialectName(d dsn.Driver) string {
	switch d {
	case dsn.DriverMySQL:
		return "MySQL"
	case dsn.DriverMariaDB:
		return "MariaDB"
	case dsn.DriverPercona:
		return "Percona Server for MySQL"
	case dsn.DriverTiDB:
		return "TiDB"
	case dsn.DriverDoris:
		return "Apache Doris"
	case dsn.DriverOceanBase:
		return "OceanBase"
	case dsn.DriverStarRocks:
		return "StarRocks"
	case dsn.DriverPostgres:
		return "PostgreSQL"
	case dsn.DriverCockroach:
		return "CockroachDB"
	case dsn.DriverGreenplum:
		return "Greenplum"
	case dsn.DriverOpenGauss:
		return "openGauss"
	case dsn.DriverMogDB:
		return "MogDB"
	case dsn.DriverSQLite:
		return "SQLite"
	case dsn.DriverMSSQL:
		return "SQL Server"
	case dsn.DriverClickHouse:
		return "ClickHouse"
	default:
		return "SQL"
	}
}

// decodeBody enforces a JSON content type, caps the body size and decodes one
// JSON value, rejecting trailing data.
func decodeBody(w http.ResponseWriter, r *http.Request, dst any) error {
	if !hasJSONContentType(r) {
		return httpError{status: http.StatusUnsupportedMediaType, message: "content type must be application/json"}
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxBodyBytes)
	dec := json.NewDecoder(r.Body)
	if err := dec.Decode(dst); err != nil {
		var maxErr *http.MaxBytesError
		if errors.As(err, &maxErr) {
			return maxErr
		}
		return invalidf("invalid JSON body")
	}
	var trailing any
	if err := dec.Decode(&trailing); err != io.EOF {
		return invalidf("invalid JSON body")
	}
	return nil
}

func hasJSONContentType(r *http.Request) bool {
	raw := strings.TrimSpace(r.Header.Get("Content-Type"))
	if raw == "" {
		return false
	}
	mediaType, _, err := mime.ParseMediaType(raw)
	if err != nil {
		return false
	}
	return mediaType == "application/json" || strings.HasSuffix(mediaType, "+json")
}

func methodNotAllowed(w http.ResponseWriter, allow string) {
	w.Header().Set("Allow", allow)
	writeJSON(w, http.StatusMethodNotAllowed, errorResponse{Error: "method not allowed"})
}

func writeJSON(w http.ResponseWriter, status int, payload any) {
	body, err := json.Marshal(payload)
	if err != nil {
		// A marshalling failure is a programming error; never echo details.
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"error":"internal error"}`))
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_, _ = w.Write(body)
}

// writeError maps store and request failures onto safe HTTP responses.
func (m *Manager) writeError(w http.ResponseWriter, r *http.Request, err error) {
	status, message := http.StatusInternalServerError, "internal error"
	switch {
	case errors.As(err, new(invalidError)):
		status, message = http.StatusBadRequest, err.Error()
	case errors.Is(err, errNotFound):
		status, message = http.StatusNotFound, "instance not found"
	case errors.Is(err, errConflict):
		status, message = http.StatusConflict, "instance already exists"
	case errors.Is(err, errClosed):
		status, message = http.StatusServiceUnavailable, "instance store is closed"
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		status, message = http.StatusGatewayTimeout, "request timed out"
	default:
		var maxErr *http.MaxBytesError
		var httpErr httpError
		switch {
		case errors.As(err, &maxErr):
			status, message = http.StatusRequestEntityTooLarge, "request body too large"
		case errors.As(err, &httpErr):
			status, message = httpErr.status, httpErr.message
		default:
			// Never echo internal errors: they can embed paths or DSNs.
			log.Printf("instances: request failed: %s %s: %v", r.Method, r.URL.Path, err)
		}
	}
	writeJSON(w, status, errorResponse{Error: message})
}
