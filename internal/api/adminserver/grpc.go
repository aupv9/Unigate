package adminserver

import (
	"context"
	"errors"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	ratelimitv1 "github.com/aupv9/unigate/gen/go/ratelimit/v1"
	"github.com/aupv9/unigate/internal/ruleengine"
)

type GRPCServer struct {
	ratelimitv1.UnimplementedAdminServiceServer

	registry *ruleengine.Registry
}

func NewGRPCServer(registry *ruleengine.Registry) *GRPCServer {
	return &GRPCServer{registry: registry}
}

func (s *GRPCServer) ListRules(ctx context.Context, req *ratelimitv1.ListRulesRequest) (*ratelimitv1.ListRulesResponse, error) {
	rules := s.registry.List(req.GetNamespace())
	out := make([]*ratelimitv1.Rule, len(rules))
	for i, r := range rules {
		out[i] = toProtoRule(r)
	}
	return &ratelimitv1.ListRulesResponse{Rules: out}, nil
}

func (s *GRPCServer) GetRule(ctx context.Context, req *ratelimitv1.GetRuleRequest) (*ratelimitv1.Rule, error) {
	rule, ok := s.registry.Get(req.GetId())
	if !ok {
		return nil, status.Error(codes.NotFound, ruleengine.ErrRuleNotFound.Error())
	}
	return toProtoRule(rule), nil
}

func (s *GRPCServer) CreateRule(ctx context.Context, req *ratelimitv1.CreateRuleRequest) (*ratelimitv1.Rule, error) {
	rule := fromProtoRule(req.GetRule())
	if err := s.registry.Create(ctx, rule); err != nil {
		return nil, toGRPCError(err)
	}
	return toProtoRule(rule), nil
}

func (s *GRPCServer) UpdateRule(ctx context.Context, req *ratelimitv1.UpdateRuleRequest) (*ratelimitv1.Rule, error) {
	rule := fromProtoRule(req.GetRule())
	if err := s.registry.Update(ctx, rule); err != nil {
		return nil, toGRPCError(err)
	}
	return toProtoRule(rule), nil
}

func (s *GRPCServer) DeleteRule(ctx context.Context, req *ratelimitv1.DeleteRuleRequest) (*ratelimitv1.DeleteRuleResponse, error) {
	if err := s.registry.Delete(ctx, req.GetId()); err != nil {
		return nil, toGRPCError(err)
	}
	return &ratelimitv1.DeleteRuleResponse{Ok: true}, nil
}

func toGRPCError(err error) error {
	switch {
	case errors.Is(err, ruleengine.ErrRuleNotFound):
		return status.Error(codes.NotFound, err.Error())
	case errors.Is(err, ruleengine.ErrDuplicateRuleID):
		return status.Error(codes.AlreadyExists, err.Error())
	default:
		return status.Error(codes.Internal, err.Error())
	}
}
