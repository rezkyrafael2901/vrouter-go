package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadRealFile(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "vrouter.json")

	raw := `{
  "providers": [
    {
      "name": "openai",
      "api_base": "https://api.openai.com/v1",
      "keys": ["sk-test-1","sk-test-2"],
      "prefix": "openai/",
      "models": ["gpt-4o","gpt-4o-mini"],
      "default_model": "gpt-4o",
      "weight": 10,
      "keep_prefix": true,
      "is_active": true,
      "api_type": "openai"
    }
  ],
  "combos": [
    {
      "name": "smart-routing",
      "routes": [
        {"provider":"openai","model":"gpt-4o","weight":0.6},
        {"provider":"openai","model":"gpt-4o-mini","weight":0.4}
      ],
      "strategy": "weighted"
    }
  ],
  "proxies": ["socks5://127.0.0.1:9050"],
  "router": {
    "health_check_interval": 60,
    "cb_fail_threshold": 4,
    "cb_lock_seconds": 120,
    "hedge_race_timeout": 2000,
    "speculative_hedge": true,
    "health_score_routing": false,
    "max_fallback_attempts": 5,
    "max_retries_per_provider": 3,
    "prime_timeout": 45
  }
}`
	if err := os.WriteFile(cfgPath, []byte(raw), 0644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}

	cfg, err := Load(cfgPath)
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}

	if len(cfg.Providers) != 1 || cfg.Providers[0].Name != "openai" {
		t.Fatalf("unexpected providers: %+v", cfg.Providers)
	}
	if len(cfg.Combos) != 1 || cfg.Combos[0].Strategy != "weighted" {
		t.Fatalf("unexpected combos: %+v", cfg.Combos)
	}
	if len(cfg.Proxies) != 1 || cfg.Proxies[0] != "socks5://127.0.0.1:9050" {
		t.Fatalf("unexpected proxies: %+v", cfg.Proxies)
	}
	if cfg.Router.HealthCheckInterval != 60 || cfg.Router.CBFailThreshold != 4 {
		t.Fatalf("unexpected router: %+v", cfg.Router)
	}
}

func TestLoadMissingFileReturnsError(t *testing.T) {
	if _, err := Load("/tmp/does_not_exist_vrouter_test.json"); err == nil {
		t.Fatal("expected error for missing config")
	}
}
