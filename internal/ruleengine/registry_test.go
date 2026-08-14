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

func testRule(id string, limit int64) config.RuleConfig {
	return config.RuleConfig{
		ID:       id,
		KeyParts: []string{"ip"},
		Windows:  []config.WindowConfig{{Limit: limit, Period: config.Duration(time.Minute)}},
	}
}

func TestRegistry_InMemory_UpdateVersionsAndHistory(t *testing.T) {
	ctx := context.Background()
	reg := NewRegistry(nil, nil)

	if err := reg.Create(ctx, testRule("r1", 5)); err != nil {
		t.Fatalf("create: %v", err)
	}
	rule, _ := reg.Get("r1")
	if rule.Windows[0].Limit != 5 {
		t.Fatalf("unexpected initial rule: %+v", rule)
	}

	hist, err := reg.History(ctx, "r1")
	if err != nil {
		t.Fatalf("history: %v", err)
	}
	if len(hist) != 0 {
		t.Fatalf("expected no history right after create, got %+v", hist)
	}

	if err := reg.Update(ctx, testRule("r1", 10)); err != nil {
		t.Fatalf("update 1: %v", err)
	}
	if err := reg.Update(ctx, testRule("r1", 20)); err != nil {
		t.Fatalf("update 2: %v", err)
	}

	rule, _ = reg.Get("r1")
	if rule.Windows[0].Limit != 20 {
		t.Fatalf("expected current limit=20, got %+v", rule)
	}

	hist, err = reg.History(ctx, "r1")
	if err != nil {
		t.Fatalf("history: %v", err)
	}
	if len(hist) != 2 {
		t.Fatalf("expected 2 history entries, got %d: %+v", len(hist), hist)
	}
	// Newest-first: the most recent history entry is the limit=10
	// version (superseded by the limit=20 update), then limit=5.
	if hist[0].Rule.Windows[0].Limit != 10 || hist[0].Version != 2 {
		t.Errorf("expected hist[0] to be version 2 (limit=10), got %+v", hist[0])
	}
	if hist[1].Rule.Windows[0].Limit != 5 || hist[1].Version != 1 {
		t.Errorf("expected hist[1] to be version 1 (limit=5), got %+v", hist[1])
	}
}

func TestRegistry_Rollback_ToPreviousVersion(t *testing.T) {
	ctx := context.Background()
	reg := NewRegistry(nil, nil)

	_ = reg.Create(ctx, testRule("r1", 5))  // v1
	_ = reg.Update(ctx, testRule("r1", 10)) // v2

	rolledBack, err := reg.Rollback(ctx, "r1", 0) // 0 = previous version (v1, limit=5)
	if err != nil {
		t.Fatalf("rollback: %v", err)
	}
	if rolledBack.Windows[0].Limit != 5 {
		t.Fatalf("expected rollback to restore limit=5, got %+v", rolledBack)
	}

	// Rollback creates a NEW version (v3) rather than rewinding -
	// history must still contain everything, forward-only.
	current, _ := reg.Get("r1")
	if current.Windows[0].Limit != 5 {
		t.Fatalf("expected current rule to now have limit=5, got %+v", current)
	}

	hist, _ := reg.History(ctx, "r1")
	if len(hist) != 2 {
		t.Fatalf("expected 2 history entries after rollback, got %d: %+v", len(hist), hist)
	}
	if hist[0].Version != 2 || hist[0].Rule.Windows[0].Limit != 10 {
		t.Errorf("expected the just-superseded v2 (limit=10) at hist[0], got %+v", hist[0])
	}
}

func TestRegistry_Rollback_ToSpecificVersion(t *testing.T) {
	ctx := context.Background()
	reg := NewRegistry(nil, nil)

	_ = reg.Create(ctx, testRule("r1", 5))  // v1
	_ = reg.Update(ctx, testRule("r1", 10)) // v2
	_ = reg.Update(ctx, testRule("r1", 20)) // v3

	rolledBack, err := reg.Rollback(ctx, "r1", 1) // explicitly back to v1 (limit=5)
	if err != nil {
		t.Fatalf("rollback: %v", err)
	}
	if rolledBack.Windows[0].Limit != 5 {
		t.Fatalf("expected rollback to v1 (limit=5), got %+v", rolledBack)
	}
}

func TestRegistry_Rollback_UnknownVersionErrors(t *testing.T) {
	ctx := context.Background()
	reg := NewRegistry(nil, nil)
	_ = reg.Create(ctx, testRule("r1", 5))
	_ = reg.Update(ctx, testRule("r1", 10))

	_, err := reg.Rollback(ctx, "r1", 999)
	if err == nil {
		t.Fatalf("expected error for unknown version")
	}
}

func TestRegistry_Rollback_NoHistoryErrors(t *testing.T) {
	ctx := context.Background()
	reg := NewRegistry(nil, nil)
	_ = reg.Create(ctx, testRule("r1", 5))

	_, err := reg.Rollback(ctx, "r1", 0)
	if err != ErrNoHistory {
		t.Fatalf("expected ErrNoHistory, got %v", err)
	}
}

func TestRegistry_Rollback_UnknownRuleErrors(t *testing.T) {
	reg := NewRegistry(nil, nil)
	_, err := reg.Rollback(context.Background(), "does-not-exist", 0)
	if err != ErrRuleNotFound {
		t.Fatalf("expected ErrRuleNotFound, got %v", err)
	}
}

// --- Redis-backed tests: prove versioning survives Refresh() (i.e.
// converges across stateless instances, NFR3), and that history is
// readable from a second Registry instance sharing the same Redis. ---

func startRegistryTestRedis(t *testing.T) *store.Store {
	t.Helper()
	if _, err := exec.LookPath("redis-server"); err != nil {
		t.Skip("redis-server not found in PATH, skipping registry Redis tests")
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
	t.Cleanup(func() { s.Close() })

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		pingCtx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
		err := s.Ping(pingCtx)
		cancel()
		if err == nil {
			return s
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("redis-server did not become ready in time")
	return nil
}

func TestRegistry_Redis_VersionSurvivesRefreshOnAnotherInstance(t *testing.T) {
	s := startRegistryTestRedis(t)
	ctx := context.Background()

	writer := NewRegistry(nil, s.Client())
	_ = writer.Create(ctx, testRule("r1", 5))
	_ = writer.Update(ctx, testRule("r1", 10))
	_ = writer.Update(ctx, testRule("r1", 20))

	// A second "instance" starts fresh and must Refresh() to the same
	// rule content AND the same version number (3) - not just the
	// content, which is the part that would silently make Rollback's
	// version numbers diverge across instances if unhandled.
	reader := NewRegistry(nil, s.Client())
	if err := reader.Refresh(ctx); err != nil {
		t.Fatalf("refresh: %v", err)
	}
	rule, ok := reader.Get("r1")
	if !ok || rule.Windows[0].Limit != 20 {
		t.Fatalf("expected reader to see limit=20 after refresh, got ok=%v rule=%+v", ok, rule)
	}

	// The reader instance's own next Update should produce version 4,
	// not restart from 2 - proving it adopted writer's version 3.
	if err := reader.Update(ctx, testRule("r1", 30)); err != nil {
		t.Fatalf("reader update: %v", err)
	}
	hist, err := reader.History(ctx, "r1")
	if err != nil {
		t.Fatalf("history: %v", err)
	}
	if len(hist) == 0 || hist[0].Version != 3 {
		t.Fatalf("expected reader's update to supersede version 3, got history %+v", hist)
	}
}

func TestRegistry_Redis_HistoryVisibleAcrossInstances(t *testing.T) {
	s := startRegistryTestRedis(t)
	ctx := context.Background()

	writer := NewRegistry(nil, s.Client())
	_ = writer.Create(ctx, testRule("r1", 5))
	_ = writer.Update(ctx, testRule("r1", 10))

	reader := NewRegistry(nil, s.Client())
	if err := reader.Refresh(ctx); err != nil {
		t.Fatalf("refresh: %v", err)
	}

	hist, err := reader.History(ctx, "r1")
	if err != nil {
		t.Fatalf("history: %v", err)
	}
	if len(hist) != 1 || hist[0].Rule.Windows[0].Limit != 5 {
		t.Fatalf("expected reader to see the v1 (limit=5) history entry written by writer, got %+v", hist)
	}

	rolledBack, err := reader.Rollback(ctx, "r1", 0)
	if err != nil {
		t.Fatalf("rollback: %v", err)
	}
	if rolledBack.Windows[0].Limit != 5 {
		t.Fatalf("expected rollback to restore limit=5, got %+v", rolledBack)
	}
}

func TestRegistry_Redis_HistoryIsCappedAtMax(t *testing.T) {
	s := startRegistryTestRedis(t)
	ctx := context.Background()

	reg := NewRegistry(nil, s.Client())
	_ = reg.Create(ctx, testRule("r1", 1))
	for i := 2; i <= maxHistoryEntries+5; i++ {
		if err := reg.Update(ctx, testRule("r1", int64(i))); err != nil {
			t.Fatalf("update %d: %v", i, err)
		}
	}

	hist, err := reg.History(ctx, "r1")
	if err != nil {
		t.Fatalf("history: %v", err)
	}
	if len(hist) != maxHistoryEntries {
		t.Fatalf("expected history capped at %d entries, got %d", maxHistoryEntries, len(hist))
	}
}
