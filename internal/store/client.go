// Package store wraps Redis (single-node or Cluster) and exposes the
// atomic primitives the rule engine needs: sliding-window counting,
// GCRA token buckets, and progressive lockout state (NFR3, NFR4).
package store

import (
	"context"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/aupv9/unigate/internal/config"
)

type Store struct {
	client redis.UniversalClient
}

func New(cfg config.RedisConfig) (*Store, error) {
	opts := &redis.UniversalOptions{
		Addrs:        cfg.Addrs,
		Password:     cfg.Password,
		DB:           cfg.DB,
		DialTimeout:  cfg.DialTimeout.Duration(),
		ReadTimeout:  cfg.ReadTimeout.Duration(),
		WriteTimeout: cfg.WriteTimeout.Duration(),
	}
	var client redis.UniversalClient
	if cfg.ClusterMode {
		client = redis.NewClusterClient(opts.Cluster())
	} else {
		client = redis.NewUniversalClient(opts)
	}
	return &Store{client: client}, nil
}

func (s *Store) Ping(ctx context.Context) error {
	return s.client.Ping(ctx).Err()
}

func (s *Store) Close() error {
	return s.client.Close()
}

// Client exposes the underlying redis.UniversalClient for callers (e.g.
// the admin rule store) that need plain get/set access.
func (s *Store) Client() redis.UniversalClient {
	return s.client
}

// hashTagKey builds the "{tag}" portion shared by all keys belonging to
// the same (namespace, ruleID, identity) so Redis Cluster co-locates them.
func hashTagKey(namespace, ruleID, identity string) string {
	return fmt.Sprintf("{%s:%s:%s}", namespace, ruleID, identity)
}

func nowMillis(t time.Time) int64 {
	return t.UnixMilli()
}
