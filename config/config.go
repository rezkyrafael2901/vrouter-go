package config

import (
	"fmt"
	"path/filepath"
	"time"

	"github.com/spf13/viper"
)

// RouterStatePath is the on-disk path for persisting router state.
// Mirrors the Python VRouter's router_state.json location.
var RouterStatePath = "router_state.json"

// PersistInterval governs how often stats/state is flushed to disk.
var PersistInterval = 5 * time.Second

// Provider is the config definition for a single upstream model provider.
type Provider struct {
	Name         string   `mapstructure:"name"`
	ApiBase      string   `mapstructure:"api_base"`
	Keys         []string `mapstructure:"keys"`
	Prefix       string   `mapstructure:"prefix"`
	Models       []string `mapstructure:"models"`
	DefaultModel string   `mapstructure:"default_model"`
	Weight       int      `mapstructure:"weight"`
	Proxy        string   `mapstructure:"proxy"`
	KeepPrefix   bool     `mapstructure:"keep_prefix"`
	IsActive     bool     `mapstructure:"is_active"`
	ApiType      string   `mapstructure:"api_type"`
}

// ComboRoute represents a single route entry in a combo definition.
type ComboRoute struct {
	Provider string  `mapstructure:"provider"`
	Model    string  `mapstructure:"model"`
	Weight   float64 `mapstructure:"weight"`
}

// Combo is a named bundle of routes across providers/models.
type Combo struct {
	Name     string       `mapstructure:"name"`
	Routes   []ComboRoute `mapstructure:"routes"`
	Strategy string       `mapstructure:"strategy"`
}

// Router holds resilience and orchestration config.
type Router struct {
	HealthCheckInterval   int  `mapstructure:"health_check_interval"`
	CBFailThreshold       int  `mapstructure:"cb_fail_threshold"`
	CBLockSeconds         int  `mapstructure:"cb_lock_seconds"`
	HedgeRaceTimeout      int  `mapstructure:"hedge_race_timeout"`
	SpeculativeHedge      bool `mapstructure:"speculative_hedge"`
	HealthScoreRouting    bool `mapstructure:"health_score_routing"`
	MaxFallbackAttempts   int  `mapstructure:"max_fallback_attempts"`
	MaxRetriesPerProvider int  `mapstructure:"max_retries_per_provider"`
	PrimeTimeout          int  `mapstructure:"prime_timeout"`
}

// Config is the top-level VRouter configuration.
type Config struct {
	Providers []Provider `mapstructure:"providers"`
	Combos    []Combo    `mapstructure:"combos"`
	Proxies   []string   `mapstructure:"proxies"`
	Router    Router     `mapstructure:"router"`
}

// DefaultModelCosts maps model name -> [input, output] per 1M tokens.
var DefaultModelCosts = map[string][2]float64{
	"gpt-4o":          {2.50, 10.00},
	"claude-sonnet-4": {3.00, 15.00},
	"deepseek-chat":   {0.14, 0.28},
	"gemini-2.5-flash": {0.15, 0.60},
}

// Load reads and merges the vrouter configuration from path.
func Load(path string) (*Config, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, fmt.Errorf("resolve config path: %w", err)
	}

	v := viper.New()
	v.SetConfigFile(abs)
	v.AutomaticEnv()

	v.SetDefault("router.health_check_interval", 120)
	v.SetDefault("router.cb_fail_threshold", 3)
	v.SetDefault("router.cb_lock_seconds", 180)
	v.SetDefault("router.hedge_race_timeout", 1500)
	v.SetDefault("router.speculative_hedge", false)
	v.SetDefault("router.health_score_routing", true)
	v.SetDefault("router.max_fallback_attempts", 3)
	v.SetDefault("router.max_retries_per_provider", 2)
	v.SetDefault("router.prime_timeout", 60)

	if err := v.ReadInConfig(); err != nil {
		return nil, fmt.Errorf("read config: %w", err)
	}

	var cfg Config
	if err := v.Unmarshal(&cfg); err != nil {
		return nil, fmt.Errorf("unmarshal config: %w", err)
	}

	return &cfg, nil
}
