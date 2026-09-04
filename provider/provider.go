package provider

import (
	"sync"
	"time"

	"github.com/rezkyrafael2901/vrouter-go/config"
)

// Provider is a runtime model for an upstream provider with health and key rotation state.
type Provider struct {
	Name         string
	ApiBase      string
	Keys         []string
	Prefix       string
	Models       []string
	DefaultModel string
	Weight       int
	Proxy        string
	KeepPrefix   bool
	IsActive     bool
	ApiType      string

	Failures     int
	LockedUntil  time.Time
	TotalRequests int
	TotalErrors   int
	LastError     string
	lastKeyIndex  int

	mu sync.RWMutex
}

// IsHealthy returns true when the provider appears usable for routing.
func (p *Provider) IsHealthy() bool {
	p.mu.RLock()
	defer p.mu.RUnlock()

	if !p.IsActive {
		return false
	}
	if !p.LockedUntil.IsZero() && time.Now().Before(p.LockedUntil) {
		return false
	}
	return p.Failures < CBFailThreshold
}

// IsLocked reports whether the provider is in a circuit-breaker cooldown window.
func (p *Provider) IsLocked() bool {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return !p.LockedUntil.IsZero() && time.Now().Before(p.LockedUntil)
}

// HasKeys reports whether the provider carries at least one API key.
func (p *Provider) HasKeys() bool {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return len(p.Keys) > 0
}

// NextKey returns the next API key using round-robin rotation.
func (p *Provider) NextKey() string {
	p.mu.Lock()
	defer p.mu.Unlock()

	if len(p.Keys) == 0 {
		return ""
	}
	key := p.Keys[p.lastKeyIndex%len(p.Keys)]
	p.lastKeyIndex++
	return key
}

// RecordSuccess updates provider accounting after a successful request.
func (p *Provider) RecordSuccess() {
	p.mu.Lock()
	defer p.mu.Unlock()

	p.TotalRequests++
	if p.Failures > 0 {
		p.Failures--
	}
}

// RecordFailure updates provider accounting and trips the circuit breaker when the failure threshold is reached.
func (p *Provider) RecordFailure(reason string) {
	p.mu.Lock()
	defer p.mu.Unlock()

	p.TotalRequests++
	p.TotalErrors++
	p.LastError = reason

	if p.Failures+1 >= CBFailThreshold {
		TripCircuit(p, reason, CBLockSeconds)
	} else {
		p.Failures++
	}
}

// ToDict exports provider state into a plain map for logging / API responses.
func (p *Provider) ToDict() map[string]interface{} {
	p.mu.RLock()
	defer p.mu.RUnlock()

	locked := !p.LockedUntil.IsZero() && time.Now().Before(p.LockedUntil)

	return map[string]interface{}{
		"name":           p.Name,
		"api_base":       p.ApiBase,
		"prefix":         p.Prefix,
		"default_model":  p.DefaultModel,
		"weight":         p.Weight,
		"proxy":          p.Proxy,
		"keep_prefix":    p.KeepPrefix,
		"is_active":      p.IsActive,
		"api_type":       p.ApiType,
		"models":         p.Models,
		"keys_count":     len(p.Keys),
		"failures":       p.Failures,
		"locked":         locked,
		"locked_until":   p.LockedUntil,
		"total_requests": p.TotalRequests,
		"total_errors":   p.TotalErrors,
		"last_error":     p.LastError,
	}
}

// ---------------------------------------------------------------------------
// Global provider registry
// ---------------------------------------------------------------------------

var (
	providersMu sync.RWMutex
	providers   = make(map[string]*Provider)
)

// Register adds a provider to the global registry.
func Register(p *Provider) {
	providersMu.Lock()
	defer providersMu.Unlock()
	providers[p.Name] = p
}

// Get returns a provider by name, or nil if not found.
func Get(name string) *Provider {
	providersMu.RLock()
	defer providersMu.RUnlock()
	return providers[name]
}

// All returns all registered providers as a slice.
func All() []*Provider {
	providersMu.RLock()
	defer providersMu.RUnlock()
	out := make([]*Provider, 0, len(providers))
	for _, p := range providers {
		out = append(out, p)
	}
	return out
}

// AllMap returns all registered providers as a map.
func AllMap() map[string]*Provider {
	providersMu.RLock()
	defer providersMu.RUnlock()
	out := make(map[string]*Provider, len(providers))
	for k, v := range providers {
		out[k] = v
	}
	return out
}

// Remove deletes a provider by name from the in-memory registry.
func Remove(name string) bool {
	providersMu.Lock()
	defer providersMu.Unlock()
	if _, ok := providers[name]; ok {
		delete(providers, name)
		return true
	}
	return false
}

// InitFromConfig creates Provider instances from config and registers them.
func InitFromConfig(cfgProviders []config.Provider) {
	for _, cp := range cfgProviders {
		p := &Provider{
			Name:         cp.Name,
			ApiBase:      cp.ApiBase,
			Keys:         cp.Keys,
			Prefix:       cp.Prefix,
			Models:       cp.Models,
			DefaultModel: cp.DefaultModel,
			Weight:       cp.Weight,
			Proxy:        cp.Proxy,
			KeepPrefix:   cp.KeepPrefix,
			IsActive:     cp.IsActive,
			ApiType:      cp.ApiType,
		}
		Register(p)
	}
}

// CircuitEvents returns the last N circuit events as a slice.
func CircuitEvents() []CircuitEvent {
	cbEventsGuard.Lock()
	defer cbEventsGuard.Unlock()
	return CB_EVENTS.All()
}
