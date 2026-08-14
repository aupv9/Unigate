// Package tracing wires up OpenTelemetry distributed tracing across
// the CheckLimit path: gRPC/HTTP request -> rule engine evaluation ->
// Redis Lua calls, exported via OTLP/HTTP.
//
// Instrumentation (ruleengine, store, the gRPC/HTTP servers) always
// calls otel.Tracer(...) - the standard library-instrumentation
// pattern - so it's a correctly-behaving no-op whenever Init hasn't
// been called (tracing disabled) or in tests that don't set up a
// TracerProvider.
package tracing

import (
	"context"
	"fmt"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"

	"github.com/aupv9/unigate/internal/config"
)

// Init sets up the global TracerProvider from cfg. When cfg.Enabled
// is false it does nothing (leaving OTel's default no-op provider in
// place) and returns a no-op shutdown func. Callers should always
// `defer shutdown(ctx)` regardless of whether tracing is enabled.
func Init(ctx context.Context, cfg config.TracingConfig) (shutdown func(context.Context) error, err error) {
	noop := func(context.Context) error { return nil }
	if !cfg.Enabled {
		return noop, nil
	}

	exporter, err := otlptracehttp.New(ctx,
		otlptracehttp.WithEndpoint(cfg.OTLPEndpoint),
		otlptracehttp.WithInsecure(),
	)
	if err != nil {
		return noop, fmt.Errorf("tracing: create OTLP exporter: %w", err)
	}

	res := resource.NewWithAttributes("",
		attribute.String("service.name", cfg.ServiceName),
	)

	tp := sdktrace.NewTracerProvider(
		sdktrace.WithBatcher(exporter),
		sdktrace.WithResource(res),
		sdktrace.WithSampler(sdktrace.ParentBased(sdktrace.TraceIDRatioBased(cfg.SampleRatio))),
	)

	otel.SetTracerProvider(tp)
	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(
		propagation.TraceContext{}, propagation.Baggage{},
	))

	return tp.Shutdown, nil
}
