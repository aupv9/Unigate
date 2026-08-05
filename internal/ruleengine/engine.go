package ruleengine

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/aupv9/unigate/internal/config"
	"github.com/aupv9/unigate/internal/store"
)

// AuditFunc is called once per CheckLimit decision so the caller can
// wire it up to metrics + audit logging (FR9) without ruleengine
// depending on those packages directly.
type AuditFunc func(evt AuditEvent)

type AuditEvent struct {
	Gateway    string
	RuleID     string
	Namespace  string
	Identity   string
	Allow      bool
	LockedOut  bool
	FailedOpen bool
}

type Engine struct {
	registry *Registry
	store    *store.Store
	clock    func() time.Time
	audit    AuditFunc
	log      *slog.Logger
}

func New(registry *Registry, s *store.Store, log *slog.Logger, audit AuditFunc) *Engine {
	if log == nil {
		log = slog.Default()
	}
	if audit == nil {
		audit = func(AuditEvent) {}
	}
	return &Engine{
		registry: registry,
		store:    s,
		clock:    time.Now,
		audit:    audit,
		log:      log,
	}
}

func (e *Engine) CheckLimit(ctx context.Context, req CheckRequest) (*CheckResult, error) {
	rule, ok := e.registry.Get(req.RuleID)
	if !ok {
		return nil, ErrRuleNotFound
	}

	namespace := rule.Namespace
	if req.Namespace != "" {
		namespace = req.Namespace
	}

	identity, err := buildIdentity(rule.KeyParts, req.Key)
	if err != nil {
		return nil, err
	}

	cost := req.Cost
	if cost <= 0 {
		cost = 1
	}

	now := e.clock()

	result, err := e.evaluate(ctx, rule, namespace, identity, cost, now)
	if err != nil {
		failOpen := rule.FailMode == config.FailOpen
		e.log.Error("ruleengine: store error, applying fail mode",
			"rule_id", rule.ID, "fail_mode", rule.FailMode, "fail_open", failOpen, "err", err)
		e.audit(AuditEvent{
			Gateway: req.Gateway, RuleID: rule.ID, Namespace: namespace,
			Identity: identity, Allow: failOpen, FailedOpen: true,
		})
		return &CheckResult{
			Allow:      failOpen,
			FailedOpen: true,
			Namespace:  namespace,
		}, nil
	}

	e.audit(AuditEvent{
		Gateway: req.Gateway, RuleID: rule.ID, Namespace: namespace,
		Identity: identity, Allow: result.Allow, LockedOut: result.LockedOut,
	})
	result.Namespace = namespace
	return result, nil
}

func (e *Engine) evaluate(ctx context.Context, rule config.RuleConfig, namespace, identity string, cost int64, now time.Time) (*CheckResult, error) {
	// A key already under an escalated lockout is rejected outright,
	// without spending any of its restored rate-limit budget (FR5).
	if rule.Lockout.Enabled {
		lockState, err := e.store.CheckLockout(ctx, namespace, rule.ID, identity, now)
		if err != nil {
			return nil, fmt.Errorf("check lockout: %w", err)
		}
		if lockState.Locked {
			return &CheckResult{
				Allow:                   false,
				LockedOut:               true,
				LockoutRemainingSeconds: int64(lockState.LockedFor.Seconds()),
				RetryAfterSeconds:       int64(lockState.LockedFor.Seconds()),
			}, nil
		}
	}

	var result *CheckResult
	var err error
	switch rule.Algorithm {
	case config.AlgorithmGCRA:
		result, err = e.checkGCRA(ctx, rule, namespace, identity, cost, now)
	default:
		result, err = e.checkSlidingWindow(ctx, rule, namespace, identity, cost, now)
	}
	if err != nil {
		return nil, err
	}

	if !result.Allow && rule.Lockout.Enabled {
		steps := make([]store.LockoutStep, len(rule.Lockout.Steps))
		for i, s := range rule.Lockout.Steps {
			steps[i] = store.LockoutStep{AfterViolations: s.AfterViolations, Lockout: s.Lockout.Duration()}
		}
		lockState, err := e.store.RecordViolation(ctx, namespace, rule.ID, identity, rule.Lockout.ViolationTTL.Duration(), steps, now)
		if err != nil {
			return nil, fmt.Errorf("record violation: %w", err)
		}
		if lockState.Locked {
			result.LockedOut = true
			result.LockoutRemainingSeconds = int64(lockState.LockedFor.Seconds())
			result.RetryAfterSeconds = int64(lockState.LockedFor.Seconds())
		}
	}

	return result, nil
}

func (e *Engine) checkSlidingWindow(ctx context.Context, rule config.RuleConfig, namespace, identity string, cost int64, now time.Time) (*CheckResult, error) {
	windows := make([]store.WindowSpec, len(rule.Windows))
	for i, w := range rule.Windows {
		windows[i] = store.WindowSpec{Period: w.Period.Duration(), Limit: w.Limit}
	}

	res, err := e.store.CheckSlidingWindow(ctx, namespace, rule.ID, identity, cost, windows, now)
	if err != nil {
		return nil, fmt.Errorf("sliding window: %w", err)
	}

	out := &CheckResult{Allow: res.Allowed}
	// Report the tightest (last, i.e. most restrictive to breach) window's
	// remaining/limit by default; override with the window that blocked.
	reportIdx := len(res.Windows) - 1
	if !res.Allowed && res.BlockedIndex >= 0 {
		reportIdx = res.BlockedIndex
		out.MatchedWindow = fmt.Sprintf("%s", rule.Windows[reportIdx].Period)
		out.RetryAfterSeconds = int64(res.RetryAfter.Seconds())
		if out.RetryAfterSeconds <= 0 && res.RetryAfter > 0 {
			out.RetryAfterSeconds = 1
		}
	}
	if reportIdx >= 0 && reportIdx < len(res.Windows) {
		w := res.Windows[reportIdx]
		out.Limit = w.Limit
		out.Remaining = w.Remaining
		out.ResetSeconds = int64(time.Until(w.ResetAt).Seconds())
		if out.ResetSeconds < 0 {
			out.ResetSeconds = 0
		}
	}
	return out, nil
}

func (e *Engine) checkGCRA(ctx context.Context, rule config.RuleConfig, namespace, identity string, cost int64, now time.Time) (*CheckResult, error) {
	if len(rule.Windows) == 0 {
		return nil, fmt.Errorf("gcra rule %s has no window configured", rule.ID)
	}
	w := rule.Windows[0]
	res, err := e.store.CheckGCRA(ctx, namespace, rule.ID, identity, cost, store.GCRASpec{
		Period: w.Period.Duration(),
		Limit:  w.Limit,
		Burst:  rule.Burst,
	}, now)
	if err != nil {
		return nil, fmt.Errorf("gcra: %w", err)
	}

	out := &CheckResult{
		Allow:     res.Allowed,
		Limit:     w.Limit,
		Remaining: res.Remaining,
	}
	out.ResetSeconds = int64(time.Until(res.ResetAt).Seconds())
	if out.ResetSeconds < 0 {
		out.ResetSeconds = 0
	}
	if !res.Allowed {
		out.RetryAfterSeconds = int64(res.RetryAfter.Seconds())
		if out.RetryAfterSeconds <= 0 && res.RetryAfter > 0 {
			out.RetryAfterSeconds = 1
		}
		out.MatchedWindow = fmt.Sprintf("%s", w.Period)
	}
	return out, nil
}

// Reset clears rate-limit and lockout state for a key (operational/testing use).
func (e *Engine) Reset(ctx context.Context, req ResetRequest) error {
	rule, ok := e.registry.Get(req.RuleID)
	if !ok {
		return ErrRuleNotFound
	}
	namespace := rule.Namespace
	if req.Namespace != "" {
		namespace = req.Namespace
	}
	identity, err := buildIdentity(rule.KeyParts, req.Key)
	if err != nil {
		return err
	}
	return e.store.ResetAll(ctx, namespace, rule.ID, identity, rule)
}

func (e *Engine) Registry() *Registry {
	return e.registry
}
