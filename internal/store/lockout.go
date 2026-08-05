package store

import (
	"context"
	"fmt"
	"time"
)

type LockoutStep struct {
	AfterViolations int
	Lockout         time.Duration
}

type LockoutResult struct {
	Locked     bool
	LockedFor  time.Duration
	Violations int64
}

// CheckLockout reports whether (namespace, ruleID, identity) is currently
// locked out, without recording a new violation.
func (s *Store) CheckLockout(ctx context.Context, namespace, ruleID, identity string, now time.Time) (*LockoutResult, error) {
	return s.runLockout(ctx, namespace, ruleID, identity, "check", 0, nil, now)
}

// RecordViolation registers one more consecutive violation for
// (namespace, ruleID, identity) and returns the resulting lockout state,
// escalating through steps as configured (FR5).
func (s *Store) RecordViolation(ctx context.Context, namespace, ruleID, identity string, violationTTL time.Duration, steps []LockoutStep, now time.Time) (*LockoutResult, error) {
	return s.runLockout(ctx, namespace, ruleID, identity, "violate", violationTTL, steps, now)
}

func (s *Store) runLockout(ctx context.Context, namespace, ruleID, identity, mode string, violationTTL time.Duration, steps []LockoutStep, now time.Time) (*LockoutResult, error) {
	tag := hashTagKey(namespace, ruleID, identity)
	key := fmt.Sprintf("unigate:%s:lockout", tag)

	args := make([]interface{}, 0, 4+len(steps)*2)
	args = append(args, nowMillis(now), mode, violationTTL.Milliseconds(), len(steps))
	for _, st := range steps {
		args = append(args, st.AfterViolations, st.Lockout.Milliseconds())
	}

	res, err := lockoutScript.Run(ctx, s.client, []string{key}, args...).Slice()
	if err != nil {
		return nil, fmt.Errorf("lockout script: %w", err)
	}
	if len(res) != 3 {
		return nil, fmt.Errorf("lockout script: unexpected result shape")
	}

	lockedUntilMs := toInt64(res[1])
	var lockedFor time.Duration
	if lockedUntilMs > 0 {
		lockedFor = time.UnixMilli(lockedUntilMs).Sub(now)
		if lockedFor < 0 {
			lockedFor = 0
		}
	}

	return &LockoutResult{
		Locked:     toInt64(res[0]) == 1,
		LockedFor:  lockedFor,
		Violations: toInt64(res[2]),
	}, nil
}
