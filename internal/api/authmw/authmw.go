// Package authmw provides simple per-gateway API-key authentication for
// adapter <-> service traffic (NFR5). It is deliberately minimal: v1
// expects mTLS to be terminated by the surrounding mesh/sidecar, and
// this API key is a second, application-level factor identifying which
// gateway is calling.
package authmw

import (
	"context"
	"net/http"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

const (
	HTTPHeaderAPIKey  = "X-Unigate-Api-Key"
	HTTPHeaderGateway = "X-Unigate-Gateway"
	grpcMetaAPIKey    = "x-unigate-api-key"
	grpcMetaGateway   = "x-unigate-gateway"
)

// Authenticator validates a gateway name + API key against the
// configured per-gateway keys (config.AuthConfig.APIKeys).
type Authenticator struct {
	keys map[string]string // gateway -> key
}

func New(keys map[string]string) *Authenticator {
	return &Authenticator{keys: keys}
}

// Enabled reports whether any API keys are configured. When disabled,
// middleware passes every request through unchanged, which is useful
// for local development.
func (a *Authenticator) Enabled() bool {
	return len(a.keys) > 0
}

func (a *Authenticator) valid(gateway, key string) bool {
	want, ok := a.keys[gateway]
	return ok && want != "" && want == key
}

// HTTPMiddleware enforces the API key on every request when auth is enabled.
func (a *Authenticator) HTTPMiddleware(next http.Handler) http.Handler {
	if !a.Enabled() {
		return next
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gateway := r.Header.Get(HTTPHeaderGateway)
		key := r.Header.Get(HTTPHeaderAPIKey)
		if !a.valid(gateway, key) {
			http.Error(w, `{"error":"unauthorized"}`, http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// UnaryServerInterceptor enforces the API key on every unary gRPC call
// when auth is enabled.
func (a *Authenticator) UnaryServerInterceptor() grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req interface{}, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (interface{}, error) {
		if !a.Enabled() {
			return handler(ctx, req)
		}
		md, ok := metadata.FromIncomingContext(ctx)
		if !ok {
			return nil, status.Error(codes.Unauthenticated, "missing metadata")
		}
		gateway := firstOrEmpty(md.Get(grpcMetaGateway))
		key := firstOrEmpty(md.Get(grpcMetaAPIKey))
		if !a.valid(gateway, key) {
			return nil, status.Error(codes.Unauthenticated, "invalid gateway credentials")
		}
		return handler(ctx, req)
	}
}

func firstOrEmpty(vals []string) string {
	if len(vals) == 0 {
		return ""
	}
	return vals[0]
}
