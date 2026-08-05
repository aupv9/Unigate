package store

import (
	"context"
	"testing"
	"time"
)

func TestLockout_EscalatesAcrossSteps(t *testing.T) {
	s := startTestRedis(t)
	defer s.Close()

	ctx := context.Background()
	base := time.Unix(1_700_000_000, 0)
	steps := []LockoutStep{
		{AfterViolations: 1, Lockout: time.Minute},
		{AfterViolations: 3, Lockout: 5 * time.Minute},
		{AfterViolations: 6, Lockout: 30 * time.Minute},
	}
	violationTTL := time.Hour

	res, err := s.RecordViolation(ctx, "ns", "rule", "ip-a", violationTTL, steps, base)
	if err != nil {
		t.Fatalf("violation 1: %v", err)
	}
	if !res.Locked || res.LockedFor != time.Minute {
		t.Fatalf("expected 1m lockout after 1st violation, got locked=%v for=%v", res.Locked, res.LockedFor)
	}

	res, err = s.RecordViolation(ctx, "ns", "rule", "ip-a", violationTTL, steps, base.Add(2*time.Minute))
	if err != nil {
		t.Fatalf("violation 2: %v", err)
	}
	if res.Violations != 2 {
		t.Fatalf("expected violations=2, got %d", res.Violations)
	}

	res, err = s.RecordViolation(ctx, "ns", "rule", "ip-a", violationTTL, steps, base.Add(3*time.Minute))
	if err != nil {
		t.Fatalf("violation 3: %v", err)
	}
	if !res.Locked || res.LockedFor != 5*time.Minute {
		t.Fatalf("expected 5m lockout after 3rd violation, got locked=%v for=%v", res.Locked, res.LockedFor)
	}
}

func TestLockout_CheckWithoutViolationIsNeverLocked(t *testing.T) {
	s := startTestRedis(t)
	defer s.Close()

	ctx := context.Background()
	res, err := s.CheckLockout(ctx, "ns", "rule", "fresh-ip", time.Unix(1_700_000_000, 0))
	if err != nil {
		t.Fatalf("check lockout: %v", err)
	}
	if res.Locked {
		t.Fatalf("expected fresh identity to not be locked")
	}
}

func TestLockout_ExpiresAfterDuration(t *testing.T) {
	s := startTestRedis(t)
	defer s.Close()

	ctx := context.Background()
	base := time.Unix(1_700_000_000, 0)
	steps := []LockoutStep{{AfterViolations: 1, Lockout: time.Minute}}

	if _, err := s.RecordViolation(ctx, "ns", "rule", "ip-a", time.Hour, steps, base); err != nil {
		t.Fatalf("violation: %v", err)
	}

	res, err := s.CheckLockout(ctx, "ns", "rule", "ip-a", base.Add(30*time.Second))
	if err != nil || !res.Locked {
		t.Fatalf("expected still locked at 30s: res=%+v err=%v", res, err)
	}

	res, err = s.CheckLockout(ctx, "ns", "rule", "ip-a", base.Add(61*time.Second))
	if err != nil {
		t.Fatalf("check lockout: %v", err)
	}
	if res.Locked {
		t.Fatalf("expected lockout to have expired after 61s")
	}
}
