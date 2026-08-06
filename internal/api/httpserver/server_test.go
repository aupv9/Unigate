package httpserver

import (
	"bytes"
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"testing"
	"time"

	"github.com/aupv9/unigate/internal/config"
	"github.com/aupv9/unigate/internal/ruleengine"
	"github.com/aupv9/unigate/internal/store"
)

func startTestEngine(t *testing.T) *ruleengine.Engine {
	t.Helper()

	if _, err := exec.LookPath("redis-server"); err != nil {
		t.Skip("redis-server not found in PATH, skipping httpserver integration tests")
	}

	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("find free port: %v", err)
	}
	addr := lis.Addr().String()
	lis.Close()

	cmd := exec.Command("redis-server", "--port", addr[len("127.0.0.1:"):], "--bind", "127.0.0.1", "--save", "", "--appendonly", "no")
	if err := cmd.Start(); err != nil {
		t.Fatalf("start redis-server: %v", err)
	}
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	})

	s, err := store.New(config.RedisConfig{
		Addrs:        []string{addr},
		DialTimeout:  config.Duration(2 * time.Second),
		ReadTimeout:  config.Duration(2 * time.Second),
		WriteTimeout: config.Duration(2 * time.Second),
	})
	if err != nil {
		t.Fatalf("new store: %v", err)
	}

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
		err := s.Ping(ctx)
		cancel()
		if err == nil {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}

	rule := config.RuleConfig{
		ID:        "test-rule",
		Algorithm: config.AlgorithmSlidingWindow,
		KeyParts:  []string{"ip"},
		Namespace: "default",
		FailMode:  config.FailClosed,
		Windows: []config.WindowConfig{
			{Limit: 2, Period: config.Duration(time.Minute)},
		},
	}
	registry := ruleengine.NewRegistry([]config.RuleConfig{rule}, s.Client())
	return ruleengine.New(registry, s, nil, nil)
}

func doCheck(t *testing.T, srv *Server, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/v1/check", bytes.NewBufferString(body))
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	return rec
}

func TestHandleCheck_AllowsAndSetsHeaders(t *testing.T) {
	engine := startTestEngine(t)
	srv := New(engine)

	rec := doCheck(t, srv, `{"rule_id":"test-rule","key":[{"kind":"ip","value":"1.2.3.4"}],"gateway":"kong"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("X-RateLimit-Limit"); got != "2" {
		t.Errorf("expected X-RateLimit-Limit=2, got %q", got)
	}
	if got := rec.Header().Get("X-RateLimit-Remaining"); got != "1" {
		t.Errorf("expected X-RateLimit-Remaining=1, got %q", got)
	}

	var resp checkResponseDTO
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if !resp.Allow {
		t.Fatalf("expected allow=true, got %+v", resp)
	}
}

func TestHandleCheck_BlocksWithRetryAfterAnd429(t *testing.T) {
	engine := startTestEngine(t)
	srv := New(engine)
	body := `{"rule_id":"test-rule","key":[{"kind":"ip","value":"5.6.7.8"}],"gateway":"kong"}`

	for i := 0; i < 2; i++ {
		rec := doCheck(t, srv, body)
		if rec.Code != http.StatusOK {
			t.Fatalf("request %d: expected 200, got %d", i, rec.Code)
		}
	}

	rec := doCheck(t, srv, body)
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("expected 429, got %d: %s", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("Retry-After"); got == "" {
		t.Errorf("expected Retry-After header to be set")
	}
}

func TestHandleCheck_MissingRuleID(t *testing.T) {
	engine := startTestEngine(t)
	srv := New(engine)

	rec := doCheck(t, srv, `{"key":[{"kind":"ip","value":"1.2.3.4"}]}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", rec.Code)
	}
}

func TestHandleCheck_UnknownRule(t *testing.T) {
	engine := startTestEngine(t)
	srv := New(engine)

	rec := doCheck(t, srv, `{"rule_id":"does-not-exist","key":[{"kind":"ip","value":"1.2.3.4"}]}`)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestHandleReset_ClearsCounters(t *testing.T) {
	engine := startTestEngine(t)
	srv := New(engine)
	body := `{"rule_id":"test-rule","key":[{"kind":"ip","value":"9.9.9.9"}],"gateway":"kong"}`

	for i := 0; i < 2; i++ {
		if rec := doCheck(t, srv, body); rec.Code != http.StatusOK {
			t.Fatalf("request %d: expected 200, got %d", i, rec.Code)
		}
	}
	if rec := doCheck(t, srv, body); rec.Code != http.StatusTooManyRequests {
		t.Fatalf("expected blocked before reset, got %d", rec.Code)
	}

	resetReq := httptest.NewRequest(http.MethodPost, "/v1/reset", bytes.NewBufferString(
		`{"rule_id":"test-rule","key":[{"kind":"ip","value":"9.9.9.9"}]}`))
	resetRec := httptest.NewRecorder()
	srv.ServeHTTP(resetRec, resetReq)
	if resetRec.Code != http.StatusOK {
		t.Fatalf("reset expected 200, got %d: %s", resetRec.Code, resetRec.Body.String())
	}

	if rec := doCheck(t, srv, body); rec.Code != http.StatusOK {
		t.Fatalf("expected allowed after reset, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestHandleHealthz(t *testing.T) {
	engine := startTestEngine(t)
	srv := New(engine)

	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	var body map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body["status"] != "ok" {
		t.Fatalf("expected status=ok, got %v", body)
	}
}
