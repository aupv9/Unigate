// Package adminserver exposes rule CRUD (FR8) so the security team can
// change thresholds without redeploying any gateway.
package adminserver

import (
	"time"

	ratelimitv1 "github.com/aupv9/unigate/gen/go/ratelimit/v1"
	"github.com/aupv9/unigate/internal/config"
)

func toProtoRule(r config.RuleConfig) *ratelimitv1.Rule {
	windows := make([]*ratelimitv1.Window, len(r.Windows))
	for i, w := range r.Windows {
		windows[i] = &ratelimitv1.Window{Limit: w.Limit, PeriodSeconds: int64(w.Period.Duration() / time.Second)}
	}
	steps := make([]*ratelimitv1.LockoutStep, len(r.Lockout.Steps))
	for i, s := range r.Lockout.Steps {
		steps[i] = &ratelimitv1.LockoutStep{
			AfterViolations: int32(s.AfterViolations),
			LockoutSeconds:  int64(s.Lockout.Duration() / time.Second),
		}
	}
	algo := ratelimitv1.Algorithm_ALGORITHM_SLIDING_WINDOW
	if r.Algorithm == config.AlgorithmGCRA {
		algo = ratelimitv1.Algorithm_ALGORITHM_GCRA
	}
	failMode := ratelimitv1.FailMode_FAIL_CLOSED
	if r.FailMode == config.FailOpen {
		failMode = ratelimitv1.FailMode_FAIL_OPEN
	}
	return &ratelimitv1.Rule{
		Id:          r.ID,
		Description: r.Description,
		Algorithm:   algo,
		Windows:     windows,
		Burst:       r.Burst,
		Namespace:   r.Namespace,
		FailMode:    failMode,
		KeyParts:    append([]string(nil), r.KeyParts...),
		Lockout: &ratelimitv1.LockoutPolicy{
			Enabled:             r.Lockout.Enabled,
			Steps:               steps,
			ViolationTtlSeconds: int64(r.Lockout.ViolationTTL.Duration() / time.Second),
		},
	}
}

func fromProtoRule(r *ratelimitv1.Rule) config.RuleConfig {
	windows := make([]config.WindowConfig, len(r.GetWindows()))
	for i, w := range r.GetWindows() {
		windows[i] = config.WindowConfig{Limit: w.GetLimit(), Period: config.Duration(time.Duration(w.GetPeriodSeconds()) * time.Second)}
	}
	steps := make([]config.LockoutStepConfig, len(r.GetLockout().GetSteps()))
	for i, s := range r.GetLockout().GetSteps() {
		steps[i] = config.LockoutStepConfig{
			AfterViolations: int(s.GetAfterViolations()),
			Lockout:         config.Duration(time.Duration(s.GetLockoutSeconds()) * time.Second),
		}
	}
	algo := config.AlgorithmSlidingWindow
	if r.GetAlgorithm() == ratelimitv1.Algorithm_ALGORITHM_GCRA {
		algo = config.AlgorithmGCRA
	}
	failMode := config.FailClosed
	if r.GetFailMode() == ratelimitv1.FailMode_FAIL_OPEN {
		failMode = config.FailOpen
	}
	return config.RuleConfig{
		ID:          r.GetId(),
		Description: r.GetDescription(),
		Algorithm:   algo,
		KeyParts:    append([]string(nil), r.GetKeyParts()...),
		Windows:     windows,
		Burst:       r.GetBurst(),
		FailMode:    failMode,
		Namespace:   r.GetNamespace(),
		Lockout: config.LockoutConfig{
			Enabled:      r.GetLockout().GetEnabled(),
			Steps:        steps,
			ViolationTTL: config.Duration(time.Duration(r.GetLockout().GetViolationTtlSeconds()) * time.Second),
		},
	}
}
