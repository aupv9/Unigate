// Package metrics exposes Prometheus-compatible counters/histograms for
// the rate-limit service (NFR6).
package metrics

import (
	"net/http"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

var (
	ChecksTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Namespace: "unigate",
		Name:      "checks_total",
		Help:      "Total CheckLimit decisions, labeled by gateway/rule/result.",
	}, []string{"gateway", "rule_id", "result"}) // result: allow|block

	BlocksTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Namespace: "unigate",
		Name:      "blocks_total",
		Help:      "Total blocked requests, labeled by gateway/rule/reason.",
	}, []string{"gateway", "rule_id", "reason"}) // reason: rate_limit|lockout

	LockoutsTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Namespace: "unigate",
		Name:      "lockouts_total",
		Help:      "Total times a key entered an escalated lockout state.",
	}, []string{"gateway", "rule_id"})

	FailModeTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Namespace: "unigate",
		Name:      "fail_mode_total",
		Help:      "Total times the backing store errored and a rule's fail-open/fail-closed mode was applied.",
	}, []string{"rule_id", "mode"}) // mode: fail_open|fail_closed

	CheckDuration = promauto.NewHistogramVec(prometheus.HistogramOpts{
		Namespace: "unigate",
		Name:      "check_duration_seconds",
		Help:      "CheckLimit end-to-end latency as observed by the service (NFR1).",
		Buckets:   []float64{.0005, .001, .0025, .005, .01, .025, .05, .1, .25, .5},
	}, []string{"gateway"})
)

// Handler returns the standard Prometheus scrape handler.
func Handler() http.Handler {
	return promhttp.Handler()
}
