package store

import (
	"context"
	"testing"
	"time"
)

func TestCheckGCRA_AllowsBurstThenThrottles(t *testing.T) {
	s := startTestRedis(t)
	defer s.Close()

	ctx := context.Background()
	base := time.Unix(1_700_000_000, 0)
	spec := GCRASpec{Period: time.Minute, Limit: 60, Burst: 5} // ~1 req/sec steady, 5 burst

	for i := 0; i < 5; i++ {
		res, err := s.CheckGCRA(ctx, "ns", "rule", "1.2.3.4", 1, spec, base)
		if err != nil {
			t.Fatalf("burst request %d: %v", i, err)
		}
		if !res.Allowed {
			t.Fatalf("burst request %d expected allowed, got blocked (remaining=%d)", i, res.Remaining)
		}
	}

	res, err := s.CheckGCRA(ctx, "ns", "rule", "1.2.3.4", 1, spec, base)
	if err != nil {
		t.Fatalf("check: %v", err)
	}
	if res.Allowed {
		t.Fatalf("request immediately after exhausting burst should be throttled")
	}
	if res.RetryAfter <= 0 {
		t.Fatalf("expected positive retry-after, got %v", res.RetryAfter)
	}
}

func TestCheckGCRA_ReplenishesOverTime(t *testing.T) {
	s := startTestRedis(t)
	defer s.Close()

	ctx := context.Background()
	base := time.Unix(1_700_000_000, 0)
	spec := GCRASpec{Period: time.Minute, Limit: 60, Burst: 1} // 1 req/sec, no extra burst

	res, err := s.CheckGCRA(ctx, "ns", "rule", "1.2.3.4", 1, spec, base)
	if err != nil || !res.Allowed {
		t.Fatalf("first request should be allowed: res=%+v err=%v", res, err)
	}

	res, err = s.CheckGCRA(ctx, "ns", "rule", "1.2.3.4", 1, spec, base.Add(500*time.Millisecond))
	if err != nil {
		t.Fatalf("check: %v", err)
	}
	if res.Allowed {
		t.Fatalf("request 500ms later at 1 req/sec should be throttled")
	}

	res, err = s.CheckGCRA(ctx, "ns", "rule", "1.2.3.4", 1, spec, base.Add(1100*time.Millisecond))
	if err != nil {
		t.Fatalf("check: %v", err)
	}
	if !res.Allowed {
		t.Fatalf("request 1.1s later at 1 req/sec should be allowed")
	}
}
