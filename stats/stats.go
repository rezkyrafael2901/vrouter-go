package stats

import (
	"encoding/json"
	"math"
	"os"
	"sync"
	"time"

	"github.com/rezkyrafael2901/vrouter-go/config"
)

// ---------------------------------------------------------------------------
// Constants
// ---------------------------------------------------------------------------

// EMA decay factor for latency and success rate smoothing.
const emaAlpha = 0.3

// ModelIDKey builds the canonical composite key for a provider/model pair.
func ModelIDKey(provider, model string) string {
	return provider + ":" + model
}

// ---------------------------------------------------------------------------
// Structs
// ---------------------------------------------------------------------------

// ModelStats tracks per-model reliability and latency metrics.
type ModelStats struct {
	Model      string    `json:"model"`
	Provider   string    `json:"provider"`
	Total      int       `json:"total"`
	Ok         int       `json:"ok"`
	Err        int       `json:"err"`
	LatencySum float64   `json:"latency_sum"`
	LatencyMin float64   `json:"latency_min"`
	LatencyMax float64   `json:"latency_max"`
	Reasoning  bool      `json:"reasoning"`
	LastError  string    `json:"last_error"`
	LastUsed   time.Time `json:"last_used"`
	EmaLatencyMs float64 `json:"ema_latency_ms"`
	EmaSuccess  float64 `json:"ema_success"`
	Samples     int     `json:"samples"`
}

// ThroughputStats tracks throughput efficiency metrics per provider/model.
type ThroughputStats struct {
	Provider  string    `json:"provider"`
	Model     string    `json:"model"`
	Samples   int       `json:"samples"`
	TtftEma   float64   `json:"ttft_ema"`
	ToksEma   float64   `json:"toks_ema"`
	TtftLast  float64   `json:"ttft_last"`
	ToksLast  float64   `json:"toks_last"`
	BestToks  float64   `json:"best_toks"`
	TokTotal  int       `json:"tok_total"`
	LastUsed  time.Time `json:"last_used"`
}

// DeadModel tracks a disabled / failing model.
type DeadModel struct {
	Failures    int       `json:"failures"`
	LastChecked time.Time `json:"last_checked"`
	LastError   string    `json:"last_error"`
	DisabledAt  time.Time `json:"disabled_at"`
}

// ---------------------------------------------------------------------------
// Global state
// ---------------------------------------------------------------------------

var (
	mu sync.RWMutex

	MODEL_STATS     = make(map[string]*ModelStats)
	THROUGHPUT_STATS = make(map[string]*ThroughputStats)
	DEAD_MODELS     = make(map[string]*DeadModel)
)

// ---------------------------------------------------------------------------
// Recording functions
// ---------------------------------------------------------------------------

// RecordModelResult records the outcome of a model invocation.
func RecordModelResult(provider, model string, latencyMs float64, success bool) {
	key := ModelIDKey(provider, model)
	now := time.Now()

	mu.Lock()
	defer mu.Unlock()

	s, ok := MODEL_STATS[key]
	if !ok {
		s = &ModelStats{
			Model:         model,
			Provider:      provider,
			LatencyMin:    latencyMs,
			LatencyMax:    latencyMs,
			EmaLatencyMs:  latencyMs,
			EmaSuccess:    1.0,
		}
		MODEL_STATS[key] = s
	}

	s.Total++
	if success {
		s.Ok++
	} else {
		s.Err++
		s.LastError = "error"
	}
	s.LatencySum += latencyMs
	if latencyMs < s.LatencyMin {
		s.LatencyMin = latencyMs
	}
	if latencyMs > s.LatencyMax {
		s.LatencyMax = latencyMs
	}
	s.LastUsed = now
	s.Samples++

	// EMA for latency.
	s.EmaLatencyMs = ema(s.EmaLatencyMs, latencyMs, emaAlpha)

	// EMA for success (0/1 sample).
	var successVal float64
	if success {
		successVal = 1.0
	}
	s.EmaSuccess = ema(s.EmaSuccess, successVal, emaAlpha)
}

// RecordThroughput records throughput metrics for a provider/model pair.
func RecordThroughput(provider, model string, ttftMs, tokS float64, cTok int) {
	key := ModelIDKey(provider, model)
	now := time.Now()

	mu.Lock()
	defer mu.Unlock()

	t, ok := THROUGHPUT_STATS[key]
	if !ok {
		t = &ThroughputStats{
			Provider: provider,
			Model:    model,
			TokTotal: cTok,
			BestToks: tokS,
		}
		THROUGHPUT_STATS[key] = t
	}

	t.Samples++
	t.TtftLast = ttftMs
	t.ToksLast = tokS
	t.TokTotal += cTok
	t.LastUsed = now

	// EMA for ttft and tokens/sec.
	t.TtftEma = ema(t.TtftEma, ttftMs, emaAlpha)
	t.ToksEma = ema(t.ToksEma, tokS, emaAlpha)

	if tokS > t.BestToks {
		t.BestToks = tokS
	}
}

// MarkModelDead records that a model has failed and is now disabled.
func MarkModelDead(provider, model, reason string) {
	key := ModelIDKey(provider, model)
	now := time.Now()

	mu.Lock()
	defer mu.Unlock()

	d, ok := DEAD_MODELS[key]
	if !ok {
		d = &DeadModel{}
		DEAD_MODELS[key] = d
	}
	d.Failures++
	d.LastChecked = now
	d.LastError = reason
	if d.DisabledAt.IsZero() {
		d.DisabledAt = now
	}
}

// IsModelDead reports whether the given provider/model is marked dead.
func IsModelDead(provider, model string) bool {
	key := ModelIDKey(provider, model)

	mu.RLock()
	defer mu.RUnlock()

	d, ok := DEAD_MODELS[key]
	if !ok {
		return false
	}
	return d.DisabledAt.After(d.LastChecked) || d.Failures >= 3 || !d.DisabledAt.IsZero()
}

// ---------------------------------------------------------------------------
// Health score
// ---------------------------------------------------------------------------

// HealthScore computes a composite health score (0-1) for a model.
// Weights: reliability 45%, latency 25%, throughput 20%, freshness 10%.
func HealthScore(modelID string) float64 {
	mu.RLock()
	defer mu.RUnlock()

	s, ok := MODEL_STATS[modelID]
	if !ok {
		return 0.0
	}

	// Reliability: EMA success rate (0-1).
	reliability := clamp(s.EmaSuccess, 0.0, 1.0)

	// Latency: inverse-normalized against observed min/max.
	latencyScore := 1.0
	if s.LatencyMax > s.LatencyMin && s.EmaLatencyMs > 0 {
		latencyScore = 1.0 - (s.EmaLatencyMs-s.LatencyMin)/(s.LatencyMax-s.LatencyMin)
	}
	latencyScore = clamp(latencyScore, 0.0, 1.0)

	// Throughput: tokens/sec EMA normalized.
	throughputScore := 0.5
	if t, tokOK := THROUGHPUT_STATS[modelID]; tokOK {
		throughputScore = clamp(t.ToksEma/t.BestToks, 0.0, 1.0)
	}

	// Freshness: recent usage decays gracefully.
	freshness := 0.0
	if !s.LastUsed.IsZero() {
		age := time.Since(s.LastUsed).Seconds()
		freshness = math.Exp(-age / 300.0) // 5-minute half-life-ish
	}

	score := 0.45*reliability +
		0.25*latencyScore +
		0.20*throughputScore +
		0.10*freshness

	return clamp(score, 0.0, 1.0)
}

// ---------------------------------------------------------------------------
// Persistence
// ---------------------------------------------------------------------------

// stateFile returns the configured path for the state file.
func stateFile() string {
	return config.RouterStatePath
}

// Persist writes the current in-memory stats state to disk as JSON.
func Persist() error {
	mu.RLock()
	defer mu.RUnlock()

	payload := struct {
		ModelStats     map[string]*ModelStats     `json:"model_stats"`
		ThroughputStats map[string]*ThroughputStats `json:"throughput_stats"`
		DeadModels     map[string]*DeadModel      `json:"dead_models"`
	}{
		ModelStats:     MODEL_STATS,
		ThroughputStats: THROUGHPUT_STATS,
		DeadModels:     DEAD_MODELS,
	}

	data, err := json.MarshalIndent(payload, "", "  ")
	if err != nil {
		return err
	}

	tmp := stateFile() + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, stateFile())
}

// Load restores stats state from disk. Merges into any existing in-memory
// entries (in-memory wins for collisions is not enforced; loaded data
// replaces the maps).
func Load() error {
	data, err := os.ReadFile(stateFile())
	if err != nil {
		return err
	}

	var payload struct {
		ModelStats     map[string]*ModelStats     `json:"model_stats"`
		ThroughputStats map[string]*ThroughputStats `json:"throughput_stats"`
		DeadModels     map[string]*DeadModel      `json:"dead_models"`
	}
	if err := json.Unmarshal(data, &payload); err != nil {
		return err
	}

	mu.Lock()
	defer mu.Unlock()

	MODEL_STATS = payload.ModelStats
	THROUGHPUT_STATS = payload.ThroughputStats
	DEAD_MODELS = payload.DeadModels

	return nil
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// ema computes the exponential moving average.
func ema(prev, sample, alpha float64) float64 {
	return alpha*sample + (1.0-alpha)*prev
}

func clamp(v, lo, hi float64) float64 {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

// ---------------------------------------------------------------------------
// Accessor functions (used by API handlers)
// ---------------------------------------------------------------------------

// AllModelStats returns a snapshot of all model stats.
func AllModelStats() []*ModelStats {
	mu.RLock()
	defer mu.RUnlock()
	out := make([]*ModelStats, 0, len(MODEL_STATS))
	for _, s := range MODEL_STATS {
		out = append(out, s)
	}
	return out
}

// AllThroughputStats returns a snapshot of all throughput stats.
func AllThroughputStats() []*ThroughputStats {
	mu.RLock()
	defer mu.RUnlock()
	out := make([]*ThroughputStats, 0, len(THROUGHPUT_STATS))
	for _, s := range THROUGHPUT_STATS {
		out = append(out, s)
	}
	return out
}

// DeadModelInfo is the public view of a dead model entry.
type DeadModelInfo struct {
	Failures   int
	LastError  string
	DisabledAt time.Time
}

// AllDeadModels returns a snapshot of all dead models.
func AllDeadModels() map[string]*DeadModelInfo {
	mu.RLock()
	defer mu.RUnlock()
	out := make(map[string]*DeadModelInfo, len(DEAD_MODELS))
	for k, d := range DEAD_MODELS {
		out[k] = &DeadModelInfo{
			Failures:   d.Failures,
			LastError:  d.LastError,
			DisabledAt: d.DisabledAt,
		}
	}
	return out
}

// ModelStatsCount returns the number of tracked models.
func ModelStatsCount() int {
	mu.RLock()
	defer mu.RUnlock()
	return len(MODEL_STATS)
}

// ThroughputCount returns the number of tracked throughput entries.
func ThroughputCount() int {
	mu.RLock()
	defer mu.RUnlock()
	return len(THROUGHPUT_STATS)
}

// DeadModelCount returns the number of dead models.
func DeadModelCount() int {
	mu.RLock()
	defer mu.RUnlock()
	return len(DEAD_MODELS)
}

// ResetThroughput clears all throughput stats.
func ResetThroughput() {
	mu.Lock()
	defer mu.Unlock()
	THROUGHPUT_STATS = make(map[string]*ThroughputStats)
}
