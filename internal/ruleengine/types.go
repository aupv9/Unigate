// Package ruleengine turns a (rule, composite key) pair into an
// allow/deny decision by combining the sliding-window/GCRA primitives
// in internal/store with progressive brute-force lockout (FR2-FR5).
package ruleengine

import "errors"

var (
	ErrRuleNotFound    = errors.New("ruleengine: rule not found")
	ErrMissingKeyPart  = errors.New("ruleengine: request missing a required key component")
	ErrDuplicateRuleID = errors.New("ruleengine: rule id already exists")
)

// KeyComponent is one piece of a composite rate-limit key, e.g.
// {Kind: "ip", Value: "203.0.113.7"} (FR3).
type KeyComponent struct {
	Kind  string
	Value string
}

type CheckRequest struct {
	RuleID    string
	Key       []KeyComponent
	Cost      int64
	Gateway   string
	Namespace string // overrides the rule's configured namespace when non-empty
}

type CheckResult struct {
	Allow                   bool
	Limit                   int64
	Remaining               int64
	ResetSeconds            int64
	RetryAfterSeconds       int64
	LockedOut               bool
	LockoutRemainingSeconds int64
	MatchedWindow           string
	FailedOpen              bool
	Namespace               string
}

type ResetRequest struct {
	RuleID    string
	Key       []KeyComponent
	Namespace string
}
