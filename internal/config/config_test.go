package config

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func writeTempConfig(t *testing.T, yamlBody string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, []byte(yamlBody), 0o644); err != nil {
		t.Fatalf("write temp config: %v", err)
	}
	return path
}

func TestLoad_ParsesDurationsAndDefaults(t *testing.T) {
	path := writeTempConfig(t, `
redis:
  addrs: ["localhost:6379"]
rules:
  - id: r1
    key_parts: ["ip"]
    windows:
      - limit: 5
        period: 1m
`)
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if cfg.Server.HTTPAddr != ":8080" {
		t.Errorf("expected default http_addr, got %q", cfg.Server.HTTPAddr)
	}
	if got := cfg.Rules[0].Windows[0].Period.Duration(); got != time.Minute {
		t.Errorf("expected period=1m, got %v", got)
	}
	if cfg.Rules[0].Algorithm != AlgorithmSlidingWindow {
		t.Errorf("expected default algorithm sliding_window, got %v", cfg.Rules[0].Algorithm)
	}
	if cfg.Rules[0].FailMode != FailClosed {
		t.Errorf("expected default fail_mode fail_closed, got %v", cfg.Rules[0].FailMode)
	}
}

func TestLoad_RejectsDuplicateRuleIDs(t *testing.T) {
	path := writeTempConfig(t, `
rules:
  - id: dup
    key_parts: ["ip"]
    windows: [{limit: 1, period: 1m}]
  - id: dup
    key_parts: ["ip"]
    windows: [{limit: 1, period: 1m}]
`)
	if _, err := Load(path); err == nil {
		t.Fatalf("expected error for duplicate rule id")
	}
}

func TestLoad_RejectsMissingKeyParts(t *testing.T) {
	path := writeTempConfig(t, `
rules:
  - id: r1
    windows: [{limit: 1, period: 1m}]
`)
	if _, err := Load(path); err == nil {
		t.Fatalf("expected error for missing key_parts")
	}
}

func TestLoad_RejectsInvalidDuration(t *testing.T) {
	path := writeTempConfig(t, `
rules:
  - id: r1
    key_parts: ["ip"]
    windows: [{limit: 1, period: "not-a-duration"}]
`)
	if _, err := Load(path); err == nil {
		t.Fatalf("expected error for invalid duration")
	}
}
