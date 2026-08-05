package store

import (
	"context"
	"fmt"
	"time"
)

type GCRASpec struct {
	Period time.Duration
	Limit  int64
	Burst  int64
}

type GCRAResult struct {
	Allowed    bool
	Remaining  int64
	RetryAfter time.Duration
	ResetAt    time.Time
}

// CheckGCRA evaluates a single GCRA bucket for (namespace, ruleID, identity).
// GCRA only ever models one rate (it doesn't stack multiple windows the way
// the sliding-window algorithm does), matching FR4's "choose per rule" split.
func (s *Store) CheckGCRA(ctx context.Context, namespace, ruleID, identity string, cost int64, spec GCRASpec, now time.Time) (*GCRAResult, error) {
	if spec.Limit <= 0 {
		return nil, fmt.Errorf("gcra spec requires limit > 0")
	}
	tag := hashTagKey(namespace, ruleID, identity)
	key := fmt.Sprintf("unigate:%s:gcra", tag)

	res, err := gcraScript.Run(ctx, s.client, []string{key},
		nowMillis(now), cost, spec.Period.Milliseconds(), spec.Limit, spec.Burst,
	).Slice()
	if err != nil {
		return nil, fmt.Errorf("gcra script: %w", err)
	}
	if len(res) != 4 {
		return nil, fmt.Errorf("gcra script: unexpected result shape")
	}

	return &GCRAResult{
		Allowed:    toInt64(res[0]) == 1,
		Remaining:  toInt64(res[1]),
		RetryAfter: time.Duration(toInt64(res[2])) * time.Millisecond,
		ResetAt:    time.UnixMilli(toInt64(res[3])),
	}, nil
}
