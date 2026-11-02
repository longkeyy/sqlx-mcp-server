package instances

import (
	"context"
	"encoding/json"
	"github.com/longkeyy/sqlx-mcp-server/internal/config"
	"net/http"
	"sort"
)

// SetExternal installs immutable CLI/environment configuration before serving.
// External records override colliding persisted IDs, but are never written to disk.
func (m *Manager) SetExternal(configs []config.DatabaseConfig) {
	m.external = make(map[string]config.DatabaseConfig, len(configs))
	for _, c := range configs {
		m.external[c.ID] = c
	}
}
func (m *Manager) externalView(c config.DatabaseConfig) instanceView {
	v := newInstanceView(record{ID: c.ID, Name: c.Name, Memo: c.Memo, DSN: c.DSN, Status: statusUnknown})
	v.Source = "external"
	return v
}
func (m *Manager) externalRequest(w http.ResponseWriter, r *http.Request, route, id string) bool {
	c, ok := m.external[id]
	if route == "instances" && r.Method == http.MethodPost {
		// Decode and validate creates here to reject external ID collisions.
		var body json.RawMessage
		if err := decodeBody(w, r, &body); err != nil {
			m.writeError(w, r, err)
			return true
		}
		var req createRequest
		if json.Unmarshal(body, &req) != nil {
			m.writeError(w, r, invalidf("invalid JSON body"))
			return true
		}
		key, _ := normalizeID(req.ID)
		if _, exists := m.external[key]; exists {
			m.writeError(w, r, errConflict)
			return true
		}
		input, err := parseCreate(req)
		if err != nil {
			m.writeError(w, r, err)
			return true
		}
		rec, err := m.create(r.Context(), input)
		if err != nil {
			m.writeError(w, r, err)
			return true
		}
		writeJSON(w, http.StatusCreated, newInstanceView(rec))
		return true
	}
	if !ok {
		return false
	}
	if route == "item" && r.Method == http.MethodGet {
		writeJSON(w, 200, m.externalView(c))
		return true
	}
	if route == "test" && r.Method == http.MethodPost {
		err := runProbe(r.Context(), c.DSN, defaultTestTimeout)
		v := testResponse{OK: err == nil, Instance: id}
		if err != nil {
			v.Error = "connection test failed"
		}
		writeJSON(w, 200, v)
		return true
	}
	m.writeError(w, r, httpError{status: 403, message: "externally configured instance is read-only"})
	return true
}
func (m *Manager) externalDefault(ctx context.Context, id string) error {
	release, err := m.acquireWrite()
	if err != nil {
		return err
	}
	defer release()
	_, err = m.db.ExecContext(ctx, `INSERT INTO settings(key,value) VALUES('default_instance',?) ON CONFLICT(key) DO UPDATE SET value=excluded.value`, id)
	return err
}
func sortConfigs(cs []config.DatabaseConfig) {
	sort.Slice(cs, func(i, j int) bool { return cs[i].ID < cs[j].ID })
}
