package store

import (
	"context"
	"testing"
	"time"
)

func TestCheckSlidingWindow_SingleWindowAllowsUpToLimit(t *testing.T) {
	s := startTestRedis(t)
	defer s.Close()

	ctx := context.Background()
	base := time.Unix(1_700_000_000, 0)
	windows := []WindowSpec{{Period: time.Minute, Limit: 3}}

	for i := 0; i < 3; i++ {
		res, err := s.CheckSlidingWindow(ctx, "ns", "rule", "1.2.3.4", 1, windows, base)
		if err != nil {
			t.Fatalf("check %d: %v", i, err)
		}
		if !res.Allowed {
			t.Fatalf("request %d expected allowed, got blocked", i)
		}
	}

	res, err := s.CheckSlidingWindow(ctx, "ns", "rule", "1.2.3.4", 1, windows, base)
	if err != nil {
		t.Fatalf("4th check: %v", err)
	}
	if res.Allowed {
		t.Fatalf("4th request expected blocked, got allowed")
	}
	if res.BlockedIndex != 0 {
		t.Fatalf("expected blocked index 0, got %d", res.BlockedIndex)
	}
}

func TestCheckSlidingWindow_WindowSlidesOverTime(t *testing.T) {
	s := startTestRedis(t)
	defer s.Close()

	ctx := context.Background()
	base := time.Unix(1_700_000_000, 0)
	windows := []WindowSpec{{Period: time.Minute, Limit: 1}}

	res, err := s.CheckSlidingWindow(ctx, "ns", "rule", "1.2.3.4", 1, windows, base)
	if err != nil || !res.Allowed {
		t.Fatalf("first request should be allowed: res=%+v err=%v", res, err)
	}

	res, err = s.CheckSlidingWindow(ctx, "ns", "rule", "1.2.3.4", 1, windows, base.Add(30*time.Second))
	if err != nil {
		t.Fatalf("check: %v", err)
	}
	if res.Allowed {
		t.Fatalf("second request within window should be blocked")
	}

	res, err = s.CheckSlidingWindow(ctx, "ns", "rule", "1.2.3.4", 1, windows, base.Add(61*time.Second))
	if err != nil {
		t.Fatalf("check: %v", err)
	}
	if !res.Allowed {
		t.Fatalf("request after window elapsed should be allowed")
	}
}

func TestCheckSlidingWindow_MultiWindowBlocksOnTighterWindow(t *testing.T) {
	s := startTestRedis(t)
	defer s.Close()

	ctx := context.Background()
	base := time.Unix(1_700_000_000, 0)
	windows := []WindowSpec{
		{Period: time.Minute, Limit: 2},
		{Period: time.Hour, Limit: 100},
	}

	for i := 0; i < 2; i++ {
		res, err := s.CheckSlidingWindow(ctx, "ns", "rule", "user1", 1, windows, base)
		if err != nil || !res.Allowed {
			t.Fatalf("request %d should be allowed: res=%+v err=%v", i, res, err)
		}
	}

	res, err := s.CheckSlidingWindow(ctx, "ns", "rule", "user1", 1, windows, base)
	if err != nil {
		t.Fatalf("check: %v", err)
	}
	if res.Allowed {
		t.Fatalf("3rd request should be blocked by the 1-minute window")
	}
	if res.BlockedIndex != 0 {
		t.Fatalf("expected the minute window (index 0) to block, got index %d", res.BlockedIndex)
	}
	// The hourly window's own remaining count should still reflect only
	// the 2 successful hits, proving the blocked request wasn't recorded.
	if res.Windows[1].Remaining != 98 {
		t.Fatalf("expected hourly window remaining=98, got %d", res.Windows[1].Remaining)
	}
}

func TestCheckSlidingWindow_DifferentIdentitiesAreIsolated(t *testing.T) {
	s := startTestRedis(t)
	defer s.Close()

	ctx := context.Background()
	base := time.Unix(1_700_000_000, 0)
	windows := []WindowSpec{{Period: time.Minute, Limit: 1}}

	res, err := s.CheckSlidingWindow(ctx, "ns", "rule", "ip-a", 1, windows, base)
	if err != nil || !res.Allowed {
		t.Fatalf("ip-a should be allowed: res=%+v err=%v", res, err)
	}
	res, err = s.CheckSlidingWindow(ctx, "ns", "rule", "ip-b", 1, windows, base)
	if err != nil || !res.Allowed {
		t.Fatalf("ip-b should be allowed independently: res=%+v err=%v", res, err)
	}
}
