// Package grpcserver exposes ruleengine.Engine over gRPC (FR1).
package grpcserver

import (
	"context"
	"errors"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	ratelimitv1 "github.com/aupv9/unigate/gen/go/ratelimit/v1"
	"github.com/aupv9/unigate/internal/ruleengine"
)

type Server struct {
	ratelimitv1.UnimplementedRateLimitServiceServer

	engine *ruleengine.Engine
}

func New(engine *ruleengine.Engine) *Server {
	return &Server{engine: engine}
}

func (s *Server) CheckLimit(ctx context.Context, req *ratelimitv1.CheckLimitRequest) (*ratelimitv1.CheckLimitResponse, error) {
	if req.GetRuleId() == "" {
		return nil, status.Error(codes.InvalidArgument, "rule_id is required")
	}

	engineReq := ruleengine.CheckRequest{
		RuleID:    req.GetRuleId(),
		Cost:      req.GetCost(),
		Gateway:   req.GetGateway(),
		Namespace: req.GetNamespace(),
		Key:       fromProtoKey(req.GetKey()),
	}

	res, err := s.engine.CheckLimit(ctx, engineReq)
	if err != nil {
		return nil, toGRPCError(err)
	}

	return &ratelimitv1.CheckLimitResponse{
		Allow:                   res.Allow,
		Limit:                   res.Limit,
		Remaining:               res.Remaining,
		ResetSeconds:            res.ResetSeconds,
		RetryAfterSeconds:       res.RetryAfterSeconds,
		LockedOut:               res.LockedOut,
		LockoutRemainingSeconds: res.LockoutRemainingSeconds,
		MatchedWindow:           res.MatchedWindow,
	}, nil
}

func (s *Server) Reset(ctx context.Context, req *ratelimitv1.ResetRequest) (*ratelimitv1.ResetResponse, error) {
	err := s.engine.Reset(ctx, ruleengine.ResetRequest{
		RuleID:    req.GetRuleId(),
		Namespace: req.GetNamespace(),
		Key:       fromProtoKey(req.GetKey()),
	})
	if err != nil {
		return nil, toGRPCError(err)
	}
	return &ratelimitv1.ResetResponse{Ok: true}, nil
}

func fromProtoKey(components []*ratelimitv1.KeyComponent) []ruleengine.KeyComponent {
	out := make([]ruleengine.KeyComponent, len(components))
	for i, c := range components {
		out[i] = ruleengine.KeyComponent{Kind: c.GetKind(), Value: c.GetValue()}
	}
	return out
}

func toGRPCError(err error) error {
	switch {
	case errors.Is(err, ruleengine.ErrRuleNotFound):
		return status.Error(codes.NotFound, err.Error())
	case errors.Is(err, ruleengine.ErrMissingKeyPart):
		return status.Error(codes.InvalidArgument, err.Error())
	default:
		return status.Error(codes.Internal, err.Error())
	}
}
