package ruleengine

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"

	"github.com/redis/go-redis/v9"

	"github.com/aupv9/unigate/internal/config"
)

const rulesHashKey = "unigate:admin:rules"

// Registry holds the live rule set. It is seeded from the static config
// file at boot and can be mutated at runtime through the Admin API
// (FR8) without redeploying any gateway. Rule writes are persisted to a
// Redis hash so every stateless service instance converges on the same
// rule set (NFR3) via Refresh.
type Registry struct {
	mu    sync.RWMutex
	rules map[string]config.RuleConfig

	redis redis.UniversalClient // optional; nil disables cross-instance persistence
}

func NewRegistry(initial []config.RuleConfig, redisClient redis.UniversalClient) *Registry {
	m := make(map[string]config.RuleConfig, len(initial))
	for _, r := range initial {
		m[r.ID] = r
	}
	return &Registry{rules: m, redis: redisClient}
}

func (r *Registry) Get(id string) (config.RuleConfig, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	rule, ok := r.rules[id]
	return rule, ok
}

func (r *Registry) List(namespace string) []config.RuleConfig {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]config.RuleConfig, 0, len(r.rules))
	for _, rule := range r.rules {
		if namespace == "" || rule.Namespace == namespace {
			out = append(out, rule)
		}
	}
	return out
}

func (r *Registry) Create(ctx context.Context, rule config.RuleConfig) error {
	r.mu.Lock()
	if _, exists := r.rules[rule.ID]; exists {
		r.mu.Unlock()
		return ErrDuplicateRuleID
	}
	r.rules[rule.ID] = rule
	r.mu.Unlock()
	return r.persist(ctx, rule)
}

func (r *Registry) Update(ctx context.Context, rule config.RuleConfig) error {
	r.mu.Lock()
	if _, exists := r.rules[rule.ID]; !exists {
		r.mu.Unlock()
		return ErrRuleNotFound
	}
	r.rules[rule.ID] = rule
	r.mu.Unlock()
	return r.persist(ctx, rule)
}

func (r *Registry) Delete(ctx context.Context, id string) error {
	r.mu.Lock()
	if _, exists := r.rules[id]; !exists {
		r.mu.Unlock()
		return ErrRuleNotFound
	}
	delete(r.rules, id)
	r.mu.Unlock()

	if r.redis == nil {
		return nil
	}
	if err := r.redis.HDel(ctx, rulesHashKey, id).Err(); err != nil {
		return fmt.Errorf("ruleengine: delete rule %s: %w", id, err)
	}
	return nil
}

func (r *Registry) persist(ctx context.Context, rule config.RuleConfig) error {
	if r.redis == nil {
		return nil
	}
	data, err := json.Marshal(rule)
	if err != nil {
		return fmt.Errorf("ruleengine: marshal rule %s: %w", rule.ID, err)
	}
	if err := r.redis.HSet(ctx, rulesHashKey, rule.ID, data).Err(); err != nil {
		return fmt.Errorf("ruleengine: persist rule %s: %w", rule.ID, err)
	}
	return nil
}

// Refresh pulls the full rule set from Redis and swaps it in locally,
// so that a rule change made on one instance becomes visible on all
// others. Call this on a periodic ticker from cmd/server.
func (r *Registry) Refresh(ctx context.Context) error {
	if r.redis == nil {
		return nil
	}
	all, err := r.redis.HGetAll(ctx, rulesHashKey).Result()
	if err != nil {
		return fmt.Errorf("ruleengine: refresh rules: %w", err)
	}
	if len(all) == 0 {
		return nil
	}
	fresh := make(map[string]config.RuleConfig, len(all))
	for id, data := range all {
		var rule config.RuleConfig
		if err := json.Unmarshal([]byte(data), &rule); err != nil {
			return fmt.Errorf("ruleengine: unmarshal rule %s: %w", id, err)
		}
		fresh[id] = rule
	}

	r.mu.Lock()
	for id, rule := range fresh {
		r.rules[id] = rule
	}
	r.mu.Unlock()
	return nil
}
