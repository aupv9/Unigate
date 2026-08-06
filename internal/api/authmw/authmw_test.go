package authmw

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

func okHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
}

func TestHTTPMiddleware_DisabledWhenNoKeysConfigured(t *testing.T) {
	auth := New(nil)
	handler := auth.HTTPMiddleware(okHandler())

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected pass-through when auth disabled, got %d", rec.Code)
	}
}

func TestHTTPMiddleware_RejectsMissingOrWrongKey(t *testing.T) {
	auth := New(map[string]string{"kong": "secret123"})
	handler := auth.HTTPMiddleware(okHandler())

	cases := []struct {
		name    string
		gateway string
		key     string
	}{
		{"no headers", "", ""},
		{"wrong key", "kong", "wrong"},
		{"unknown gateway", "apisix", "secret123"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/", nil)
			if tc.gateway != "" {
				req.Header.Set(HTTPHeaderGateway, tc.gateway)
			}
			if tc.key != "" {
				req.Header.Set(HTTPHeaderAPIKey, tc.key)
			}
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req)
			if rec.Code != http.StatusUnauthorized {
				t.Fatalf("expected 401, got %d", rec.Code)
			}
		})
	}
}

func TestHTTPMiddleware_AllowsValidKey(t *testing.T) {
	auth := New(map[string]string{"kong": "secret123"})
	handler := auth.HTTPMiddleware(okHandler())

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set(HTTPHeaderGateway, "kong")
	req.Header.Set(HTTPHeaderAPIKey, "secret123")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 with valid key, got %d", rec.Code)
	}
}

func okUnaryHandler(ctx context.Context, req interface{}) (interface{}, error) {
	return "ok", nil
}

func TestUnaryServerInterceptor_DisabledWhenNoKeysConfigured(t *testing.T) {
	auth := New(nil)
	interceptor := auth.UnaryServerInterceptor()

	_, err := interceptor(context.Background(), nil, &grpc.UnaryServerInfo{}, okUnaryHandler)
	if err != nil {
		t.Fatalf("expected no error when auth disabled, got %v", err)
	}
}

func TestUnaryServerInterceptor_RejectsMissingMetadata(t *testing.T) {
	auth := New(map[string]string{"kong": "secret123"})
	interceptor := auth.UnaryServerInterceptor()

	_, err := interceptor(context.Background(), nil, &grpc.UnaryServerInfo{}, okUnaryHandler)
	if status.Code(err) != codes.Unauthenticated {
		t.Fatalf("expected Unauthenticated, got %v", err)
	}
}

func TestUnaryServerInterceptor_RejectsWrongKey(t *testing.T) {
	auth := New(map[string]string{"kong": "secret123"})
	interceptor := auth.UnaryServerInterceptor()

	md := metadata.Pairs(grpcMetaGateway, "kong", grpcMetaAPIKey, "wrong")
	ctx := metadata.NewIncomingContext(context.Background(), md)

	_, err := interceptor(ctx, nil, &grpc.UnaryServerInfo{}, okUnaryHandler)
	if status.Code(err) != codes.Unauthenticated {
		t.Fatalf("expected Unauthenticated, got %v", err)
	}
}

func TestUnaryServerInterceptor_AllowsValidKey(t *testing.T) {
	auth := New(map[string]string{"kong": "secret123"})
	interceptor := auth.UnaryServerInterceptor()

	md := metadata.Pairs(grpcMetaGateway, "kong", grpcMetaAPIKey, "secret123")
	ctx := metadata.NewIncomingContext(context.Background(), md)

	resp, err := interceptor(ctx, nil, &grpc.UnaryServerInfo{}, okUnaryHandler)
	if err != nil {
		t.Fatalf("expected no error with valid key, got %v", err)
	}
	if resp != "ok" {
		t.Fatalf("expected handler response to pass through, got %v", resp)
	}
}
