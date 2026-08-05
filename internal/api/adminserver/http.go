package adminserver

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/aupv9/unigate/internal/config"
	"github.com/aupv9/unigate/internal/ruleengine"
)

// HTTPServer is a plain JSON REST view over the same Registry the gRPC
// AdminService uses, operating on config.RuleConfig directly so it can
// round-trip every field (including key_parts) without the proto
// mapping in convert.go.
type HTTPServer struct {
	registry *ruleengine.Registry
	mux      *http.ServeMux
}

func NewHTTPServer(registry *ruleengine.Registry) *HTTPServer {
	s := &HTTPServer{registry: registry, mux: http.NewServeMux()}
	s.mux.HandleFunc("GET /v1/admin/rules", s.list)
	s.mux.HandleFunc("POST /v1/admin/rules", s.create)
	s.mux.HandleFunc("GET /v1/admin/rules/{id}", s.get)
	s.mux.HandleFunc("PUT /v1/admin/rules/{id}", s.update)
	s.mux.HandleFunc("DELETE /v1/admin/rules/{id}", s.delete)
	return s
}

func (s *HTTPServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.mux.ServeHTTP(w, r)
}

func (s *HTTPServer) list(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.registry.List(r.URL.Query().Get("namespace")))
}

func (s *HTTPServer) get(w http.ResponseWriter, r *http.Request) {
	rule, ok := s.registry.Get(r.PathValue("id"))
	if !ok {
		writeError(w, http.StatusNotFound, ruleengine.ErrRuleNotFound)
		return
	}
	writeJSON(w, http.StatusOK, rule)
}

func (s *HTTPServer) create(w http.ResponseWriter, r *http.Request) {
	var rule config.RuleConfig
	if err := json.NewDecoder(r.Body).Decode(&rule); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if rule.ID == "" {
		writeError(w, http.StatusBadRequest, errors.New("id is required"))
		return
	}
	applyDefaultsAndValidate(&rule)
	if err := s.registry.Create(r.Context(), rule); err != nil {
		writeRegistryError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, rule)
}

func (s *HTTPServer) update(w http.ResponseWriter, r *http.Request) {
	var rule config.RuleConfig
	if err := json.NewDecoder(r.Body).Decode(&rule); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	rule.ID = r.PathValue("id")
	applyDefaultsAndValidate(&rule)
	if err := s.registry.Update(r.Context(), rule); err != nil {
		writeRegistryError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, rule)
}

func (s *HTTPServer) delete(w http.ResponseWriter, r *http.Request) {
	if err := s.registry.Delete(r.Context(), r.PathValue("id")); err != nil {
		writeRegistryError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func applyDefaultsAndValidate(r *config.RuleConfig) {
	if r.Algorithm == "" {
		r.Algorithm = config.AlgorithmSlidingWindow
	}
	if r.FailMode == "" {
		r.FailMode = config.FailClosed
	}
	if r.Namespace == "" {
		r.Namespace = "default"
	}
}

func writeRegistryError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, ruleengine.ErrRuleNotFound):
		writeError(w, http.StatusNotFound, err)
	case errors.Is(err, ruleengine.ErrDuplicateRuleID):
		writeError(w, http.StatusConflict, err)
	default:
		writeError(w, http.StatusInternalServerError, err)
	}
}

func writeError(w http.ResponseWriter, status int, err error) {
	writeJSON(w, status, map[string]string{"error": err.Error()})
}

func writeJSON(w http.ResponseWriter, status int, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
