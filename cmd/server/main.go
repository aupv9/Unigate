// Command server runs the Universal Rate Limiting Service: the
// stateless "rate-limit brain" that Kong/APISIX/Apigee adapters call
// into via gRPC or HTTP to decide allow/deny (see docs/PRD.md).
package main

import (
	"context"
	"errors"
	"flag"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"google.golang.org/grpc"

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

	stopRefresh := startRuleRefreshLoop(registry, log, 5*time.Second)
	defer stopRefresh()

	grpcSrv := newGRPCServer(engine, registry, auth)
	httpSrv := newHTTPServer(engine, cfg.Server.HTTPAddr, auth)
	adminHTTPSrv := newAdminHTTPServer(registry, cfg.Server.AdminAddr, auth)
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

func newGRPCServer(engine *ruleengine.Engine, registry *ruleengine.Registry, auth *authmw.Authenticator) *grpc.Server {
	srv := grpc.NewServer(grpc.UnaryInterceptor(auth.UnaryServerInterceptor()))
	ratelimitv1.RegisterRateLimitServiceServer(srv, grpcserver.New(engine))
	ratelimitv1.RegisterAdminServiceServer(srv, adminserver.NewGRPCServer(registry))
	return srv
}

func newHTTPServer(engine *ruleengine.Engine, addr string, auth *authmw.Authenticator) *http.Server {
	handler := httpserver.New(engine)
	return &http.Server{
		Addr:         addr,
		Handler:      auth.HTTPMiddleware(handler),
		ReadTimeout:  5 * time.Second,
		WriteTimeout: 5 * time.Second,
	}
}

func newAdminHTTPServer(registry *ruleengine.Registry, addr string, auth *authmw.Authenticator) *http.Server {
	handler := adminserver.NewHTTPServer(registry)
	return &http.Server{
		Addr:         addr,
		Handler:      auth.HTTPMiddleware(handler),
		ReadTimeout:  5 * time.Second,
		WriteTimeout: 5 * time.Second,
	}
}

func newMetricsServer(addr string) *http.Server {
	mux := http.NewServeMux()
	mux.Handle("/metrics", metrics.Handler())
	return &http.Server{Addr: addr, Handler: mux}
}

func runOrIgnoreClose(srv *http.Server) error {
	err := srv.ListenAndServe()
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
