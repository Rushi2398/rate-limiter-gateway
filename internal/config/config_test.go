package config

import (
	"os"
	"path/filepath"
	"testing"
)

// writeTempConfig writes the given YAML content to a temp file and returns its path. t.TempDir() auto-cleans after the test.

func writeTempConfig(t *testing.T, content string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatalf("failed to write temp config: %v", err)
	}
	return path
}

const validConfig = `
redis_addr: "localhost:6379"
default_capacity: 100
default_rate: 50

clients:
  api-key-abc123:
    capacity: 500
    rate: 200
  api-key-free-tier:
    capacity: 20
    rate: 5
`

func TestLoad_ParsesValidConfig(t *testing.T) {
	path := writeTempConfig(t, validConfig)

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.RedisAddr != "localhost:6379" {
		t.Errorf("RedisAddr = %q, want %q", cfg.RedisAddr, "localhost:6379")
	}
	if cfg.DefaultCapacity != 100 {
		t.Errorf("DefaultCapacity = %d, want 100", cfg.DefaultCapacity)
	}
	if cfg.DefaultRate != 50 {
		t.Errorf("DefaultRate = %d, want 50", cfg.DefaultRate)
	}
	if len(cfg.Clients) != 2 {
		t.Errorf("len(Clients) = %d, want 2", len(cfg.Clients))
	}

	rule, ok := cfg.Clients["api-key-abc123"]
	if !ok {
		t.Fatal(`expected client "api-key-abc123" to be present`)
	}
	if rule.Capacity != 500 || rule.Rate != 200 {
		t.Errorf("api-key-abc123 rule = %+v, want {Capacity:500 Rate:200}", rule)
	}
}

func TestLoad_MissingFileReturnsError(t *testing.T) {
	_, err := Load("/nonexistent/path/config.yaml")
	if err == nil {
		t.Fatal("expected error for missing file, got nil")
	}
}

func TestLoad_MalformedYAMLReturnsError(t *testing.T) {
	path := writeTempConfig(t, "this: is: not: valid: yaml: at: all:")
	_, err := Load(path)
	if err == nil {
		t.Fatal("expected error for malformed yaml, got nil")
	}
}

func TestLoad_EnvOverridesRedisAddr(t *testing.T) {
	path := writeTempConfig(t, validConfig)

	t.Setenv("REDIS_ADDR", "redis-prod.internal:6379")

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.RedisAddr != "redis-prod.internal:6379" {
		t.Errorf("RedisAddr = %q, want env override %q", cfg.RedisAddr, "redis-prod.internal:6379")
	}
}

func TestLoad_EmptyEnvDoesNotOverride(t *testing.T) {
	path := writeTempConfig(t, validConfig)

	// Explicitly ensure REDIS_ADDR is unset for this test.
	t.Setenv("REDIS_ADDR", "")

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.RedisAddr != "localhost:6379" {
		t.Errorf("RedisAddr = %q, want yaml value %q (empty env should not override)", cfg.RedisAddr, "localhost:6379")
	}
}

func TestLoad_ValidationCatchesBadConfig(t *testing.T) {
	cases := []struct {
		name    string
		content string
	}{
		{
			name: "missing redis_addr",
			content: `
				default_capacity: 100
				default_rate: 50
				`,
		},
		{
			name: "zero default_capacity",
			content: `
				redis_addr: "localhost:6379"
				default_capacity: 0
				default_rate: 50
				`,
		},
		{
			name: "negative default_rate",
			content: `
				redis_addr: "localhost:6379"
				default_capacity: 100
				default_rate: -10
				`,
		},
		{
			name: "client with zero capacity",
			content: `
				redis_addr: "localhost:6379"
				default_capacity: 100
				default_rate: 50
				clients:
				bad-client:
					capacity: 0
					rate: 10
				`,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path := writeTempConfig(t, tc.content)
			_, err := Load(path)
			if err == nil {
				t.Errorf("expected validation error for case %q, got nil", tc.name)
			}
		})
	}
}

func TestRuleFor_ReturnsExplicitClientRule(t *testing.T) {
	path := writeTempConfig(t, validConfig)
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	rule := cfg.RuleFor("api-key-free-tier")
	if rule.Capacity != 20 || rule.Rate != 5 {
		t.Errorf("RuleFor(api-key-free-tier) = %+v, want {Capacity:20 Rate:5}", rule)
	}
}

func TestRuleFor_FallsBackToDefaultsForUnknownClient(t *testing.T) {
	path := writeTempConfig(t, validConfig)
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	rule := cfg.RuleFor("some-client-not-in-config")
	if rule.Capacity != cfg.DefaultCapacity || rule.Rate != cfg.DefaultRate {
		t.Errorf("RuleFor(unknown) = %+v, want defaults {Capacity:%d Rate:%d}",
			rule, cfg.DefaultCapacity, cfg.DefaultRate)
	}
}

func TestFailOpenOrDefault_DefaultsTrueWhenOmitted(t *testing.T) {
	// validConfig has no fail_open key at all.
	path := writeTempConfig(t, validConfig)
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if got := cfg.FailOpenOrDefault(); got != true {
		t.Errorf("FailOpenOrDefault() = %v, want true when fail_open is omitted from yaml", got)
	}
}

func TestFailOpenOrDefault_RespectsExplicitFalse(t *testing.T) {
	content := validConfig + "\nfail_open: false\n"
	path := writeTempConfig(t, content)
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if got := cfg.FailOpenOrDefault(); got != false {
		t.Errorf("FailOpenOrDefault() = %v, want false when yaml explicitly sets fail_open: false", got)
	}
}

func TestFailOpenOrDefault_RespectsExplicitTrue(t *testing.T) {
	content := validConfig + "\nfail_open: true\n"
	path := writeTempConfig(t, content)
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if got := cfg.FailOpenOrDefault(); got != true {
		t.Errorf("FailOpenOrDefault() = %v, want true when yaml explicitly sets fail_open: true", got)
	}
}

func TestAllowAnonymousOrDefault_DefaultsTrueWhenOmitted(t *testing.T) {
	// validConfig has no allow_anonymous key at all.
	path := writeTempConfig(t, validConfig)
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if got := cfg.AllowAnonymousOrDefault(); got != true {
		t.Errorf("AllowAnonymousOrDefault() = %v, want true when allow_anonymous is omitted from yaml", got)
	}
}

func TestAllowAnonymousOrDefault_RespectsExplicitFalse(t *testing.T) {
	content := validConfig + "\nallow_anonymous: false\n"
	path := writeTempConfig(t, content)
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if got := cfg.AllowAnonymousOrDefault(); got != false {
		t.Errorf("AllowAnonymousOrDefault() = %v, want false when yaml explicitly sets allow_anonymous: false", got)
	}
}

func TestAllowAnonymousOrDefault_RespectsExplicitTrue(t *testing.T) {
	content := validConfig + "\nallow_anonymous: true\n"
	path := writeTempConfig(t, content)
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if got := cfg.AllowAnonymousOrDefault(); got != true {
		t.Errorf("AllowAnonymousOrDefault() = %v, want true when yaml explicitly sets allow_anonymous: true", got)
	}
}
