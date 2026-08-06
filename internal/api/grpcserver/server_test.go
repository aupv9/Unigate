package grpcserver

import (
	"context"
	"net"
	"os/exec"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"

	ratelimitv1 "github.com/aupv9/unigate/gen/go/ratelimit/v1"
	"github.com/aupv9/unigate/internal/config"
	"github.com/aupv9/unigate/internal/ruleengine"
	"github.com/aupv9/unigate/internal/store"
)

func startTestEngine(t *testing.T) *ruleengine.Engine {
	t.Helper()

	if _, err := exec.LookPath("redis-server"); err != nil {
		t.Skip("redis-server not found in PATH, skipping grpcserver integration tests")
	}

	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("find free port: %v", err)
	}
	addr := lis.Addr().String()
	lis.Close()

	cmd := exec.Command("redis-server", "--port", addr[len("127.0.0.1:"):], "--bind", "127.0.0.1", "--save", "", "--appendonly", "no")
	if err := cmd.Start(); err != nil {
		t.Fatalf("start redis-server: %v", err)
	}
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	})

	s, err := store.New(config.RedisConfig{
		Addrs:        []string{addr},
		DialTimeout:  config.Duration(2 * time.Second),
		ReadTimeout:  config.Duration(2 * time.Second),
		WriteTimeout: config.Duration(2 * time.Second),
	})
	if err != nil {
		t.Fatalf("new store: %v", err)
	}

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
		err := s.Ping(ctx)
		cancel()
		if err == nil {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}

	rule := config.RuleConfig{
		ID:        "test-rule",
		Algorithm: config.AlgorithmSlidingWindow,
		KeyParts:  []string{"ip"},
		Namespace: "default",
		FailMode:  config.FailClosed,
		Windows: []config.WindowConfig{
			{Limit: 2, Period: config.Duration(time.Minute)},
		},
	}
	registry := ruleengine.NewRegistry([]config.RuleConfig{rule}, s.Client())
	return ruleengine.New(registry, s, nil, nil)
}

// startTestClient wires up an in-memory gRPC server+client pair via
// bufconn, avoiding any real network listener.
func startTestClient(t *testing.T, engine *ruleengine.Engine) ratelimitv1.RateLimitServiceClient {
	t.Helper()

	lis := bufconn.Listen(1024 * 1024)
	srv := grpc.NewServer()
	ratelimitv1.RegisterRateLimitServiceServer(srv, New(engine))
	go func() { _ = srv.Serve(lis) }()
	t.Cleanup(srv.Stop)

	conn, err := grpc.NewClient("passthrough:///bufconn",
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) {
			return lis.DialContext(ctx)
		}),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		t.Fatalf("dial bufconn: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })

	return ratelimitv1.NewRateLimitServiceClient(conn)
}

func TestCheckLimit_AllowsThenBlocks(t *testing.T) {
	engine := startTestEngine(t)
	client := startTestClient(t, engine)
	ctx := context.Background()

	key := []*ratelimitv1.KeyComponent{{Kind: "ip", Value: "1.2.3.4"}}

	for i := 0; i < 2; i++ {
		res, err := client.CheckLimit(ctx, &ratelimitv1.CheckLimitRequest{RuleId: "test-rule", Key: key, Gateway: "kong"})
		if err != nil {
			t.Fatalf("request %d: %v", i, err)
		}
		if !res.Allow {
			t.Fatalf("request %d expected allow, got blocked", i)
		}
	}

	res, err := client.CheckLimit(ctx, &ratelimitv1.CheckLimitRequest{RuleId: "test-rule", Key: key, Gateway: "kong"})
	if err != nil {
		t.Fatalf("3rd request: %v", err)
	}
	if res.Allow {
		t.Fatalf("3rd request expected blocked")
	}
	if res.RetryAfterSeconds <= 0 {
		t.Errorf("expected positive retry_after_seconds, got %d", res.RetryAfterSeconds)
	}
}

func TestCheckLimit_MissingRuleIDReturnsInvalidArgument(t *testing.T) {
	engine := startTestEngine(t)
	client := startTestClient(t, engine)

	_, err := client.CheckLimit(context.Background(), &ratelimitv1.CheckLimitRequest{
		Key: []*ratelimitv1.KeyComponent{{Kind: "ip", Value: "1.2.3.4"}},
	})
	if status.Code(err) != codes.InvalidArgument {
		t.Fatalf("expected InvalidArgument, got %v", err)
	}
}

func TestCheckLimit_UnknownRuleReturnsNotFound(t *testing.T) {
	engine := startTestEngine(t)
	client := startTestClient(t, engine)

	_, err := client.CheckLimit(context.Background(), &ratelimitv1.CheckLimitRequest{
		RuleId: "does-not-exist",
		Key:    []*ratelimitv1.KeyComponent{{Kind: "ip", Value: "1.2.3.4"}},
	})
	if status.Code(err) != codes.NotFound {
		t.Fatalf("expected NotFound, got %v", err)
	}
}

func TestCheckLimit_MissingKeyPartReturnsInvalidArgument(t *testing.T) {
	engine := startTestEngine(t)
	client := startTestClient(t, engine)

	_, err := client.CheckLimit(context.Background(), &ratelimitv1.CheckLimitRequest{
		RuleId: "test-rule",
		Key:    []*ratelimitv1.KeyComponent{{Kind: "username", Value: "alice"}}, // rule needs "ip"
	})
	if status.Code(err) != codes.InvalidArgument {
		t.Fatalf("expected InvalidArgument, got %v", err)
	}
}

func TestReset_ClearsCounters(t *testing.T) {
	engine := startTestEngine(t)
	client := startTestClient(t, engine)
	ctx := context.Background()
	key := []*ratelimitv1.KeyComponent{{Kind: "ip", Value: "9.9.9.9"}}

	for i := 0; i < 2; i++ {
		if _, err := client.CheckLimit(ctx, &ratelimitv1.CheckLimitRequest{RuleId: "test-rule", Key: key}); err != nil {
			t.Fatalf("request %d: %v", i, err)
		}
	}
	blocked, err := client.CheckLimit(ctx, &ratelimitv1.CheckLimitRequest{RuleId: "test-rule", Key: key})
	if err != nil || blocked.Allow {
		t.Fatalf("expected blocked before reset: allow=%v err=%v", blocked.GetAllow(), err)
	}

	resetRes, err := client.Reset(ctx, &ratelimitv1.ResetRequest{RuleId: "test-rule", Key: key})
	if err != nil || !resetRes.Ok {
		t.Fatalf("reset failed: ok=%v err=%v", resetRes.GetOk(), err)
	}

	allowed, err := client.CheckLimit(ctx, &ratelimitv1.CheckLimitRequest{RuleId: "test-rule", Key: key})
	if err != nil || !allowed.Allow {
		t.Fatalf("expected allowed after reset: allow=%v err=%v", allowed.GetAllow(), err)
	}
}
