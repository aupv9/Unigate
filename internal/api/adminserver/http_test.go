package adminserver

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/aupv9/unigate/internal/config"
	"github.com/aupv9/unigate/internal/ruleengine"
)

func newTestHTTPServer() *HTTPServer {
	registry := ruleengine.NewRegistry(nil, nil)
	return NewHTTPServer(registry)
}

func TestHTTP_CreateGetListDeleteRule(t *testing.T) {
	srv := newTestHTTPServer()

	rule := config.RuleConfig{
		ID:        "r1",
		KeyParts:  []string{"ip"},
		Windows:   []config.WindowConfig{{Limit: 5, Period: config.Duration(time.Minute)}},
		Namespace: "default",
	}
	body, _ := json.Marshal(rule)

	// Create
	req := httptest.NewRequest(http.MethodPost, "/v1/admin/rules", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create: expected 201, got %d: %s", rec.Code, rec.Body.String())
	}

	// Duplicate create -> conflict
	req = httptest.NewRequest(http.MethodPost, "/v1/admin/rules", bytes.NewReader(body))
	rec = httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusConflict {
		t.Fatalf("duplicate create: expected 409, got %d", rec.Code)
	}

	// Get
	req = httptest.NewRequest(http.MethodGet, "/v1/admin/rules/r1", nil)
	rec = httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("get: expected 200, got %d", rec.Code)
	}
	var got config.RuleConfig
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.ID != "r1" || got.Algorithm != config.AlgorithmSlidingWindow {
		t.Fatalf("unexpected rule after defaults applied: %+v", got)
	}

	// List
	req = httptest.NewRequest(http.MethodGet, "/v1/admin/rules", nil)
	rec = httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	var list []config.RuleConfig
	if err := json.Unmarshal(rec.Body.Bytes(), &list); err != nil {
		t.Fatalf("decode list: %v", err)
	}
	if len(list) != 1 {
		t.Fatalf("expected 1 rule, got %d", len(list))
	}

	// Update
	rule.Description = "updated"
	body, _ = json.Marshal(rule)
	req = httptest.NewRequest(http.MethodPut, "/v1/admin/rules/r1", bytes.NewReader(body))
	rec = httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("update: expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	// Delete
	req = httptest.NewRequest(http.MethodDelete, "/v1/admin/rules/r1", nil)
	rec = httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("delete: expected 200, got %d", rec.Code)
	}

	// Get after delete -> 404
	req = httptest.NewRequest(http.MethodGet, "/v1/admin/rules/r1", nil)
	rec = httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("get after delete: expected 404, got %d", rec.Code)
	}
}

func TestHTTP_CreateMissingIDReturnsBadRequest(t *testing.T) {
	srv := newTestHTTPServer()
	body := []byte(`{"key_parts":["ip"],"windows":[{"limit":5,"period":"1m"}]}`)
	req := httptest.NewRequest(http.MethodPost, "/v1/admin/rules", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", rec.Code)
	}
}

func TestHTTP_UpdateNonExistentReturnsNotFound(t *testing.T) {
	srv := newTestHTTPServer()
	body := []byte(`{"key_parts":["ip"],"windows":[{"limit":5,"period":"1m"}]}`)
	req := httptest.NewRequest(http.MethodPut, "/v1/admin/rules/does-not-exist", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", rec.Code)
	}
}

func TestHTTP_DeleteNonExistentReturnsNotFound(t *testing.T) {
	srv := newTestHTTPServer()
	req := httptest.NewRequest(http.MethodDelete, "/v1/admin/rules/does-not-exist", nil)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", rec.Code)
	}
}

func createTestRule(t *testing.T, srv *HTTPServer, id string, limit int64) {
	t.Helper()
	rule := config.RuleConfig{
		ID:       id,
		KeyParts: []string{"ip"},
		Windows:  []config.WindowConfig{{Limit: limit, Period: config.Duration(time.Minute)}},
	}
	body, _ := json.Marshal(rule)
	req := httptest.NewRequest(http.MethodPost, "/v1/admin/rules", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create %s: expected 201, got %d: %s", id, rec.Code, rec.Body.String())
	}
}

func updateTestRule(t *testing.T, srv *HTTPServer, id string, limit int64) {
	t.Helper()
	rule := config.RuleConfig{
		ID:       id,
		KeyParts: []string{"ip"},
		Windows:  []config.WindowConfig{{Limit: limit, Period: config.Duration(time.Minute)}},
	}
	body, _ := json.Marshal(rule)
	req := httptest.NewRequest(http.MethodPut, "/v1/admin/rules/"+id, bytes.NewReader(body))
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("update %s: expected 200, got %d: %s", id, rec.Code, rec.Body.String())
	}
}

func TestHTTP_VersionsAndRollback(t *testing.T) {
	srv := newTestHTTPServer()
	createTestRule(t, srv, "r1", 5)
	updateTestRule(t, srv, "r1", 10)
	updateTestRule(t, srv, "r1", 20)

	// GET versions
	req := httptest.NewRequest(http.MethodGet, "/v1/admin/rules/r1/versions", nil)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("versions: expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var hist []ruleengine.RuleVersion
	if err := json.Unmarshal(rec.Body.Bytes(), &hist); err != nil {
		t.Fatalf("decode versions: %v", err)
	}
	if len(hist) != 2 {
		t.Fatalf("expected 2 history entries, got %d: %+v", len(hist), hist)
	}
	if hist[0].Rule.Windows[0].Limit != 10 {
		t.Errorf("expected hist[0] limit=10, got %+v", hist[0])
	}

	// Rollback to previous (limit=10)
	req = httptest.NewRequest(http.MethodPost, "/v1/admin/rules/r1/rollback", nil)
	rec = httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("rollback: expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var rolledBack config.RuleConfig
	if err := json.Unmarshal(rec.Body.Bytes(), &rolledBack); err != nil {
		t.Fatalf("decode rollback response: %v", err)
	}
	if rolledBack.Windows[0].Limit != 10 {
		t.Fatalf("expected rollback to restore limit=10, got %+v", rolledBack)
	}

	// Rollback to a specific version (limit=5)
	req = httptest.NewRequest(http.MethodPost, "/v1/admin/rules/r1/rollback", bytes.NewReader([]byte(`{"version":1}`)))
	rec = httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("rollback to v1: expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &rolledBack); err != nil {
		t.Fatalf("decode rollback response: %v", err)
	}
	if rolledBack.Windows[0].Limit != 5 {
		t.Fatalf("expected rollback to v1 to restore limit=5, got %+v", rolledBack)
	}
}

func TestHTTP_VersionsUnknownRuleReturnsNotFound(t *testing.T) {
	srv := newTestHTTPServer()
	req := httptest.NewRequest(http.MethodGet, "/v1/admin/rules/does-not-exist/versions", nil)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", rec.Code)
	}
}

func TestHTTP_RollbackNoHistoryReturnsConflict(t *testing.T) {
	srv := newTestHTTPServer()
	createTestRule(t, srv, "r1", 5)

	req := httptest.NewRequest(http.MethodPost, "/v1/admin/rules/r1/rollback", nil)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusConflict {
		t.Fatalf("expected 409, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestHTTP_RollbackUnknownVersionReturnsNotFound(t *testing.T) {
	srv := newTestHTTPServer()
	createTestRule(t, srv, "r1", 5)
	updateTestRule(t, srv, "r1", 10)

	req := httptest.NewRequest(http.MethodPost, "/v1/admin/rules/r1/rollback", bytes.NewReader([]byte(`{"version":999}`)))
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d: %s", rec.Code, rec.Body.String())
	}
}
