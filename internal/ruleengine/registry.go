package ruleengine

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/aupv9/unigate/internal/config"
)

const (
	rulesHashKey      = "unigate:admin:rules"
	maxHistoryEntries = 10
)

// RuleVersion is one historical snapshot of a rule, kept so an Admin
// API change can be rolled back without having to remember/re-type
// the previous thresholds by hand.
type RuleVersion struct {
	Version   int               `json:"version"`
	Rule      config.RuleConfig `json:"rule"`
	UpdatedAt time.Time         `json:"updated_at"`
}

// storedRule is the on-the-wire shape in Redis: the rule plus its
// current version number, so every stateless instance converges on
// the same version count via Refresh, not just the same rule content.
type storedRule struct {
	Version int               `json:"version"`
	Rule    config.RuleConfig `json:"rule"`
}

// Registry holds the live rule set. It is seeded from the static config
// file at boot and can be mutated at runtime through the Admin API
// (FR8) without redeploying any gateway. Rule writes are persisted to a
// Redis hash so every stateless service instance converges on the same
// rule set (NFR3) via Refresh.
type Registry struct {
	mu       sync.RWMutex
	rules    map[string]config.RuleConfig
	versions map[string]int
	// history is the in-memory fallback used when redis is nil (e.g.
	// tests, or a deployment that intentionally runs without
	// cross-instance persistence). When redis is set, History/Rollback
	// read authoritative history from Redis instead, since multiple
	// instances can each have written versions the others don't know
	// about locally.
	history map[string][]RuleVersion

	redis redis.UniversalClient // optional; nil disables cross-instance persistence
	clock func() time.Time
}

func NewRegistry(initial []config.RuleConfig, redisClient redis.UniversalClient) *Registry {
	m := make(map[string]config.RuleConfig, len(initial))
	versions := make(map[string]int, len(initial))
	for _, r := range initial {
		m[r.ID] = r
		versions[r.ID] = 1
	}
	return &Registry{
		rules:    m,
		versions: versions,
		history:  make(map[string][]RuleVersion),
		redis:    redisClient,
		clock:    time.Now,
	}
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
	r.versions[rule.ID] = 1
	r.mu.Unlock()
	return r.persist(ctx, rule, 1)
}

// Update replaces a rule's content, incrementing its version and
// snapshotting the previous content into history (FR8 + rollback
// support) - the previous version is never lost, only superseded.
func (r *Registry) Update(ctx context.Context, rule config.RuleConfig) error {
	r.mu.Lock()
	oldRule, exists := r.rules[rule.ID]
	if !exists {
		r.mu.Unlock()
		return ErrRuleNotFound
	}
	oldVersion := r.versions[rule.ID]
	newVersion := oldVersion + 1
	now := r.clock()
	snapshot := RuleVersion{Version: oldVersion, Rule: oldRule, UpdatedAt: now}

	r.rules[rule.ID] = rule
	r.versions[rule.ID] = newVersion
	r.history[rule.ID] = prependCapped(r.history[rule.ID], snapshot, maxHistoryEntries)
	r.mu.Unlock()

	// History is a supplementary audit trail, not required for the
	// rule change itself to take effect - a failure here shouldn't
	// fail the whole update.
	r.pushHistory(ctx, rule.ID, snapshot)

	return r.persist(ctx, rule, newVersion)
}

func (r *Registry) Delete(ctx context.Context, id string) error {
	r.mu.Lock()
	if _, exists := r.rules[id]; !exists {
		r.mu.Unlock()
		return ErrRuleNotFound
	}
	delete(r.rules, id)
	delete(r.versions, id)
	delete(r.history, id)
	r.mu.Unlock()

	if r.redis == nil {
		return nil
	}
	if err := r.redis.HDel(ctx, rulesHashKey, id).Err(); err != nil {
		return fmt.Errorf("ruleengine: delete rule %s: %w", id, err)
	}
	_ = r.redis.Del(ctx, historyKey(id)).Err()
	return nil
}

// History returns this rule's past versions, most recent first,
// capped at maxHistoryEntries. Does not include the current version -
// use Get for that.
func (r *Registry) History(ctx context.Context, id string) ([]RuleVersion, error) {
	if r.redis != nil {
		raw, err := r.redis.LRange(ctx, historyKey(id), 0, maxHistoryEntries-1).Result()
		if err != nil {
			return nil, fmt.Errorf("ruleengine: history %s: %w", id, err)
		}
		out := make([]RuleVersion, 0, len(raw))
		for _, item := range raw {
			var v RuleVersion
			if err := json.Unmarshal([]byte(item), &v); err != nil {
				return nil, fmt.Errorf("ruleengine: unmarshal history entry for %s: %w", id, err)
			}
			out = append(out, v)
		}
		return out, nil
	}

	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]RuleVersion, len(r.history[id]))
	copy(out, r.history[id])
	return out, nil
}

// Rollback reapplies a rule's historical content as a brand-new
// version (like `git revert`, not a destructive rewind) - so the
// history log stays a complete, forward-only audit trail. toVersion
// == 0 means "roll back to the immediately preceding version".
func (r *Registry) Rollback(ctx context.Context, id string, toVersion int) (config.RuleConfig, error) {
	if _, ok := r.Get(id); !ok {
		return config.RuleConfig{}, ErrRuleNotFound
	}

	history, err := r.History(ctx, id)
	if err != nil {
		return config.RuleConfig{}, err
	}
	if len(history) == 0 {
		return config.RuleConfig{}, ErrNoHistory
	}

	target := toVersion
	if target == 0 {
		target = history[0].Version
	}

	for _, v := range history {
		if v.Version == target {
			if err := r.Update(ctx, v.Rule); err != nil {
				return config.RuleConfig{}, err
			}
			rule, _ := r.Get(id)
			return rule, nil
		}
	}
	return config.RuleConfig{}, fmt.Errorf("ruleengine: version %d not found in history for rule %s: %w", target, id, ErrVersionNotFound)
}

func (r *Registry) persist(ctx context.Context, rule config.RuleConfig, version int) error {
	if r.redis == nil {
		return nil
	}
	data, err := json.Marshal(storedRule{Version: version, Rule: rule})
	if err != nil {
		return fmt.Errorf("ruleengine: marshal rule %s: %w", rule.ID, err)
	}
	if err := r.redis.HSet(ctx, rulesHashKey, rule.ID, data).Err(); err != nil {
		return fmt.Errorf("ruleengine: persist rule %s: %w", rule.ID, err)
	}
	return nil
}

func (r *Registry) pushHistory(ctx context.Context, id string, entry RuleVersion) {
	if r.redis == nil {
		return
	}
	data, err := json.Marshal(entry)
	if err != nil {
		return
	}
	key := historyKey(id)
	pipe := r.redis.TxPipeline()
	pipe.LPush(ctx, key, data)
	pipe.LTrim(ctx, key, 0, maxHistoryEntries-1)
	_, _ = pipe.Exec(ctx)
}

func historyKey(id string) string {
	return "unigate:admin:rule_history:" + id
}

func prependCapped(list []RuleVersion, entry RuleVersion, cap int) []RuleVersion {
	list = append([]RuleVersion{entry}, list...)
	if len(list) > cap {
		list = list[:cap]
	}
	return list
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
	freshRules := make(map[string]config.RuleConfig, len(all))
	freshVersions := make(map[string]int, len(all))
	for id, data := range all {
		var stored storedRule
		if err := json.Unmarshal([]byte(data), &stored); err != nil {
			return fmt.Errorf("ruleengine: unmarshal rule %s: %w", id, err)
		}
		freshRules[id] = stored.Rule
		freshVersions[id] = stored.Version
	}

	r.mu.Lock()
	for id, rule := range freshRules {
		r.rules[id] = rule
		r.versions[id] = freshVersions[id]
	}
	r.mu.Unlock()
	return nil
}
