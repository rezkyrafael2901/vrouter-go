package provider

import "time"

// Unlock forces the provider out of a circuit-breaker lock.
// This is primarily useful for testing and administrative recovery.
func (p *Provider) Unlock() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.LockedUntil = time.Time{}
}
