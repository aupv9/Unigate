// Command server runs the Universal Rate Limiting Service: the
// stateless "rate-limit brain" that Kong/APISIX/Apigee adapters call
// into via gRPC or HTTP to decide allow/deny (see docs/PRD.md).
package main

import (
	"context"
	"crypto/tls"
	"errors"
	"flag"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"go.opentelemetry.io/contrib/instrumentation/google.golang.org/grpc/otelgrpc"
	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"

	ratelimitv1 "github.com/aupv9/unigate/gen/go/ratelimit/v1"
	"github.com/aupv9/unigate/internal/api/adminserver"
	"github.com/aupv9/unigate/internal/api/authmw"
	"github.com/aupv9/unigate/internal/api/grpcserver"
	"github.com/aupv9/unigate/internal/api/httpserver"
	"github.com/aupv9/unigate/internal/audit"
	"github.com/aupv9/unigate/internal/config"
	"github.com/aupv9/unigate/internal/metrics"
	"github.com/aupv9/unigate/internal/ruleengine"
	"github.com/aupv9/unigate/internal/store"
	"github.com/aupv9/unigate/internal/tlsutil"
	"github.com/aupv9/unigate/internal/tracing"
)

func main() {
	configPath := flag.String("config", "deploy/config/config.yaml", "path to server config YAML")
	flag.Parse()

	log := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	slog.SetDefault(log)

	cfg, err := config.Load(*configPath)
	if err != nil {
		log.Error("load config", "err", err)
		os.Exit(1)
	}

	tracingShutdown, err := tracing.Init(context.Background(), cfg.Tracing)
	if err != nil {
		log.Error("init tracing", "err", err)
		os.Exit(1)
	}
	defer func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := tracingShutdown(shutdownCtx); err != nil {
			log.Warn("tracing shutdown", "err", err)
		}
	}()

	redisStore, err := store.New(cfg.Redis)
	if err != nil {
		log.Error("init redis store", "err", err)
		os.Exit(1)
	}
	defer redisStore.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	if err := redisStore.Ping(ctx); err != nil {
		log.Warn("redis not reachable at startup, will retry per-request per fail-mode", "err", err)
	}
	cancel()

	registry := ruleengine.NewRegistry(cfg.Rules, redisStore.Client())
	recorder := audit.NewRecorder(log)
	engine := ruleengine.New(registry, redisStore, log, recorder.Record)

	auth := authmw.New(cfg.Auth.APIKeys)

	tlsConfig, err := tlsutil.Build(cfg.Server.TLS)
	if err != nil {
		log.Error("build tls config", "err", err)
		os.Exit(1)
	}

	stopRefresh := startRuleRefreshLoop(registry, log, 5*time.Second)
	defer stopRefresh()

	grpcSrv := newGRPCServer(engine, registry, auth, tlsConfig)
	httpSrv := newHTTPServer(engine, cfg.Server.HTTPAddr, auth, tlsConfig)
	adminHTTPSrv := newAdminHTTPServer(registry, cfg.Server.AdminAddr, auth, tlsConfig)
	metricsSrv := newMetricsServer(cfg.Server.MetricsAddr)

	grpcLis, err := net.Listen("tcp", cfg.Server.GRPCAddr)
	if err != nil {
		log.Error("listen grpc", "addr", cfg.Server.GRPCAddr, "err", err)
		os.Exit(1)
	}

	errCh := make(chan error, 4)
	go func() { errCh <- grpcSrv.Serve(grpcLis) }()
	go func() { errCh <- runOrIgnoreClose(httpSrv) }()
	go func() { errCh <- runOrIgnoreClose(adminHTTPSrv) }()
	go func() { errCh <- runOrIgnoreClose(metricsSrv) }()

	log.Info("unigate rate-limit service started",
		"grpc_addr", cfg.Server.GRPCAddr,
		"http_addr", cfg.Server.HTTPAddr,
		"admin_addr", cfg.Server.AdminAddr,
		"metrics_addr", cfg.Server.MetricsAddr,
		"rules", len(cfg.Rules),
		"tls_enabled", tlsConfig != nil,
	)

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

	select {
	case sig := <-sigCh:
		log.Info("shutting down", "signal", sig.String())
	case err := <-errCh:
		log.Error("server exited unexpectedly", "err", err)
	}

	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer shutdownCancel()

	grpcSrv.GracefulStop()
	_ = httpSrv.Shutdown(shutdownCtx)
	_ = adminHTTPSrv.Shutdown(shutdownCtx)
	_ = metricsSrv.Shutdown(shutdownCtx)
}

func newGRPCServer(engine *ruleengine.Engine, registry *ruleengine.Registry, auth *authmw.Authenticator, tlsConfig *tls.Config) *grpc.Server {
	opts := []grpc.ServerOption{
		grpc.UnaryInterceptor(auth.UnaryServerInterceptor()),
		grpc.StatsHandler(otelgrpc.NewServerHandler()),
	}
	if tlsConfig != nil {
		opts = append(opts, grpc.Creds(credentials.NewTLS(tlsConfig)))
	}
	srv := grpc.NewServer(opts...)
	ratelimitv1.RegisterRateLimitServiceServer(srv, grpcserver.New(engine))
	ratelimitv1.RegisterAdminServiceServer(srv, adminserver.NewGRPCServer(registry))
	return srv
}

func newHTTPServer(engine *ruleengine.Engine, addr string, auth *authmw.Authenticator, tlsConfig *tls.Config) *http.Server {
	handler := otelhttp.NewHandler(auth.HTTPMiddleware(httpserver.New(engine)), "unigate.check_http")
	return &http.Server{
		Addr:         addr,
		Handler:      handler,
		ReadTimeout:  5 * time.Second,
		WriteTimeout: 5 * time.Second,
		TLSConfig:    tlsConfig,
	}
}

func newAdminHTTPServer(registry *ruleengine.Registry, addr string, auth *authmw.Authenticator, tlsConfig *tls.Config) *http.Server {
	handler := otelhttp.NewHandler(auth.HTTPMiddleware(adminserver.NewHTTPServer(registry)), "unigate.admin_http")
	return &http.Server{
		Addr:         addr,
		Handler:      handler,
		ReadTimeout:  5 * time.Second,
		WriteTimeout: 5 * time.Second,
		TLSConfig:    tlsConfig,
	}
}

func newMetricsServer(addr string) *http.Server {
	mux := http.NewServeMux()
	mux.Handle("/metrics", metrics.Handler())
	return &http.Server{Addr: addr, Handler: mux}
}

// runOrIgnoreClose serves srv, using TLS when srv.TLSConfig has been
// set (certificates already loaded into it, so both file arguments to
// ListenAndServeTLS are empty - see net/http's docs on that).
func runOrIgnoreClose(srv *http.Server) error {
	var err error
	if srv.TLSConfig != nil {
		err = srv.ListenAndServeTLS("", "")
	} else {
		err = srv.ListenAndServe()
	}
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}

// startRuleRefreshLoop periodically pulls the Admin-API-managed rule
// set from Redis so every stateless instance converges on the same
// rules (NFR3, FR8). Returns a stop function.
func startRuleRefreshLoop(registry *ruleengine.Registry, log *slog.Logger, interval time.Duration) func() {
	ticker := time.NewTicker(interval)
	done := make(chan struct{})
	go func() {
		for {
			select {
			case <-ticker.C:
				ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
				if err := registry.Refresh(ctx); err != nil {
					log.Warn("rule registry refresh failed", "err", err)
				}
				cancel()
			case <-done:
				return
			}
		}
	}()
	return func() {
		ticker.Stop()
		close(done)
	}
}
