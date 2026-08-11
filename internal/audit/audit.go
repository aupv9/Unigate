// Package audit records every CheckLimit decision as a structured log
// line (for the security team, FR9) and as Prometheus counters
// (NFR6). Only blocks/lockouts are logged at info level; allows are
// logged at debug level to keep steady-state log volume low.
package audit

import (
	"log/slog"
	"time"

	"github.com/aupv9/unigate/internal/metrics"
	"github.com/aupv9/unigate/internal/ruleengine"
)

type Recorder struct {
	log *slog.Logger
}

func NewRecorder(log *slog.Logger) *Recorder {
	if log == nil {
		log = slog.Default()
	}
	return &Recorder{log: log}
}

// Record implements ruleengine.AuditFunc.
func (r *Recorder) Record(evt ruleengine.AuditEvent) {
	result := "allow"
	if !evt.Allow {
		result = "block"
	}
	metrics.ChecksTotal.WithLabelValues(evt.Gateway, evt.RuleID, result).Inc()
	metrics.CheckDuration.WithLabelValues(evt.Gateway).Observe(evt.Duration.Seconds())

	if evt.FailedOpen {
		mode := "fail_closed"
		if evt.Allow {
			mode = "fail_open"
		}
		metrics.FailModeTotal.WithLabelValues(evt.RuleID, mode).Inc()
	}

	if !evt.Allow {
		reason := "rate_limit"
		if evt.LockedOut {
			reason = "lockout"
			metrics.LockoutsTotal.WithLabelValues(evt.Gateway, evt.RuleID).Inc()
		}
		metrics.BlocksTotal.WithLabelValues(evt.Gateway, evt.RuleID, reason).Inc()

		r.log.Info("rate_limit_block",
			"time", time.Now().UTC(),
			"gateway", evt.Gateway,
			"rule_id", evt.RuleID,
			"namespace", evt.Namespace,
			"identity", evt.Identity,
			"locked_out", evt.LockedOut,
			"failed_open", evt.FailedOpen,
			"reason", reason,
		)
		return
	}

	r.log.Debug("rate_limit_allow",
		"gateway", evt.Gateway,
		"rule_id", evt.RuleID,
		"namespace", evt.Namespace,
		"identity", evt.Identity,
	)
}
