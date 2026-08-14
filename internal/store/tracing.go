package store

import "go.opentelemetry.io/otel"

// tracer follows the standard OTel Go library-instrumentation
// pattern: calling otel.Tracer(...) is always safe and a correctly
// behaving no-op until something has installed a TracerProvider
// globally (internal/tracing.Init, or a test's own provider).
var tracer = otel.Tracer("github.com/aupv9/unigate/internal/store")
