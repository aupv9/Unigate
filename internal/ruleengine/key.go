package ruleengine

import (
	"sort"
	"strings"
)

// buildIdentity produces a deterministic string identity for a rule's
// configured key_parts (e.g. ["ip","username"]) out of the components
// present on the incoming request (FR3: IP-only, username-only, or
// IP+username combined).
func buildIdentity(keyParts []string, components []KeyComponent) (string, error) {
	byKind := make(map[string]string, len(components))
	for _, c := range components {
		byKind[c.Kind] = c.Value
	}

	parts := make([]string, 0, len(keyParts))
	for _, kind := range keyParts {
		val, ok := byKind[kind]
		if !ok || val == "" {
			return "", ErrMissingKeyPart
		}
		parts = append(parts, kind+"="+val)
	}
	// key_parts order is meaningful for readability but must not affect
	// which Redis key we hash to, so sort before joining.
	sort.Strings(parts)
	return strings.Join(parts, "|"), nil
}
