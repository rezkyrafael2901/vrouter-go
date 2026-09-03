package provider

import (
	"sync"
	"time"
)

// CircuitBreakerConfig holds tunable circuit breaker behavior for a provider pool.
type CircuitBreakerConfig struct {
	FailThreshold       int
	LockSeconds         int
	HealthLockSeconds   int
}

var (
	// CBFailThreshold is the failure count that trips a provider circuit.
	CBFailThreshold = 3
	// CBLockSeconds is how long the circuit stays open once tripped.
	cbLockSeconds = 180
	CBLockSeconds = cbLockSeconds
	// CBHealthLockSeconds is the lock window used for health-check driven trips.
	CBHealthLockSeconds = 60
)

// CircuitEvent records a single circuit open / reset transition for a provider.
type CircuitEvent struct {
	Provider  string
	Reason    string
	Closed    bool // true => reset/closed, false => tripped/open
	Timestamp time.Time
}

// cbEventsGuard and CB_EVENTS form the rolling deque of circuit events.
var (
	cbEventsGuard sync.Mutex
	CB_EVENTS   = newCircuitEventDeque(200)
)

// circuitEventDeque is a fixed-capacity ring buffer (deque) for CircuitEvent.
type circuitEventDeque struct {
	buf  []CircuitEvent
	head int // write position
	tail int // read position
	size int
	cap  int
	mu   sync.Mutex
}

func newCircuitEventDeque(capacity int) *circuitEventDeque {
	if capacity < 1 {
		capacity = 200
	}
	return &circuitEventDeque{
		buf: make([]CircuitEvent, capacity),
		cap: capacity,
	}
}

func (d *circuitEventDeque) Push(e CircuitEvent) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.size == d.cap {
		// overwrite oldest (ring semantics)
		d.buf[d.tail] = e
		d.tail = (d.tail + 1) % d.cap
		d.head = (d.head + 1) % d.cap
	} else {
		d.buf[d.head] = e
		d.head = (d.head + 1) % d.cap
		d.size++
		if d.size == d.cap {
			d.tail = (d.tail + 1) % d.cap
		}
	}
}

// Pop removes and returns the oldest event.
func (d *circuitEventDeque) Pop() (CircuitEvent, bool) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.size == 0 {
		return CircuitEvent{}, false
	}
	e := d.buf[d.tail]
	d.tail = (d.tail + 1) % d.cap
	d.size--
	return e, true
}

// Len returns current event count.
func (d *circuitEventDeque) Len() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.size
}

// All returns a snapshot copy of all events in oldest-first order.
func (d *circuitEventDeque) All() []CircuitEvent {
	d.mu.Lock()
	defer d.mu.Unlock()
	out := make([]CircuitEvent, 0, d.size)
	for i := 0; i < d.size; i++ {
		idx := (d.tail + i) % d.cap
		out = append(out, d.buf[idx])
	}
	return out
}

// TripCircuit opens the circuit for provider p, locking it for lockSeconds.
func TripCircuit(p *Provider, reason string, lockSeconds int) {
	if p == nil {
		return
	}
	lockSecs := lockSeconds
	if lockSecs <= 0 {
		lockSecs = CBLockSeconds
	}

	now := time.Now()
	p.mu.Lock()
	p.LockedUntil = now.Add(time.Duration(lockSecs) * time.Second)
	p.Failures = 0
	p.mu.Unlock()

	event := CircuitEvent{
		Provider:  p.Name,
		Reason:    reason,
		Closed:    false,
		Timestamp: now,
	}
	cbEventsGuard.Lock()
	CB_EVENTS.Push(event)
	cbEventsGuard.Unlock()
}

// ResetCircuit closes the circuit for provider p after a recovery moment.
func ResetCircuit(p *Provider, reason string) {
	if p == nil {
		return
	}
	now := time.Now()
	p.mu.Lock()
	p.LockedUntil = time.Time{}
	p.Failures = 0
	p.mu.Unlock()

	event := CircuitEvent{
		Provider:  p.Name,
		Reason:    reason,
		Closed:    true,
		Timestamp: now,
	}
	cbEventsGuard.Lock()
	CB_EVENTS.Push(event)
	cbEventsGuard.Unlock()
}
