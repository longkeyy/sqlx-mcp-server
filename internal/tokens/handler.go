package tokens

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
)

const (
	tokensPath      = "/api/tokens"
	maxBodyBytes    = 64 << 10
	maxIDLength     = 128
	jsonContentType = "application/json; charset=utf-8"
)

// Handler returns the token administration API. It owns /api/tokens only and
// must be mounted outside Middleware, without http.StripPrefix:
//
//	GET    /api/tokens      -> 200 {"tokens":[{id,name,allow_execute,created_at}]}
//	POST   /api/tokens      -> 201 {id,name,allow_execute,created_at,token}
//	DELETE /api/tokens/{id} -> 204
//
// The plaintext token appears only in the POST response. Errors are JSON
// objects {"error":"..."}. Unsafe requests from cross-origin browsers are
// rejected with 403; direct clients without Origin/Sec-Fetch-Site are allowed.
// The API has no login of its own, so it must only be exposed on a trusted
// (by default loopback) admin listener.
func (m *Manager) Handler() http.Handler {
	return m.handler
}

func (m *Manager) newHandler() http.Handler {
	protection := http.NewCrossOriginProtection()
	protection.SetDenyHandler(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		writeJSONError(w, http.StatusForbidden, "cross-origin request denied")
	}))
	return protection.Handler(http.HandlerFunc(m.serveAPI))
}

func (m *Manager) serveAPI(w http.ResponseWriter, r *http.Request) {
	setNoStore(w.Header())

	id, ok := matchTokensPath(r.URL.EscapedPath())
	if !ok {
		writeJSONError(w, http.StatusNotFound, "not found")
		return
	}
	if id == "" {
		switch r.Method {
		case http.MethodGet, http.MethodHead:
			m.handleList(w, r)
		case http.MethodPost:
			m.handleCreate(w, r)
		default:
			w.Header().Set("Allow", "GET, HEAD, POST")
			writeJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
		}
		return
	}
	if r.Method != http.MethodDelete {
		w.Header().Set("Allow", "DELETE")
		writeJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	m.handleDelete(w, r, id)
}

// matchTokensPath reports whether the escaped path addresses the collection
// (id == "") or a single token, rejecting everything outside /api/tokens.
func matchTokensPath(escapedPath string) (id string, ok bool) {
	rest, found := strings.CutPrefix(escapedPath, tokensPath)
	if !found {
		return "", false
	}
	if rest == "" || rest == "/" {
		return "", true
	}
	rawID, found := strings.CutPrefix(rest, "/")
	if !found || strings.Contains(rawID, "/") {
		return "", false
	}
	id, err := url.PathUnescape(rawID)
	if err != nil || id == "" || len(id) > maxIDLength || strings.TrimSpace(id) != id {
		return "", false
	}
	return id, true
}

func (m *Manager) handleList(w http.ResponseWriter, r *http.Request) {
	records, err := m.list(r.Context())
	if err != nil {
		writeStoreError(w, err)
		return
	}
	body := struct {
		Tokens []tokenJSON `json:"tokens"`
	}{Tokens: make([]tokenJSON, 0, len(records))}
	for _, rec := range records {
		body.Tokens = append(body.Tokens, newTokenJSON(rec))
	}
	writeJSON(w, http.StatusOK, body)
}

func (m *Manager) handleCreate(w http.ResponseWriter, r *http.Request) {
	req, status, err := decodeCreateRequest(w, r)
	if err != nil {
		writeJSONError(w, status, err.Error())
		return
	}
	rec, secret, err := m.createToken(r.Context(), req.Name, req.AllowExecute)
	if err != nil {
		if isValidationError(err) {
			writeJSONError(w, http.StatusBadRequest, err.Error())
			return
		}
		writeStoreError(w, err)
		return
	}
	body := struct {
		tokenJSON
		Token string `json:"token"`
	}{tokenJSON: newTokenJSON(rec), Token: secret}
	writeJSON(w, http.StatusCreated, body)
}

func (m *Manager) handleDelete(w http.ResponseWriter, r *http.Request, id string) {
	removed, err := m.revokeToken(r.Context(), id)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	if !removed {
		writeJSONError(w, http.StatusNotFound, "token not found")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// tokenJSON is the public, non-secret representation of a token.
type tokenJSON struct {
	ID           string `json:"id"`
	Name         string `json:"name"`
	AllowExecute bool   `json:"allow_execute"`
	CreatedAt    string `json:"created_at"`
}

func newTokenJSON(rec record) tokenJSON {
	return tokenJSON{
		ID:           rec.ID,
		Name:         rec.Name,
		AllowExecute: rec.AllowExecute,
		CreatedAt:    rec.CreatedAt,
	}
}

type createRequest struct {
	Name         string `json:"name"`
	AllowExecute bool   `json:"allow_execute"`
}

// decodeCreateRequest parses a single bounded JSON object. A missing
// Content-Type is accepted for CLI convenience; any other type is rejected.
func decodeCreateRequest(w http.ResponseWriter, r *http.Request) (createRequest, int, error) {
	if mt := mediaType(r.Header.Get("Content-Type")); mt != "" && mt != "application/json" {
		return createRequest{}, http.StatusUnsupportedMediaType, errors.New("Content-Type must be application/json")
	}
	var req createRequest
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBodyBytes))
	if err := decoder.Decode(&req); err != nil {
		return createRequest{}, decodeErrorStatus(err), decodeError(err)
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		if err == nil {
			err = errors.New("unexpected trailing data")
		}
		return createRequest{}, decodeErrorStatus(err), decodeError(err)
	}
	return req, 0, nil
}

func decodeErrorStatus(err error) int {
	var maxErr *http.MaxBytesError
	if errors.As(err, &maxErr) {
		return http.StatusRequestEntityTooLarge
	}
	return http.StatusBadRequest
}

func decodeError(err error) error {
	var maxErr *http.MaxBytesError
	var typeErr *json.UnmarshalTypeError
	switch {
	case errors.Is(err, io.EOF):
		return errors.New("request body must be a JSON object")
	case errors.As(err, &maxErr):
		return fmt.Errorf("request body exceeds %d bytes", maxErr.Limit)
	case errors.As(err, &typeErr) && typeErr.Field != "":
		return fmt.Errorf("field %q has an invalid type", typeErr.Field)
	case errors.As(err, &typeErr):
		return errors.New("request body must be a JSON object")
	default:
		return errors.New("request body must be a single valid JSON object")
	}
}

// mediaType extracts the lower-case media type without parameters.
func mediaType(header string) string {
	value, _, _ := strings.Cut(header, ";")
	return strings.ToLower(strings.TrimSpace(value))
}

func writeStoreError(w http.ResponseWriter, err error) {
	if errors.Is(err, ErrClosed) {
		writeJSONError(w, http.StatusServiceUnavailable, "token store unavailable")
		return
	}
	// Storage details are deliberately not echoed to clients.
	writeJSONError(w, http.StatusInternalServerError, "token store failure")
}

func setNoStore(h http.Header) {
	h.Set("Cache-Control", "no-store")
	h.Set("X-Content-Type-Options", "nosniff")
}

func writeJSONError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, map[string]string{"error": message})
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	data, err := json.Marshal(body)
	if err != nil {
		status = http.StatusInternalServerError
		data = []byte(`{"error":"internal error"}`)
	}
	h := w.Header()
	setNoStore(h)
	h.Set("Content-Type", jsonContentType)
	w.WriteHeader(status)
	_, _ = w.Write(append(data, '\n'))
}
