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
