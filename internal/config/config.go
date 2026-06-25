package config

import (
	"fmt"
	"os"

	"github.com/goccy/go-yaml"
)

// Package config loads gateway settings from a YAML file, with a small set of environment variables allowed to override specific fields. This keeps secrets and per-deployment values (like the Redis address) out of the committed YAML, while keeping per-client rate-limit rules — which are numerous and change often — easy to review in version control.

// ClientRule defines the token-bucket parameters for one client.
type ClientRule struct {
	Capacity int `yaml:"capacity"`
	Rate     int `yaml:"rate"`
}

// Config is the full gateway configuration.
type Config struct {
	RedisAddr string `yaml:"redis_addr"`
	// DefaultCapacity and DefaultRate apply to any client not listed explicitly in Clients below.
	DefaultCapacity int `yaml:"default_capacity"`
	DefaultRate     int `yaml:"default_rate"`

	// Clients maps a client identifier (typically an API key) to its specific rate-limit rule, overriding the defaults above.
	Clients map[string]ClientRule `yaml:"clients"`
}

// Load reads and parses the YAML config at path, then applies environment variable overrides on top. Currently only REDIS_ADDR is override-able; this is intentional — per-client rules are meant to live in version control, not in ad hoc environment variables that are easy to lose track of across deployments.

func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("config: failed to read %s: %w", path, err)
	}

	var cfg Config
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("config: failed to parse %s: %w", path, err)
	}

	if err := cfg.validate(); err != nil {
		return nil, fmt.Errorf("config: invalid config in %s: %w", path, err)
	}

	cfg.applyEnvOverrides()

	return &cfg, nil
}

// applyEnvOverrides layers environment variables on top of the parsed YAML. Called after validate() so a bad YAML file is still caught even if the env override would have masked a missing required field.
func (c *Config) applyEnvOverrides() {
	if v := os.Getenv("REDIS_ADDR"); v != "" {
		c.RedisAddr = v
	}
}

// validate catches config mistakes early — at startup — rather than as a confusing runtime error the first time a request hits an affected client. A gateway that fails fast on bad config is much easier to operate than one that silently misbehaves.
func (c *Config) validate() error {
	if c.RedisAddr == "" {
		return fmt.Errorf("redis_addr must be set")
	}
	if c.DefaultCapacity <= 0 {
		return fmt.Errorf("default_capacity must be positive, got %d", c.DefaultCapacity)
	}
	if c.DefaultRate <= 0 {
		return fmt.Errorf("default_rate must be positive, got %d", c.DefaultRate)
	}
	for client, rule := range c.Clients {
		if rule.Capacity <= 0 {
			return fmt.Errorf("client %q: capacity must be positive, got %d", client, rule.Capacity)
		}
		if rule.Rate <= 0 {
			return fmt.Errorf("client %q: rate must be positive, got %d", client, rule.Rate)
		}
	}
	return nil
}

// RuleFor returns the rate-limit rule for the given client ID, falling
// back to the configured defaults if the client has no explicit entry.
func (c *Config) RuleFor(clientID string) ClientRule {
	if rule, ok := c.Clients[clientID]; ok {
		return rule
	}
	return ClientRule{Capacity: c.DefaultCapacity, Rate: c.DefaultRate}
}
