package ruleengine

import (
	"context"
	"net"
	"os/exec"
	"testing"
	"time"

	"github.com/aupv9/unigate/internal/config"
	"github.com/aupv9/unigate/internal/store"
)

func startTestStore(t *testing.T) *store.Store {
	t.Helper()

	if _, err := exec.LookPath("redis-server"); err != nil {
		t.Skip("redis-server not found in PATH, skipping ruleengine integration tests")
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
			return s
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("redis-server did not become ready in time")
	return nil
}

func loginRule() config.RuleConfig {
	return config.RuleConfig{
		ID:        "login-brute-force",
		Algorithm: config.AlgorithmSlidingWindow,
		KeyParts:  []string{"ip", "username"},
		Namespace: "default",
		FailMode:  config.FailClosed,
		Windows: []config.WindowConfig{
			{Limit: 2, Period: config.Duration(time.Minute)},
		},
		Lockout: config.LockoutConfig{
			Enabled:      true,
			ViolationTTL: config.Duration(time.Hour),
			Steps: []config.LockoutStepConfig{
				{AfterViolations: 1, Lockout: config.Duration(time.Minute)},
				{AfterViolations: 2, Lockout: config.Duration(5 * time.Minute)},
			},
		},
	}
}

func newTestEngine(t *testing.T, rules ...config.RuleConfig) (*Engine, func(time.Time)) {
	t.Helper()
	s := startTestStore(t)
	registry := NewRegistry(rules, s.Client())
	engine := New(registry, s, nil, nil)
	setClock := func(now time.Time) {
		engine.clock = func() time.Time { return now }
	}
	return engine, setClock
}

func TestEngine_AllowsUnderLimitThenBlocksAndLocksOut(t *testing.T) {
	engine, setClock := newTestEngine(t, loginRule())
	base := time.Unix(1_700_000_000, 0)

	key := []KeyComponent{{Kind: "ip", Value: "1.2.3.4"}, {Kind: "username", Value: "alice"}}

	for i := 0; i < 2; i++ {
		setClock(base)
		res, err := engine.CheckLimit(context.Background(), CheckRequest{RuleID: "login-brute-force", Key: key, Gateway: "kong"})
		if err != nil {
			t.Fatalf("check %d: %v", i, err)
		}
		if !res.Allow {
			t.Fatalf("check %d expected allow, got blocked", i)
		}
	}

	// 3rd attempt exceeds the 2/min window -> blocked, 1st violation -> 1m lockout.
	setClock(base)
	res, err := engine.CheckLimit(context.Background(), CheckRequest{RuleID: "login-brute-force", Key: key, Gateway: "kong"})
	if err != nil {
		t.Fatalf("3rd check: %v", err)
	}
	if res.Allow {
		t.Fatalf("3rd check expected blocked")
	}
	if !res.LockedOut || res.LockoutRemainingSeconds != 60 {
		t.Fatalf("expected 60s lockout, got locked=%v remaining=%d", res.LockedOut, res.LockoutRemainingSeconds)
	}

	// Even after the rate-limit window would reset, the lockout itself
	// still blocks every request until it expires.
	setClock(base.Add(90 * time.Second))
	res, err = engine.CheckLimit(context.Background(), CheckRequest{RuleID: "login-brute-force", Key: key, Gateway: "kong"})
	if err != nil {
		t.Fatalf("post-lockout check: %v", err)
	}
	if !res.Allow {
		t.Fatalf("expected allowed once lockout (60s) has expired and rate window reset")
	}
}

func TestEngine_UnknownRuleReturnsError(t *testing.T) {
	engine, _ := newTestEngine(t, loginRule())
	_, err := engine.CheckLimit(context.Background(), CheckRequest{RuleID: "does-not-exist"})
	if err != ErrRuleNotFound {
		t.Fatalf("expected ErrRuleNotFound, got %v", err)
	}
}

func TestEngine_MissingKeyPartReturnsError(t *testing.T) {
	engine, _ := newTestEngine(t, loginRule())
	_, err := engine.CheckLimit(context.Background(), CheckRequest{
		RuleID: "login-brute-force",
		Key:    []KeyComponent{{Kind: "ip", Value: "1.2.3.4"}}, // missing username
	})
	if err != ErrMissingKeyPart {
		t.Fatalf("expected ErrMissingKeyPart, got %v", err)
	}
}

func TestEngine_FailOpenAllowsWhenStoreUnreachable(t *testing.T) {
	// Point the engine at a store whose Redis connection is already
	// closed, simulating an outage (FR10).
	s := startTestStore(t)
	rule := loginRule()
	rule.FailMode = config.FailOpen
	registry := NewRegistry([]config.RuleConfig{rule}, s.Client())
	engine := New(registry, s, nil, nil)
	s.Close() // force every subsequent Redis call to fail

	res, err := engine.CheckLimit(context.Background(), CheckRequest{
		RuleID: "login-brute-force",
		Key:    []KeyComponent{{Kind: "ip", Value: "1.2.3.4"}, {Kind: "username", Value: "alice"}},
	})
	if err != nil {
		t.Fatalf("expected no error on fail-open, got %v", err)
	}
	if !res.Allow || !res.FailedOpen {
		t.Fatalf("expected allow+failed_open, got %+v", res)
	}
}

func TestEngine_FailClosedBlocksWhenStoreUnreachable(t *testing.T) {
	s := startTestStore(t)
	rule := loginRule()
	rule.FailMode = config.FailClosed
	registry := NewRegistry([]config.RuleConfig{rule}, s.Client())
	engine := New(registry, s, nil, nil)
	s.Close()

	res, err := engine.CheckLimit(context.Background(), CheckRequest{
		RuleID: "login-brute-force",
		Key:    []KeyComponent{{Kind: "ip", Value: "1.2.3.4"}, {Kind: "username", Value: "alice"}},
	})
	if err != nil {
		t.Fatalf("expected no error on fail-closed, got %v", err)
	}
	if res.Allow || !res.FailedOpen {
		t.Fatalf("expected block+failed_open flag set, got %+v", res)
	}
}
