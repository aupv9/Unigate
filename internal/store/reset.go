package store

import (
	"context"
	"fmt"

	"github.com/aupv9/unigate/internal/config"
)

// ResetAll deletes every counter/bucket/lockout key associated with
// (namespace, ruleID, identity) — used by the Reset RPC for operational
// and test scenarios.
func (s *Store) ResetAll(ctx context.Context, namespace, ruleID, identity string, rule config.RuleConfig) error {
	tag := hashTagKey(namespace, ruleID, identity)
	keys := []string{
		fmt.Sprintf("unigate:%s:gcra", tag),
		fmt.Sprintf("unigate:%s:lockout", tag),
	}
	for _, w := range rule.Windows {
		keys = append(keys, fmt.Sprintf("unigate:%s:sw:%d", tag, w.Period.Duration().Milliseconds()))
	}
	return s.client.Del(ctx, keys...).Err()
}
