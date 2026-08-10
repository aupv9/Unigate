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

func TestLoad_ExpandsEnvVarsForSecrets(t *testing.T) {
	t.Setenv("TEST_KONG_API_KEY", "s3cr3t-from-env")
	t.Setenv("TEST_REDIS_PASSWORD", "redis-pw")

	path := writeTempConfig(t, `
redis:
  addrs: ["localhost:6379"]
  password: "${TEST_REDIS_PASSWORD}"
auth:
  api_keys:
    kong: "${TEST_KONG_API_KEY}"
    apisix: "${TEST_APISIX_API_KEY:-fallback-default}"
rules:
  - id: r1
    key_parts: ["ip"]
    windows: [{limit: 1, period: 1m}]
`)
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if cfg.Redis.Password != "redis-pw" {
		t.Errorf("expected redis password from env, got %q", cfg.Redis.Password)
	}
	if cfg.Auth.APIKeys["kong"] != "s3cr3t-from-env" {
		t.Errorf("expected kong api key from env, got %q", cfg.Auth.APIKeys["kong"])
	}
	if cfg.Auth.APIKeys["apisix"] != "fallback-default" {
		t.Errorf("expected apisix api key to fall back to default, got %q", cfg.Auth.APIKeys["apisix"])
	}
}

func TestLoad_UnsetEnvVarWithNoDefaultExpandsEmpty(t *testing.T) {
	os.Unsetenv("TEST_TOTALLY_UNSET_VAR")
	path := writeTempConfig(t, `
auth:
  api_keys:
    kong: "${TEST_TOTALLY_UNSET_VAR}"
rules:
  - id: r1
    key_parts: ["ip"]
    windows: [{limit: 1, period: 1m}]
`)
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if cfg.Auth.APIKeys["kong"] != "" {
		t.Errorf("expected empty string for unset var with no default, got %q", cfg.Auth.APIKeys["kong"])
	}
}
