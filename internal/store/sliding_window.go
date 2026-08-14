package store

import (
	"context"
	"fmt"
	"time"

	"go.opentelemetry.io/otel/attribute"
	oteltrace "go.opentelemetry.io/otel/trace"
)

// WindowSpec is one time-windowed threshold to evaluate atomically
// alongside its siblings (FR2).
type WindowSpec struct {
	Period time.Duration
	Limit  int64
}

// WindowResult reports the outcome for a single window after a
// CheckSlidingWindow call.
type WindowResult struct {
	Remaining int64
	Limit     int64
	ResetAt   time.Time
}

type SlidingWindowResult struct {
	Allowed      bool
	BlockedIndex int // 0-based index into the input windows, -1 if allowed
	RetryAfter   time.Duration
	Windows      []WindowResult
}

// CheckSlidingWindow evaluates every window for (namespace, ruleID, identity)
// in a single atomic Lua call: it only records the hit if every window would
// still allow it (NFR4).
func (s *Store) CheckSlidingWindow(ctx context.Context, namespace, ruleID, identity string, cost int64, windows []WindowSpec, now time.Time) (*SlidingWindowResult, error) {
	ctx, span := tracer.Start(ctx, "redis.sliding_window", oteltrace.WithAttributes(
		attribute.String("unigate.rule_id", ruleID),
		attribute.Int("unigate.window_count", len(windows)),
	))
	defer span.End()

	if len(windows) == 0 {
		return nil, fmt.Errorf("sliding window check requires at least one window")
	}
	tag := hashTagKey(namespace, ruleID, identity)
	keys := make([]string, len(windows))
	args := make([]interface{}, 0, 3+len(windows)*3)
	args = append(args, nowMillis(now), cost, len(windows))
	for i, w := range windows {
		keys[i] = fmt.Sprintf("unigate:%s:sw:%d", tag, w.Period.Milliseconds())
		ttl := w.Period + w.Period/2 + time.Second
		args = append(args, w.Period.Milliseconds(), w.Limit, ttl.Milliseconds())
	}

	res, err := slidingWindowScript.Run(ctx, s.client, keys, args...).Slice()
	if err != nil {
		return nil, fmt.Errorf("sliding window script: %w", err)
	}
	if len(res) < 3+len(windows)*3 {
		return nil, fmt.Errorf("sliding window script: unexpected result shape")
	}

	allowed := toInt64(res[0]) == 1
	blockedIdx := int(toInt64(res[1])) - 1
	retryAfterMs := toInt64(res[2])

	out := &SlidingWindowResult{
		Allowed:      allowed,
		BlockedIndex: blockedIdx,
		RetryAfter:   time.Duration(retryAfterMs) * time.Millisecond,
		Windows:      make([]WindowResult, len(windows)),
	}
	offset := 3
	for i := range windows {
		remaining := toInt64(res[offset])
		limit := toInt64(res[offset+1])
		resetMs := toInt64(res[offset+2])
		out.Windows[i] = WindowResult{
			Remaining: remaining,
			Limit:     limit,
			ResetAt:   time.UnixMilli(resetMs),
		}
		offset += 3
	}
	return out, nil
}

func toInt64(v interface{}) int64 {
	switch n := v.(type) {
	case int64:
		return n
	case int:
		return int64(n)
	default:
		return 0
	}
}
