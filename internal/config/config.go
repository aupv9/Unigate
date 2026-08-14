// Package config loads the rate-limit service's static configuration:
// server listeners, Redis connection, and the initial rule set.
package config

import (
	"fmt"
	"os"
	"time"

	"gopkg.in/yaml.v3"
)

type Config struct {
	Server  ServerConfig  `yaml:"server"`
	Redis   RedisConfig   `yaml:"redis"`
	Rules   []RuleConfig  `yaml:"rules"`
	Auth    AuthConfig    `yaml:"auth"`
	Tracing TracingConfig `yaml:"tracing"`
}

// TracingConfig configures OpenTelemetry distributed tracing across
// the CheckLimit path (gRPC/HTTP request -> rule engine -> Redis Lua
// calls), exported via OTLP/HTTP. Disabled by default - tracing adds
// per-request overhead and an external collector dependency that
// shouldn't be assumed present.
type TracingConfig struct {
	Enabled bool `yaml:"enabled"`
	// OTLPEndpoint is host:port of an OTLP/HTTP collector (e.g. an
	// OpenTelemetry Collector or Jaeger's OTLP receiver), without a
	// scheme - e.g. "otel-collector:4318".
	OTLPEndpoint string `yaml:"otlp_endpoint"`
	ServiceName  string `yaml:"service_name"`
	// SampleRatio is the fraction of traces to record (0.0-1.0).
	// Defaults to 1.0 (sample everything) if unset AND Enabled is
	// true, since an explicit zero value is indistinguishable from
	// "unset" in YAML - set it explicitly to reduce sampling.
	SampleRatio float64 `yaml:"sample_ratio"`
}

type ServerConfig struct {
	GRPCAddr    string    `yaml:"grpc_addr"`
	HTTPAddr    string    `yaml:"http_addr"`
	AdminAddr   string    `yaml:"admin_addr"`
	MetricsAddr string    `yaml:"metrics_addr"`
	TLS         TLSConfig `yaml:"tls"`
}

// TLSConfig configures native TLS/mTLS for the gRPC, CheckLimit HTTP,
// and Admin HTTP listeners (NFR5). Applies uniformly to all three -
// they're behind the same adapter<->service trust boundary. Disabled
// by default, matching a deployment where TLS is instead terminated
// by a service mesh/sidecar in front of the listeners; the API-key
// auth in AuthConfig works independently of whether this is enabled.
type TLSConfig struct {
	Enabled  bool   `yaml:"enabled"`
	CertFile string `yaml:"cert_file"`
	KeyFile  string `yaml:"key_file"`
	// RequireClientCert turns this into mutual TLS: ClientCAFile must
	// then be set, and every connection must present a certificate
	// signed by it. When false, this is server-side TLS only.
	RequireClientCert bool   `yaml:"require_client_cert"`
	ClientCAFile      string `yaml:"client_ca_file"`
}

type RedisConfig struct {
	Addrs        []string `yaml:"addrs"`
	Password     string   `yaml:"password"`
	DB           int      `yaml:"db"`
	DialTimeout  Duration `yaml:"dial_timeout"`
	ReadTimeout  Duration `yaml:"read_timeout"`
	WriteTimeout Duration `yaml:"write_timeout"`
	// ClusterMode toggles go-redis' cluster client vs a single-node client.
	// Production deployments should run a real Redis Cluster (NFR3).
	ClusterMode bool `yaml:"cluster_mode"`
}

// AuthConfig configures adapter <-> service authentication (NFR5).
// v1 supports a simple per-gateway API key; mTLS is expected to be
// terminated by the service mesh/sidecar in front of the listeners.
type AuthConfig struct {
	APIKeys map[string]string `yaml:"api_keys"` // gateway name -> key
}

type Algorithm string

const (
	AlgorithmSlidingWindow Algorithm = "sliding_window"
	AlgorithmGCRA          Algorithm = "gcra"
)

type FailMode string

const (
	FailOpen   FailMode = "fail_open"
	FailClosed FailMode = "fail_closed"
)

type WindowConfig struct {
	Limit  int64    `yaml:"limit" json:"limit"`
	Period Duration `yaml:"period" json:"period"`
}

type LockoutStepConfig struct {
	AfterViolations int      `yaml:"after_violations" json:"after_violations"`
	Lockout         Duration `yaml:"lockout" json:"lockout"`
}

type LockoutConfig struct {
	Enabled      bool                `yaml:"enabled" json:"enabled"`
	Steps        []LockoutStepConfig `yaml:"steps" json:"steps"`
	ViolationTTL Duration            `yaml:"violation_ttl" json:"violation_ttl"`
}

// RuleConfig is the on-disk / admin-API shape of a Rule (FR2-FR5, FR8).
type RuleConfig struct {
	ID          string         `yaml:"id" json:"id"`
	Description string         `yaml:"description" json:"description"`
	Algorithm   Algorithm      `yaml:"algorithm" json:"algorithm"`
	KeyParts    []string       `yaml:"key_parts" json:"key_parts"` // e.g. ["ip"], ["username"], ["ip","username"]
	Windows     []WindowConfig `yaml:"windows" json:"windows"`
	Burst       int64          `yaml:"burst" json:"burst"` // GCRA only
	Lockout     LockoutConfig  `yaml:"lockout" json:"lockout"`
	FailMode    FailMode       `yaml:"fail_mode" json:"fail_mode"`
	Namespace   string         `yaml:"namespace" json:"namespace"`
}

func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read config %s: %w", path, err)
	}
	data = expandEnv(data)
	var cfg Config
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parse config %s: %w", path, err)
	}
	cfg.applyDefaults()
	if err := cfg.validate(); err != nil {
		return nil, fmt.Errorf("invalid config %s: %w", path, err)
	}
	return &cfg, nil
}

func (c *Config) applyDefaults() {
	if c.Server.GRPCAddr == "" {
		c.Server.GRPCAddr = ":9090"
	}
	if c.Server.HTTPAddr == "" {
		c.Server.HTTPAddr = ":8080"
	}
	if c.Server.AdminAddr == "" {
		c.Server.AdminAddr = ":8081"
	}
	if c.Server.MetricsAddr == "" {
		c.Server.MetricsAddr = ":9100"
	}
	if len(c.Redis.Addrs) == 0 {
		c.Redis.Addrs = []string{"127.0.0.1:6379"}
	}
	if c.Redis.DialTimeout == 0 {
		c.Redis.DialTimeout = Duration(500 * time.Millisecond)
	}
	if c.Redis.ReadTimeout == 0 {
		c.Redis.ReadTimeout = Duration(200 * time.Millisecond)
	}
	if c.Redis.WriteTimeout == 0 {
		c.Redis.WriteTimeout = Duration(200 * time.Millisecond)
	}
	if c.Tracing.ServiceName == "" {
		c.Tracing.ServiceName = "unigate"
	}
	if c.Tracing.SampleRatio == 0 {
		c.Tracing.SampleRatio = 1.0
	}
	for i := range c.Rules {
		r := &c.Rules[i]
		if r.Algorithm == "" {
			r.Algorithm = AlgorithmSlidingWindow
		}
		if r.FailMode == "" {
			r.FailMode = FailClosed
		}
		if r.Namespace == "" {
			r.Namespace = "default"
		}
		if r.Lockout.Enabled && r.Lockout.ViolationTTL == 0 {
			r.Lockout.ViolationTTL = Duration(time.Hour)
		}
	}
}

func (c *Config) validate() error {
	if c.Server.TLS.Enabled {
		if c.Server.TLS.CertFile == "" || c.Server.TLS.KeyFile == "" {
			return fmt.Errorf("server.tls: cert_file and key_file are required when enabled")
		}
		if c.Server.TLS.RequireClientCert && c.Server.TLS.ClientCAFile == "" {
			return fmt.Errorf("server.tls: client_ca_file is required when require_client_cert is true")
		}
	}

	if c.Tracing.Enabled && c.Tracing.OTLPEndpoint == "" {
		return fmt.Errorf("tracing.otlp_endpoint is required when enabled")
	}
	if c.Tracing.SampleRatio < 0 || c.Tracing.SampleRatio > 1 {
		return fmt.Errorf("tracing.sample_ratio must be between 0 and 1, got %v", c.Tracing.SampleRatio)
	}

	seen := map[string]bool{}
	for _, r := range c.Rules {
		if r.ID == "" {
			return fmt.Errorf("rule missing id")
		}
		if seen[r.ID] {
			return fmt.Errorf("duplicate rule id %q", r.ID)
		}
		seen[r.ID] = true
		if len(r.Windows) == 0 {
			return fmt.Errorf("rule %q: at least one window required", r.ID)
		}
		if len(r.KeyParts) == 0 {
			return fmt.Errorf("rule %q: at least one key_parts entry required", r.ID)
		}
		if r.Algorithm != AlgorithmSlidingWindow && r.Algorithm != AlgorithmGCRA {
			return fmt.Errorf("rule %q: unknown algorithm %q", r.ID, r.Algorithm)
		}
		if r.FailMode != FailOpen && r.FailMode != FailClosed {
			return fmt.Errorf("rule %q: unknown fail_mode %q", r.ID, r.FailMode)
		}
		for _, s := range r.Lockout.Steps {
			if s.AfterViolations <= 0 {
				return fmt.Errorf("rule %q: lockout step after_violations must be > 0", r.ID)
			}
		}
	}
	return nil
}
