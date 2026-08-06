package adminserver

import (
	"context"
	"net"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"

	ratelimitv1 "github.com/aupv9/unigate/gen/go/ratelimit/v1"
	"github.com/aupv9/unigate/internal/config"
	"github.com/aupv9/unigate/internal/ruleengine"
	"google.golang.org/grpc/test/bufconn"
)

func startTestAdminClient(t *testing.T, registry *ruleengine.Registry) ratelimitv1.AdminServiceClient {
	t.Helper()

	lis := bufconn.Listen(1024 * 1024)
	srv := grpc.NewServer()
	ratelimitv1.RegisterAdminServiceServer(srv, NewGRPCServer(registry))
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

	return ratelimitv1.NewAdminServiceClient(conn)
}

func TestGRPC_CreateGetListDeleteRule(t *testing.T) {
	registry := ruleengine.NewRegistry(nil, nil)
	client := startTestAdminClient(t, registry)
	ctx := context.Background()

	protoRule := &ratelimitv1.Rule{
		Id:        "grpc-rule",
		Algorithm: ratelimitv1.Algorithm_ALGORITHM_GCRA,
		KeyParts:  []string{"ip", "username"},
		Namespace: "prod",
		FailMode:  ratelimitv1.FailMode_FAIL_OPEN,
		Burst:     10,
		Windows:   []*ratelimitv1.Window{{Limit: 100, PeriodSeconds: 60}},
		Lockout: &ratelimitv1.LockoutPolicy{
			Enabled:             true,
			ViolationTtlSeconds: 3600,
			Steps: []*ratelimitv1.LockoutStep{
				{AfterViolations: 1, LockoutSeconds: 60},
				{AfterViolations: 3, LockoutSeconds: 300},
			},
		},
	}

	created, err := client.CreateRule(ctx, &ratelimitv1.CreateRuleRequest{Rule: protoRule})
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	// Round-trip fidelity: everything the proto Rule carries should
	// survive proto -> config.RuleConfig -> proto unchanged.
	if created.GetAlgorithm() != ratelimitv1.Algorithm_ALGORITHM_GCRA {
		t.Errorf("algorithm not preserved: %v", created.GetAlgorithm())
	}
	if created.GetFailMode() != ratelimitv1.FailMode_FAIL_OPEN {
		t.Errorf("fail_mode not preserved: %v", created.GetFailMode())
	}
	if len(created.GetKeyParts()) != 2 || created.GetKeyParts()[0] != "ip" || created.GetKeyParts()[1] != "username" {
		t.Errorf("key_parts not preserved: %v", created.GetKeyParts())
	}
	if len(created.GetWindows()) != 1 || created.GetWindows()[0].GetLimit() != 100 || created.GetWindows()[0].GetPeriodSeconds() != 60 {
		t.Errorf("windows not preserved: %v", created.GetWindows())
	}
	if !created.GetLockout().GetEnabled() || len(created.GetLockout().GetSteps()) != 2 {
		t.Errorf("lockout not preserved: %+v", created.GetLockout())
	}
	if created.GetLockout().GetViolationTtlSeconds() != 3600 {
		t.Errorf("violation ttl not preserved: %v", created.GetLockout().GetViolationTtlSeconds())
	}

	// Also verify it landed correctly in the underlying config.RuleConfig.
	stored, ok := registry.Get("grpc-rule")
	if !ok {
		t.Fatalf("rule not found in registry after create")
	}
	if stored.Lockout.Steps[1].Lockout.Duration() != 5*time.Minute {
		t.Errorf("expected 2nd lockout step = 5m, got %v", stored.Lockout.Steps[1].Lockout.Duration())
	}

	// Get
	got, err := client.GetRule(ctx, &ratelimitv1.GetRuleRequest{Id: "grpc-rule"})
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.GetId() != "grpc-rule" {
		t.Fatalf("unexpected rule: %+v", got)
	}

	// List
	list, err := client.ListRules(ctx, &ratelimitv1.ListRulesRequest{})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(list.GetRules()) != 1 {
		t.Fatalf("expected 1 rule, got %d", len(list.GetRules()))
	}

	// Delete
	delRes, err := client.DeleteRule(ctx, &ratelimitv1.DeleteRuleRequest{Id: "grpc-rule"})
	if err != nil || !delRes.GetOk() {
		t.Fatalf("delete failed: ok=%v err=%v", delRes.GetOk(), err)
	}

	// Get after delete -> NotFound
	_, err = client.GetRule(ctx, &ratelimitv1.GetRuleRequest{Id: "grpc-rule"})
	if status.Code(err) != codes.NotFound {
		t.Fatalf("expected NotFound after delete, got %v", err)
	}
}

func TestGRPC_CreateDuplicateReturnsAlreadyExists(t *testing.T) {
	registry := ruleengine.NewRegistry([]config.RuleConfig{{
		ID: "dup", KeyParts: []string{"ip"},
		Windows: []config.WindowConfig{{Limit: 1, Period: config.Duration(time.Minute)}},
	}}, nil)
	client := startTestAdminClient(t, registry)

	_, err := client.CreateRule(context.Background(), &ratelimitv1.CreateRuleRequest{
		Rule: &ratelimitv1.Rule{Id: "dup", KeyParts: []string{"ip"}, Windows: []*ratelimitv1.Window{{Limit: 1, PeriodSeconds: 60}}},
	})
	if status.Code(err) != codes.AlreadyExists {
		t.Fatalf("expected AlreadyExists, got %v", err)
	}
}

func TestGRPC_UpdateNonExistentReturnsNotFound(t *testing.T) {
	registry := ruleengine.NewRegistry(nil, nil)
	client := startTestAdminClient(t, registry)

	_, err := client.UpdateRule(context.Background(), &ratelimitv1.UpdateRuleRequest{
		Rule: &ratelimitv1.Rule{Id: "missing", KeyParts: []string{"ip"}, Windows: []*ratelimitv1.Window{{Limit: 1, PeriodSeconds: 60}}},
	})
	if status.Code(err) != codes.NotFound {
		t.Fatalf("expected NotFound, got %v", err)
	}
}
