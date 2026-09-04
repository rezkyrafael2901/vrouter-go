package api

import (
	"sort"
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/rezkyrafael2901/vrouter-go/config"
	"github.com/rezkyrafael2901/vrouter-go/provider"
	"github.com/rezkyrafael2901/vrouter-go/stats"
)

type API struct {
	Config *config.Config
}

func NewAPI(cfg *config.Config) *API {
	return &API{Config: cfg}
}

func (a *API) Register(app *fiber.App) {
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
	api.Get("/providers", a.handleProviders)
	api.Post("/providers", a.handleAddProvider)
	api.Put("/providers/:name", a.handleUpdateProvider)
	api.Post("/providers/:name/fetch-models", a.handleFetchModels)
	api.Get("/combos", a.handleCombos)
	api.Post("/login", a.handleLogin)
	api.Post("/logout", a.handleLogout)
	api.Get("/history", a.handleHistory)
	api.Delete("/history", a.handleClearHistory)
	api.Get("/costs", a.handleCosts)
	api.Get("/proxies", a.handleProxies)
	api.Post("/proxies", a.handleAddProxy)
	api.Put("/proxies/:name", a.handleUpdateProxy)
	api.Delete("/proxies/:name", a.handleDeleteProxy)
	api.Post("/health-check", a.handleHealthCheckTrigger)
	api.Post("/refresh-all-models", a.handleRefreshAllModels)
}
func (a *API) handleStatus(c *fiber.Ctx) error {

	totalReqs := int64(0)
	totalErrs := int64(0)
	healthy := 0
	unhealthy := 0

	// Build providers array inline
	type provResp struct {
		Name      string   `json:"name"`
		ApiBase   string   `json:"base_url"`
		Prefix    string   `json:"prefix"`
		Models    []string `json:"models"`
		Default   string   `json:"default_model"`
		Weight    int      `json:"weight"`
		IsActive  bool     `json:"is_active"`
		ApiType   string   `json:"type"`
		KeysCount   int      `json:"key_count"`
		ModelsCount int      `json:"models_count"`
		Failures    int      `json:"failures"`
		Locked      bool     `json:"locked"`
		Healthy     bool     `json:"healthy"`
		Proxy       string   `json:"proxy"`
	}
	providers := provider.All()
	provs := make([]provResp, 0, len(providers))
	// Compute totals from stats (router updates these, not provider.TotalRequests)
	allStats := stats.AllModelStats()
	for _, s := range allStats {
		totalReqs += int64(s.Total)
		totalErrs += int64(s.Err)
	}
	for _, p := range providers {
		if p.IsHealthy() {
			healthy++
		} else {
			unhealthy++
		}
		provs = append(provs, provResp{
			Name: p.Name, ApiBase: p.ApiBase, Prefix: p.Prefix, Models: p.Models,
			Default: p.DefaultModel, Weight: p.Weight, IsActive: p.IsActive, ApiType: p.ApiType,
			KeysCount: len(p.Keys), ModelsCount: len(p.Models), Failures: p.Failures,
			Locked: p.IsLocked(), Healthy: p.IsHealthy(), Proxy: p.Proxy,
		})
	}

	// Build combos array inline
	type routeResp struct {
		Provider string `json:"provider"`
		Model    string `json:"model"`
		Weight   int    `json:"weight"`
	}
	type comboResp struct {
		Name     string      `json:"name"`
		Strategy string      `json:"strategy"`
		Routes   []routeResp `json:"routes"`
	}
	combos := make([]comboResp, 0, len(a.Config.Combos))
	for _, combo := range a.Config.Combos {
		routes := make([]routeResp, 0, len(combo.Routes))
		for _, r := range combo.Routes {
			routes = append(routes, routeResp{Provider: r.Provider, Model: r.Model, Weight: int(r.Weight)})
		}
		combos = append(combos, comboResp{Name: combo.Name, Strategy: combo.Strategy, Routes: routes})
	}


	// Compute aggregate stats from history
	aggrOk := int64(0)
	aggrIn := int64(0)
	aggrOut := int64(0)
	history := stats.AllHistory()
	for _, h := range history {
		if h.Status == "ok" {
			aggrOk++
		}
		aggrIn += int64(h.PromptTokens)
		aggrOut += int64(h.CompletionTokens)
	}
	successRate := 100.0
	if totalReqs > 0 {
		successRate = float64(totalReqs-totalErrs) / float64(totalReqs) * 100
	}

	return c.JSON(fiber.Map{
		"ok":                    true,
		"ts":                    time.Now().Unix(),
		"version":               "1.0.0-go",
		"total_providers":       len(providers),
		"total_requests":        totalReqs,
		"total_errors":          totalErrs,
		"healthy_providers":     healthy,
		"unhealthy_providers":   unhealthy,
		"model_stats_count":     stats.ModelStatsCount(),
		"throughput_count":      stats.ThroughputCount(),
		"dead_models_count":     stats.DeadModelCount(),
		"providers":             provs,
		"combos":                combos,
		"proxies":               []string{},
		"history":               history,
		"success_rate":           successRate,
		"agg": map[string]interface{}{
			"total": map[string]interface{}{
				"ok":   aggrOk,
				"in":   aggrIn,
				"out":  aggrOut,
				"cost": 0.0,
			},
			"today": map[string]interface{}{
				"ok":   aggrOk,
				"in":   aggrIn,
				"out":  aggrOut,
				"cost": 0.0,
			},
		},
	})
}

func (a *API) handleModelStats(c *fiber.Ctx) error {
	rows := stats.AllModelStats()
	type row struct {
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
	result := make([]row, 0, len(rows))
	for _, s := range rows {
		avgMs := 0.0
		sr := 0.0
		if s.Total > 0 {
			avgMs = float64(s.LatencySum) / float64(s.Total)
			sr = float64(s.Ok) / float64(s.Total) * 100
		}
		result = append(result, row{
			Model: s.Model, Total: s.Total, Ok: s.Ok, Err: s.Err,
			AvgMs: avgMs, MinMs: s.LatencyMin, MaxMs: s.LatencyMax,
			SuccessRate: sr, Reasoning: s.Reasoning, LastError: s.LastError,
			LastUsed: s.LastUsed.Unix(), EmaLatency: s.EmaLatencyMs,
			EmaSuccess: s.EmaSuccess, Samples: s.Samples,
		})
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Total > result[j].Total })
	return c.JSON(fiber.Map{"stats": result, "total_models": len(result)})
}

func (a *API) handleHealthScore(c *fiber.Ctx) error {
	type row struct {
		Model string  `json:"model"`
		Score float64 `json:"score"`
	}
	rows := stats.AllModelStats()
	result := make([]row, 0, len(rows))
	for _, s := range rows {
		result = append(result, row{Model: s.Model, Score: stats.HealthScore(s.Model)})
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Score > result[j].Score })
	return c.JSON(fiber.Map{"scores": result, "count": len(result)})
}

func (a *API) handleCircuitBreakers(c *fiber.Ctx) error {
	type bRow struct {
		Provider    string `json:"provider"`
		State       string `json:"state"`
		Locked      bool   `json:"locked"`
		RemainingS  int    `json:"remaining_s"`
		Failures    int    `json:"failures"`
		Threshold   int    `json:"threshold"`
		LastError   string `json:"last_error"`
		TotalReqs   int    `json:"total_requests"`
		TotalErrors int    `json:"total_errors"`
		IsActive    bool   `json:"is_active"`
	}
	now := time.Now()
	rows := make([]bRow, 0)
	openCount := 0
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
			openCount++
		} else if p.Failures > 0 {
			state = "half_open"
		}
		rows = append(rows, bRow{
			Provider: p.Name, State: state, Locked: locked, RemainingS: remaining,
			Failures: p.Failures, Threshold: a.Config.Router.CBFailThreshold,
			LastError: p.LastError, TotalReqs: p.TotalRequests,
			TotalErrors: p.TotalErrors, IsActive: p.IsActive,
		})
	}
	return c.JSON(fiber.Map{"ok": true, "ts": now.Unix(), "open_count": openCount, "breakers": rows})
}

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

func (a *API) handleResetBreaker(c *fiber.Ctx) error {
	name := c.Params("name")
	p := provider.Get(name)
	if p == nil {
		return fiber.ErrNotFound
	}
	provider.ResetCircuit(p, "manual dashboard reset")
	return c.JSON(fiber.Map{"ok": true, "provider": name, "state": "closed"})
}

func (a *API) handleThroughput(c *fiber.Ctx) error {
	rows := stats.AllThroughputStats()
	type tpRow struct {
		Provider      string  `json:"provider"`
		Model         string  `json:"model"`
		Samples       int     `json:"samples"`
		TokPerSec     float64 `json:"tok_s"`
		BestTokPerSec float64 `json:"best_tok_s"`
		TtftMs        float64 `json:"ttft_ms"`
		TokTotal      int     `json:"tok_total"`
		AgeS          int     `json:"age_s"`
	}
	result := make([]tpRow, 0, len(rows))
	now := time.Now()
	for _, s := range rows {
		ageS := 0
		if !s.LastUsed.IsZero() {
			ageS = int(now.Sub(s.LastUsed).Seconds())
		}
		result = append(result, tpRow{
			Provider: s.Provider, Model: s.Model, Samples: s.Samples,
			TokPerSec: s.ToksEma, BestTokPerSec: s.BestToks,
			TtftMs: s.TtftEma, TokTotal: s.TokTotal, AgeS: ageS,
		})
	}
	sort.Slice(result, func(i, j int) bool { return result[i].TokPerSec > result[j].TokPerSec })
	return c.JSON(fiber.Map{"ok": true, "ts": now.Unix(), "count": len(result), "models": result})
}

func (a *API) handleResetThroughput(c *fiber.Ctx) error {
	stats.ResetThroughput()
	return c.JSON(fiber.Map{"ok": true})
}

func (a *API) handleDeadModels(c *fiber.Ctx) error {
	dead := stats.AllDeadModels()
	type dRow struct {
		Model      string `json:"model"`
		Failures   int    `json:"failures"`
		LastError  string `json:"last_error"`
		DisabledAt int64  `json:"disabled_at"`
	}
	result := make([]dRow, 0, len(dead))
	for k, d := range dead {
		result = append(result, dRow{Model: k, Failures: d.Failures, LastError: d.LastError, DisabledAt: d.DisabledAt.Unix()})
	}
	return c.JSON(fiber.Map{"dead_models": result, "total": len(result)})
}

func (a *API) handleProviders(c *fiber.Ctx) error {
	type provResp struct {
		Name        string   `json:"name"`
		ApiBase     string   `json:"base_url"`
		Prefix      string   `json:"prefix"`
		Models      []string `json:"models"`
		Default     string   `json:"default_model"`
		Weight      int      `json:"weight"`
		IsActive    bool     `json:"is_active"`
		ApiType     string   `json:"type"`
		KeysCount   int      `json:"key_count"`
		ModelsCount int      `json:"models_count"`
		Failures    int      `json:"failures"`
		Locked      bool     `json:"locked"`
		Healthy     bool     `json:"healthy"`
		Proxy       string   `json:"proxy"`
	}
	providers := provider.All()
	result := make([]provResp, 0, len(providers))
	for _, p := range providers {
		result = append(result, provResp{
			Name: p.Name, ApiBase: p.ApiBase, Prefix: p.Prefix, Models: p.Models,
			Default: p.DefaultModel, Weight: p.Weight, IsActive: p.IsActive, ApiType: p.ApiType,
			KeysCount: len(p.Keys), ModelsCount: len(p.Models), Failures: p.Failures,
			Locked: p.IsLocked(), Healthy: p.IsHealthy(), Proxy: p.Proxy,
		})
	}
	return c.JSON(fiber.Map{"providers": result})
}

func (a *API) handleCombos(c *fiber.Ctx) error {
	type routeResp struct {
		Provider string `json:"provider"`
		Model    string `json:"model"`
		Weight   int    `json:"weight"`
	}
	type comboResp struct {
		Name     string      `json:"name"`
		Strategy string      `json:"strategy"`
		Routes   []routeResp `json:"routes"`
	}
	result := make([]comboResp, 0, len(a.Config.Combos))
	for _, combo := range a.Config.Combos {
		routes := make([]routeResp, 0, len(combo.Routes))
		for _, r := range combo.Routes {
			routes = append(routes, routeResp{Provider: r.Provider, Model: r.Model, Weight: int(r.Weight)})
		}
		result = append(result, comboResp{Name: combo.Name, Strategy: combo.Strategy, Routes: routes})
	}
	return c.JSON(fiber.Map{"combos": result})
}

func (a *API) handleLogin(c *fiber.Ctx) error {
	return c.JSON(fiber.Map{"ok": true, "token": "go-vrouter"})
}

func (a *API) handleLogout(c *fiber.Ctx) error {
	return c.JSON(fiber.Map{"ok": true})
}

func (a *API) handleHistory(c *fiber.Ctx) error {
	history := stats.AllHistory()
	return c.JSON(fiber.Map{"history": history, "total": len(history)})
}

func (a *API) handleClearHistory(c *fiber.Ctx) error {
	stats.ClearHistory()
	return c.JSON(fiber.Map{"ok": true, "cleared": true})
}

func (a *API) handleCosts(c *fiber.Ctx) error {
	return c.JSON(fiber.Map{"costs": map[string]interface{}{}, "total": 0.0})
}

func (a *API) handleProxies(c *fiber.Ctx) error {
	return c.JSON(fiber.Map{"proxies": []string{}})
}

func (a *API) handleAddProxy(c *fiber.Ctx) error {
	return c.JSON(fiber.Map{"ok": true})
}

func (a *API) handleUpdateProxy(c *fiber.Ctx) error {
	return c.JSON(fiber.Map{"ok": true})
}

func (a *API) handleDeleteProxy(c *fiber.Ctx) error {
	return c.JSON(fiber.Map{"ok": true})
}

func (a *API) handleAddProvider(c *fiber.Ctx) error {
	return c.JSON(fiber.Map{"ok": true, "message": "restart required"})
}

func (a *API) handleUpdateProvider(c *fiber.Ctx) error {
	name := c.Params("name")
	return c.JSON(fiber.Map{"ok": true, "provider": name})
}

func (a *API) handleFetchModels(c *fiber.Ctx) error {
	name := c.Params("name")
	return c.JSON(fiber.Map{"ok": true, "provider": name, "models": []string{}})
}

func (a *API) handleHealthCheckTrigger(c *fiber.Ctx) error {
	checked := 0
	for _, p := range provider.All() {
		if p.IsActive && p.HasKeys() {
			checked++
		}
	}
	return c.JSON(fiber.Map{"ok": true, "checked": checked})
}

func (a *API) handleRefreshAllModels(c *fiber.Ctx) error {
	return c.JSON(fiber.Map{"ok": true})
}
