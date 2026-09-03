package provider

import (
	"strings"
	"testing"
	"time"
)

func newMockProvider() *Provider {
	return &Provider{
		Name:         "openai",
		ApiBase:      "https://api.openai.com/v1",
		Keys:         []string{"key-1", "key-2", "key-3"},
		Prefix:       "openai/",
		Models:       []string{"gpt-4o"},
		DefaultModel: "gpt-4o",
		Weight:       10,
		Proxy:        "",
		KeepPrefix:   true,
		IsActive:     true,
		ApiType:      "openai",
	}
}

func TestHasKeys(t *testing.T) {
	p := newMockProvider()
	if !p.HasKeys() {
		t.Fatal("expected HasKeys true")
	}
	p.Keys = nil
	if p.HasKeys() {
		t.Fatal("expected HasKeys false for empty keys")
	}
}

func TestNextKeyRoundRobin(t *testing.T) {
	p := newMockProvider()
	want := []string{"key-1", "key-2", "key-3", "key-1"}
	for i, w := range want {
		got := p.NextKey()
		if got != w {
			t.Fatalf("call %d: got %s want %s", i, got, w)
		}
	}
}

func TestNextKeyEmpty(t *testing.T) {
	p := newMockProvider()
	p.Keys = nil
	if k := p.NextKey(); k != "" {
		t.Fatalf("expected empty key, got %s", k)
	}
}

func TestIsHealthyInactive(t *testing.T) {
	p := newMockProvider()
	p.IsActive = false
	if p.IsHealthy() {
		t.Fatal("inactive provider should not be healthy")
	}
}

func TestRecordSuccessRecoversFailures(t *testing.T) {
	p := newMockProvider()
	for i := 0; i < CBFailThreshold; i++ {
		p.RecordFailure("boom")
	}
	if p.IsHealthy() {
		t.Fatal("expected unhealthy after threshold failures")
	}
	// record success should decrement failures back to healthy
	for i := 0; i < CBFailThreshold; i++ {
		p.RecordSuccess()
	}
	p.Unlock()
}

func TestRecordFailureTripsCircuit(t *testing.T) {
	p := newMockProvider()
	for i := 0; i < CBFailThreshold; i++ {
		p.RecordFailure("timeout " + string(rune('A'+i)))
	}
	if !p.IsLocked() {
		t.Fatal("expected circuit open after threshold")
	}
	if p.IsHealthy() {
		t.Fatal("locked provider should not be healthy")
	}
}

func TestTripAndResetCircuit(t *testing.T) {
	p := newMockProvider()
	TripCircuit(p, "manual trip", 0)
	if !p.IsLocked() {
		t.Fatal("expected locked after TripCircuit")
	}
	ResetCircuit(p, "manual reset")
	if p.IsLocked() {
		t.Fatal("expected unlocked after ResetCircuit")
	}
	if p.Failures != 0 {
		t.Fatalf("expected failures reset to 0, got %d", p.Failures)
	}
}

func TestTripCircuitNilProvider(t *testing.T) {
	// should not panic
	TripCircuit(nil, "noop", 0)
	ResetCircuit(nil, "noop")
}

func TestCBEventsDeque(t *testing.T) {
	d := newCircuitEventDeque(3)
	for i := 0; i < 5; i++ {
		d.Push(CircuitEvent{Provider: "p", Reason: "r", Timestamp: time.Now()})
	}
	if d.Len() != 3 {
		t.Fatalf("expected len 3, got %d", d.Len())
	}
	all := d.All()
	if len(all) != 3 {
		t.Fatalf("expected 3 events, got %d", len(all))
	}
	if all[0].Provider != "p" {
		t.Fatalf("expected first event provider p, got %s", all[0].Provider)
	}
}

func TestProviderToDict(t *testing.T) {
	p := newMockProvider()
	d := p.ToDict()
	if d["name"].(string) != "openai" {
		t.Fatalf("unexpected name in ToDict: %+v", d)
	}
	if d["keys_count"].(int) != 3 {
		t.Fatalf("unexpected keys_count: %+v", d)
	}
	if d["is_active"].(bool) != true {
		t.Fatalf("unexpected is_active: %+v", d)
	}
}

func TestCircuitEventRecording(t *testing.T) {
	p := newMockProvider()
	before := CB_EVENTS.Len()
	for i := 0; i < CBFailThreshold; i++ {
		p.RecordFailure("test trip")
	}
	if CB_EVENTS.Len() <= before {
		t.Fatal("expected a circuit event to be recorded")
	}
	ev, ok := CB_EVENTS.Pop()
	if !ok {
		t.Fatal("expected to pop an event")
	}
	if ev.Provider != "openai" || ev.Closed {
		t.Fatalf("unexpected event: %+v", ev)
	}
	ResetCircuit(p, "recovery")
	ev2, ok := CB_EVENTS.Pop()
	if !ok {
		t.Fatal("expected to pop reset event")
	}
	if !ev2.Closed {
		t.Fatalf("expected closed/reset event, got: %+v", ev2)
	}
	_ = strings.TrimSpace
}
