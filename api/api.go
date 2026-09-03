package api

import (
	"bufio"
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/rezkyrafael2901/vrouter-go/config"
	"github.com/rezkyrafael2901/vrouter-go/provider"
	"github.com/rezkyrafael2901/vrouter-go/stats"
)

// API holds references for all dashboard/API endpoints.
type API struct {
	Config  *config.Config
	Started time.Time
}

// NewAPI creates an API handler set.
func NewAPI(cfg *config.Config) *API {
	return &API{Config: cfg, Started: time.Now()}
}

// Register mounts all /api/* routes on the given app.
func (a *API) Register(app *fiber.App) {
	// Dashboard endpoints
	api := app.Group("/api")
	api.Get("/status", a.handleStatus)
	api.Get("/model-stats", a.handleModelStats)
	api.Get("/health-score", a.handleHealthScore)
	api.Get("/circuit-breakers", a.handleCircuitBreakers)
	api.Post("/circuit-breakers/reset-all", a.handleResetAllBreakers)
	api.Post("/circuit-breakers/:name/reset", a.handleResetBreaker)
	api.Get("/throughput", a.handleThroughput)
	api.Post("/throughput/reset", a.handleResetThroughput)
	api.Get("/dead-models", a.handleDeadModels)
}

// GET /api/status — full system status
func (a *API) handleStatus(c *fiber.Ctx) error {
	totalReqs := int64(0)
	totalErrs := int64(0)
	healthy := 0
	unhealthy := 0

	for _, p := range provider.All() {
		totalReqs += int64(p.TotalRequests)
		totalErrs += int64(p.TotalErrors)
		if p.IsHealthy() {
			healthy++
		} else {
			unhealthy++
		}
	}

	modelStatsCount := stats.ModelStatsCount()
	throughputCount := stats.ThroughputCount()
	deadCount := stats.DeadModelCount()

	return c.JSON(fiber.Map{
		"ok":                   true,
		"ts":                   time.Now().Unix(),
		"version":              "1.0.0-go",
		"total_providers":      len(provider.All()),
		"total_requests":       totalReqs,
		"total_errors":         totalErrs,
		"healthy_providers":    healthy,
		"unhealthy_providers":  unhealthy,
		"model_stats_count":    modelStatsCount,
		"throughput_count":     throughputCount,
		"dead_models_count":    deadCount,
	})
}

// GET /api/model-stats — per-model performance
func (a *API) handleModelStats(c *fiber.Ctx) error {
	rows := stats.AllModelStats()
	type statRow struct {
		Model       string  `json:"model"`
		Total       int     `json:"total"`
		Ok          int     `json:"ok"`
		Err         int     `json:"err"`
		AvgMs       float64 `json:"avg_ms"`
		MinMs       float64 `json:"min_ms"`
		MaxMs       float64 `json:"max_ms"`
		SuccessRate float64 `json:"success_rate"`
		Reasoning   bool    `json:"reasoning"`
		LastError   string  `json:"last_error"`
		LastUsed    int64   `json:"last_used"`
		EmaLatency  float64 `json:"ema_latency_ms"`
		EmaSuccess  float64 `json:"ema_success"`
		Samples     int     `json:"samples"`
	}

	result := make([]statRow, 0, len(rows))
	for _, s := range rows {
		avgMs := 0.0
		if s.Total > 0 {
			avgMs = float64(s.LatencySum) / float64(s.Total)
		}
		successRate := 0.0
		if s.Total > 0 {
			successRate = float64(s.Ok) / float64(s.Total) * 100
		}
		minMs := s.LatencyMin
		if minMs >= 999999 {
			minMs = 0
		}
		result = append(result, statRow{
			Model:       s.Model,
			Total:       s.Total,
			Ok:          s.Ok,
			Err:         s.Err,
			AvgMs:       avgMs,
			MinMs:       minMs,
			MaxMs:       s.LatencyMax,
			SuccessRate: successRate,
			Reasoning:   s.Reasoning,
			LastError:   s.LastError,
			LastUsed:    s.LastUsed.Unix(),
			EmaLatency:  s.EmaLatencyMs,
			EmaSuccess:  s.EmaSuccess,
			Samples:     s.Samples,
		})
	}

	sort.Slice(result, func(i, j int) bool {
		return result[i].Total > result[j].Total
	})

	return c.JSON(fiber.Map{"stats": result, "total_models": len(result)})
}

// GET /api/health-score — composite health score per model
func (a *API) handleHealthScore(c *fiber.Ctx) error {
	rows := stats.AllModelStats()
	type hsRow struct {
		Model        string  `json:"model"`
		Score        float64 `json:"score"`
		Reliability  float64 `json:"reliability"`
		Latency      float64 `json:"latency"`
		Throughput   float64 `json:"throughput"`
		Freshness    float64 `json:"freshness"`
		Total        int     `json:"total"`
		TokPerSec    float64 `json:"tok_s"`
	}

	result := make([]hsRow, 0, len(rows))
	for _, s := range rows {
		score := stats.HealthScore(s.Model)
		result = append(result, hsRow{
			Model:       s.Model,
			Score:       score,
			Reliability: s.EmaSuccess * 100,
			Total:       s.Total,
		})
	}

	sort.Slice(result, func(i, j int) bool {
		return result[i].Score > result[j].Score
	})

	return c.JSON(fiber.Map{"scores": result, "count": len(result)})
}

// GET /api/circuit-breakers — circuit breaker states + events
func (a *API) handleCircuitBreakers(c *fiber.Ctx) error {
	type breakerRow struct {
		Provider    string `json:"provider"`
		State       string `json:"state"`
		Locked      bool   `json:"locked"`
		RemainingS  int    `json:"remaining_s"`
		Failures    int    `json:"failures"`
		Threshold   int    `json:"threshold"`
		LastError   string `json:"last_error"`
		TotalReqs   int64  `json:"total_requests"`
		TotalErrors int64  `json:"total_errors"`
		IsActive    bool   `json:"is_active"`
	}

	now := time.Now()
	rows := make([]breakerRow, 0)
	for _, p := range provider.All() {
		locked := p.IsLocked()
		state := "closed"
		remaining := 0
		if locked {
			state = "open"
			remaining = int(p.LockedUntil.Sub(now).Seconds())
			if remaining < 0 {
				remaining = 0
			}
		} else if p.Failures > 0 {
			state = "half_open"
		}
		rows = append(rows, breakerRow{
			Provider:    p.Name,
			State:       state,
			Locked:      locked,
			RemainingS:  remaining,
			Failures:    p.Failures,
			Threshold:   a.Config.Router.CBFailThreshold,
			LastError:   p.LastError,
			TotalReqs:   int64(p.TotalRequests),
			TotalErrors: int64(p.TotalErrors),
			IsActive:    p.IsActive,
		})
	}

	openCount := 0
	for _, r := range rows {
		if r.Locked {
			openCount++
		}
	}

	events := provider.CircuitEvents()
	return c.JSON(fiber.Map{
		"ok":         true,
		"ts":         now.Unix(),
		"open_count": openCount,
		"breakers":   rows,
		"events":     events,
	})
}

// POST /api/circuit-breakers/reset-all
func (a *API) handleResetAllBreakers(c *fiber.Ctx) error {
	n := 0
	for _, p := range provider.All() {
		if p.IsLocked() || p.Failures > 0 {
			provider.ResetCircuit(p, "manual reset-all")
			n++
		}
	}
	return c.JSON(fiber.Map{"ok": true, "reset": n})
}

// POST /api/circuit-breakers/:name/reset
func (a *API) handleResetBreaker(c *fiber.Ctx) error {
	name := c.Params("name")
	p := provider.Get(name)
	if p == nil {
		return fiber.ErrNotFound
	}
	provider.ResetCircuit(p, "manual dashboard reset")
	return c.JSON(fiber.Map{"ok": true, "provider": name, "state": "closed"})
}

// GET /api/throughput — tok/s meter
func (a *API) handleThroughput(c *fiber.Ctx) error {
	rows := stats.AllThroughputStats()
	type tpRow struct {
		Provider   string  `json:"provider"`
		Model      string  `json:"model"`
		Samples    int     `json:"samples"`
		TokPerSec  float64 `json:"tok_s"`
		BestTokPerSec float64 `json:"best_tok_s"`
		TtftMs     float64 `json:"ttft_ms"`
		TokTotal   int     `json:"tok_total"`
		AgeS       int     `json:"age_s"`
	}

	result := make([]tpRow, 0, len(rows))
	now := time.Now()
	for _, s := range rows {
		ageS := 0
		if !s.LastUsed.IsZero() {
			ageS = int(now.Sub(s.LastUsed).Seconds())
		}
		result = append(result, tpRow{
			Provider:      s.Provider,
			Model:         s.Model,
			Samples:       s.Samples,
			TokPerSec:     s.ToksEma,
			BestTokPerSec: s.BestToks,
			TtftMs:        s.TtftEma,
			TokTotal:      s.TokTotal,
			AgeS:          ageS,
		})
	}

	sort.Slice(result, func(i, j int) bool {
		return result[i].TokPerSec > result[j].TokPerSec
	})

	return c.JSON(fiber.Map{"ok": true, "ts": now.Unix(), "count": len(result), "models": result})
}

// POST /api/throughput/reset
func (a *API) handleResetThroughput(c *fiber.Ctx) error {
	stats.ResetThroughput()
	return c.JSON(fiber.Map{"ok": true})
}

// GET /api/dead-models
func (a *API) handleDeadModels(c *fiber.Ctx) error {
	dead := stats.AllDeadModels()
	type deadRow struct {
		Model      string `json:"model"`
		Failures   int    `json:"failures"`
		LastError  string `json:"last_error"`
		DisabledAt int64  `json:"disabled_at"`
	}
	result := make([]deadRow, 0, len(dead))
	for k, d := range dead {
		result = append(result, deadRow{
			Model:      k,
			Failures:   d.Failures,
			LastError:  d.LastError,
			DisabledAt: d.DisabledAt.Unix(),
		})
	}
	return c.JSON(fiber.Map{"dead_models": result, "total": len(result)})
}

// SSE helper — not used by Fiber directly but available for upstream streaming
type Flusher interface {
	Flush()
}

// ParseSSELine parses a single SSE line from upstream
func ParseSSELine(line string) (string, interface{}, error) {
	if !strings.HasPrefix(line, "data: ") {
		return "", nil, nil
	}
	data := strings.TrimPrefix(line, "data: ")
	if data == "[DONE]" {
		return "DONE", nil, nil
	}
	// Parse JSON chunk
	return "chunk", data, nil
}

// ReadSSEChunks reads SSE chunks from a reader and sends via channel
func ReadSSEChunks(ctx context.Context, reader *bufio.Scanner, ch chan<- string) {
	defer close(ch)
	for reader.Scan() {
		line := reader.Text()
		if strings.HasPrefix(line, "data: ") {
			ch <- line
		}
	}
}

// FormatModelStats formats a model stats row for display
func FormatModelStats(s *stats.ModelStats) string {
	avgMs := 0.0
	if s.Total > 0 {
		avgMs = float64(s.LatencySum) / float64(s.Total)
	}
	sr := 0.0
	if s.Total > 0 {
		sr = float64(s.Ok) / float64(s.Total) * 100
	}
	return fmt.Sprintf("%s: %d reqs, %.1f%% success, %.0fms avg",
		s.Model, s.Total, sr, avgMs)
}
