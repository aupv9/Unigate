package ruleengine

import (
	"context"
	"sync"
	"testing"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
)

// OTel Go's global TracerProvider delegation only upgrades once: a
// package-level `var tracer = otel.Tracer(...)` (as engine.go and
// store use) binds permanently to whichever provider was installed on
// the FIRST otel.SetTracerProvider call in the process. A second
// SetTracerProvider call in a later test does NOT retroactively
// re-bind that already-vended tracer - so tests can't each install
// their own provider. Install exactly once for the whole package's
// test run, and Reset() the shared in-memory exporter between tests.
var (
	testExporter      *tracetest.InMemoryExporter
	installTracerOnce sync.Once
)

func installTestTracerProvider(t *testing.T) *tracetest.InMemoryExporter {
	t.Helper()
	installTracerOnce.Do(func() {
		testExporter = tracetest.NewInMemoryExporter()
		tp := sdktrace.NewTracerProvider(sdktrace.WithSyncer(testExporter))
		otel.SetTracerProvider(tp)
	})
	testExporter.Reset()
	return testExporter
}

func findSpan(spans tracetest.SpanStubs, name string) *tracetest.SpanStub {
	for i := range spans {
		if spans[i].Name == name {
			return &spans[i]
		}
	}
	return nil
}

func spanNames(spans tracetest.SpanStubs) []string {
	names := make([]string, len(spans))
	for i, s := range spans {
		names[i] = s.Name
	}
	return names
}

func attrMap(kvs []attribute.KeyValue) map[string]interface{} {
	m := make(map[string]interface{}, len(kvs))
	for _, kv := range kvs {
		m[string(kv.Key)] = kv.Value.AsInterface()
	}
	return m
}

func TestCheckLimit_ProducesASpanWithExpectedAttributes(t *testing.T) {
	exporter := installTestTracerProvider(t)
	engine, setClock := newTestEngine(t, loginRule())
	setClock(time.Unix(1_700_000_000, 0))

	key := []KeyComponent{{Kind: "ip", Value: "1.2.3.4"}, {Kind: "username", Value: "alice"}}
	res, err := engine.CheckLimit(context.Background(), CheckRequest{RuleID: "login-brute-force", Key: key, Gateway: "kong"})
	if err != nil {
		t.Fatalf("CheckLimit: %v", err)
	}
	if !res.Allow {
		t.Fatalf("expected allow on first request")
	}

	spans := exporter.GetSpans()
	checkLimitSpan := findSpan(spans, "CheckLimit")
	if checkLimitSpan == nil {
		t.Fatalf("expected a span named CheckLimit, got spans: %v", spanNames(spans))
	}

	attrs := attrMap(checkLimitSpan.Attributes)
	if attrs["unigate.rule_id"] != "login-brute-force" {
		t.Errorf("expected rule_id attribute, got %v", attrs["unigate.rule_id"])
	}
	if attrs["unigate.gateway"] != "kong" {
		t.Errorf("expected gateway attribute, got %v", attrs["unigate.gateway"])
	}
	if attrs["unigate.allow"] != true {
		t.Errorf("expected allow=true attribute, got %v", attrs["unigate.allow"])
	}

	// The store's sliding-window Redis call should show up as a CHILD
	// span of CheckLimit, proving the trace context (not just the
	// span itself) actually propagates from ruleengine into store.
	childSpan := findSpan(spans, "redis.sliding_window")
	if childSpan == nil {
		t.Fatalf("expected a child span named redis.sliding_window, got spans: %v", spanNames(spans))
	}
	if childSpan.Parent.SpanID() != checkLimitSpan.SpanContext.SpanID() {
		t.Errorf("expected redis.sliding_window to be a child of CheckLimit's span")
	}
}

func TestCheckLimit_RecordsErrorOnUnknownRule(t *testing.T) {
	exporter := installTestTracerProvider(t)
	engine, _ := newTestEngine(t, loginRule())

	_, err := engine.CheckLimit(context.Background(), CheckRequest{RuleID: "does-not-exist"})
	if err != ErrRuleNotFound {
		t.Fatalf("expected ErrRuleNotFound, got %v", err)
	}

	spans := exporter.GetSpans()
	if len(spans) != 1 || spans[0].Name != "CheckLimit" {
		t.Fatalf("expected exactly one CheckLimit span, got %v", spanNames(spans))
	}
	if spans[0].Status.Code != codes.Error {
		t.Errorf("expected span status Error, got %v", spans[0].Status.Code)
	}
}
