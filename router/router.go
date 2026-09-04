package router

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math/rand"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/rezkyrafael2901/vrouter-go/config"
	"github.com/rezkyrafael2901/vrouter-go/provider"
	"github.com/rezkyrafael2901/vrouter-go/stats"
)

const (
	TTFTTimeout      = 5 * time.Second  // pre-first-token failover window
	StreamIdleTimeout = 15 * time.Second // max wait for upstream response before failover
)

// ---------------------------------------------------------------------------
// Router struct
// ---------------------------------------------------------------------------

// Router holds the routing engine: provider pool, combos, and config.
type Router struct {
	Providers map[string]*provider.Provider // keyed by provider name
	Combos    map[string]*config.Combo      // keyed by combo name
	Config    *config.Config
	mu        sync.RWMutex
}

// NewRouter builds a Router from the loaded config.
func NewRouter(cfg *config.Config) *Router {
	r := &Router{
		Providers: make(map[string]*provider.Provider),
		Combos:    make(map[string]*config.Combo),
		Config:    cfg,
	}

	for _, pc := range cfg.Providers {
		p := &provider.Provider{
			Name:         pc.Name,
			ApiBase:      pc.ApiBase,
			Keys:         pc.Keys,
			Prefix:       pc.Prefix,
			Models:       pc.Models,
			DefaultModel: pc.DefaultModel,
			Weight:       pc.Weight,
			Proxy:        pc.Proxy,
			KeepPrefix:   pc.KeepPrefix,
			IsActive:     pc.IsActive,
			ApiType:      pc.ApiType,
		}
		r.Providers[pc.Name] = p
	}

	for i := range cfg.Combos {
		combo := &cfg.Combos[i]
		r.Combos[combo.Name] = combo
	}

	return r
}

// ---------------------------------------------------------------------------

// logReq logs a request to the history ring buffer.
func logReq(c *fiber.Ctx, model, provider, status, errMsg string, latencyMs float64, promptTokens, completionTokens int) {
	ip := c.IP()
	stats.LogHistory(model, provider, status, errMsg, ip, latencyMs, promptTokens, completionTokens)
}

// HandleRequest — main handler for POST /v1/chat/completions
// ---------------------------------------------------------------------------

func (rt *Router) HandleRequest(c *fiber.Ctx) error {
	body := c.Body()
	if len(body) == 0 {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "empty request body"})
	}

	var reqBody map[string]interface{}
	if err := json.Unmarshal(body, &reqBody); err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "invalid JSON"})
	}

	reqModel, _ := reqBody["model"].(string)
	if reqModel == "" {
		reqModel = "gpt-3.5-turbo"
	}

	// Check if request targets a combo directly
	if combo, ok := rt.Combos[reqModel]; ok {
		return rt.routeCombo(c, combo, reqBody)
	}

	// Resolve via prefix routing
	p, resolvedModel := rt.resolveModel(reqModel)

	if combo, ok := rt.Combos[resolvedModel]; ok {
		return rt.routeCombo(c, combo, reqBody)
	}

	if p == nil {
		providers := rt.healthyProviders()
		if len(providers) == 0 {
			return c.Status(fiber.StatusServiceUnavailable).JSON(fiber.Map{"error": "no providers available"})
		}
		return rt.routeWithFallback(c, providers, resolvedModel, reqBody)
	}

	isStream := reqBody["stream"] == true
	if isStream {
		return rt.streamUpstream(c, p, resolvedModel, reqBody)
	}
	return rt.routeWithFallback(c, []*provider.Provider{p}, resolvedModel, reqBody)
}

// ---------------------------------------------------------------------------
// resolveModel — prefix-based model → provider resolution
// ---------------------------------------------------------------------------

func (rt *Router) resolveModel(reqModel string) (*provider.Provider, string) {
	rt.mu.RLock()
	defer rt.mu.RUnlock()

	// 1) Exact model match on any provider
	for _, p := range rt.Providers {
		if p.IsHealthy() && rt.providerHasModel(p, reqModel) {
			return p, reqModel
		}
	}

	// 2) Prefix match: "JEROUTER/f/mimo-v2.5-free" → strip "JEROUTER/" and find provider
	for name, p := range rt.Providers {
		if p.Prefix != "" && strings.HasPrefix(reqModel, p.Prefix) {
			stripped := strings.TrimPrefix(reqModel, p.Prefix)
			stripped = strings.TrimPrefix(stripped, "/") // strip separator
			if rt.providerHasModel(p, stripped) || rt.providerHasModel(p, "*") {
				return p, stripped
			}
		}
		// Also try using the provider name as a prefix
		if strings.HasPrefix(reqModel, name+"/") {
			stripped := strings.TrimPrefix(reqModel, name+"/")
			return p, stripped
		}
	}

	// 3) Wildcard model provider
	for _, p := range rt.Providers {
		if p.IsHealthy() && rt.providerHasModel(p, "*") {
			return p, reqModel
		}
	}

	// 4) Fallback to any healthy provider
	for _, p := range rt.Providers {
		if p.IsHealthy() {
			return p, reqModel
		}
	}

	return nil, reqModel
}

func (rt *Router) providerHasModel(p *provider.Provider, model string) bool {
	if model == "*" {
		return true
	}
	for _, m := range p.Models {
		if m == model || m == "*" {
			return true
		}
	}
	return false
}

// ---------------------------------------------------------------------------
// healthyProviders — returns all providers that are active, unlocked, and have keys
// ---------------------------------------------------------------------------

func (rt *Router) healthyProviders() []*provider.Provider {
	rt.mu.RLock()
	defer rt.mu.RUnlock()
	var result []*provider.Provider
	for _, p := range rt.Providers {
		if p.IsHealthy() && p.HasKeys() {
			result = append(result, p)
		}
	}
	return result
}

// ---------------------------------------------------------------------------
// routeCombo — route via a named combo (multiple providers, strategies)
// ---------------------------------------------------------------------------

func (rt *Router) routeCombo(c *fiber.Ctx, combo *config.Combo, reqBody map[string]interface{}) error {
	// Collect unique healthy providers from combo routes
	seen := make(map[string]bool)
	var providers []*provider.Provider
	for _, route := range combo.Routes {
		if seen[route.Provider] {
			continue
		}
		if p, ok := rt.Providers[route.Provider]; ok && p.IsHealthy() && p.HasKeys() {
			providers = append(providers, p)
			seen[route.Provider] = true
		}
	}

	if len(providers) == 0 {
		providers = rt.healthyProviders()
		if len(providers) == 0 {
			return c.Status(fiber.StatusServiceUnavailable).JSON(fiber.Map{"error": "no healthy providers in combo"})
		}
	}

	isStream := reqBody["stream"] == true

	// If combo supports speculative hedge and we have 2+ providers
	if rt.Config.Router.SpeculativeHedge && len(providers) >= 2 && !isStream {
		return rt.hedgeRequest(c, combo, providers, reqBody)
	}

	// Pick ordered routes via strategy
	routes := rt.pickComboRoute(combo)
	if len(routes) == 0 {
		return rt.routeWithFallback(c, providers, combo.Name, reqBody)
	}

	if isStream {
		return rt.streamWithFailover(c, routes, reqBody)
	}

	// Non-stream: try routes in order via fallback
	for _, route := range routes {
		p, ok := rt.Providers[route.Provider]
		if !ok || !p.IsHealthy() || !p.HasKeys() {
			continue
		}
		model := route.Model
		if model == "" {
			model, _ = reqBody["model"].(string)
		}
		start := time.Now()
		data, err := rt.forwardRequest(p, model, reqBody)
		if err == nil {
			latMs := float64(time.Since(start).Milliseconds())
			pIn, pOut := parseTokens(data)
			logReq(c, model, p.Name, "ok", "", latMs, pIn, pOut)
			c.Set("Content-Type", "application/json")
			return c.Send(data)
		}
		fmt.Printf("[routeCombo] non-stream route %s failed: %v\n", model, err)
	}

	logReq(c, combo.Name, "", "error", "all combo routes failed", 0, 0, 0)
	return c.Status(fiber.StatusBadGateway).JSON(fiber.Map{"error": "all combo routes failed"})
}

// ---------------------------------------------------------------------------
// pickComboRoute — score-weighted / round-robin / fallback route selection
// ---------------------------------------------------------------------------

func (rt *Router) pickComboRoute(combo *config.Combo) []config.ComboRoute {
	if len(combo.Routes) == 0 {
		return nil
	}

	// Filter to active providers
	var active []config.ComboRoute
	for _, route := range combo.Routes {
		p, ok := rt.Providers[route.Provider]
		if !ok || !p.IsHealthy() || !p.HasKeys() {
			continue
		}
		active = append(active, route)
	}

	if len(active) == 0 {
		return nil
	}

	switch combo.Strategy {
	case "round-robin":
		idx := int(time.Now().UnixNano()) % len(active)
		return active[idx : idx+1]
	case "fallback":
		// Priority-ordered: first = best
		return active
	default:
		// "weighted" or unknown → weighted random
		return rt.weightedSelect(active)
	}
}

func (rt *Router) weightedSelect(routes []config.ComboRoute) []config.ComboRoute {
	totalWeight := 0.0
	type wr struct {
		route  config.ComboRoute
		weight float64
	}
	var items []wr
	for _, route := range routes {
		w := route.Weight
		if w <= 0 {
			w = 1.0
		}
		if p, ok := rt.Providers[route.Provider]; ok && !p.IsHealthy() {
			w *= 0.1
		}
		items = append(items, wr{route: route, weight: w})
		totalWeight += w
	}

	if totalWeight <= 0 {
		return routes
	}

	// Weighted random without replacement
	var result []config.ComboRoute
	remaining := make([]wr, len(items))
	copy(remaining, items)
	remTotal := totalWeight

	for len(remaining) > 0 {
		threshold := rand.Float64() * remTotal
		cum := 0.0
		pickIdx := 0
		for i, item := range remaining {
			cum += item.weight
			if cum >= threshold {
				pickIdx = i
				break
			}
		}
		result = append(result, remaining[pickIdx].route)
		remTotal -= remaining[pickIdx].weight
		remaining = append(remaining[:pickIdx], remaining[pickIdx+1:]...)
	}

	return result
}

// ---------------------------------------------------------------------------
// routeWithFallback — try providers in order, circuit-breaker aware
// ---------------------------------------------------------------------------

func (rt *Router) routeWithFallback(c *fiber.Ctx, providers []*provider.Provider, model string, reqBody map[string]interface{}) error {
	if len(providers) == 0 {
		return c.Status(fiber.StatusServiceUnavailable).JSON(fiber.Map{"error": "no providers available"})
	}

	maxAttempts := rt.Config.Router.MaxFallbackAttempts
	if maxAttempts <= 0 {
		maxAttempts = len(providers)
	}
	if maxAttempts > len(providers) {
		maxAttempts = len(providers)
	}

	var lastErr error
	for i := 0; i < maxAttempts; i++ {
		p := providers[i]

		if !p.IsHealthy() || !p.HasKeys() {
			continue
		}

		isStream := reqBody["stream"] == true
		if isStream {
			return rt.streamUpstream(c, p, model, reqBody)
		}

		start := time.Now()
		respData, err := rt.forwardRequest(p, model, reqBody)
		latencyMs := float64(time.Since(start).Milliseconds())

		if err == nil {
			p.RecordSuccess()
			pIn, pOut := parseTokens(respData)
			stats.RecordModelResult(p.Name, model, latencyMs, true)
			logReq(c, model, p.Name, "ok", "", latencyMs, pIn, pOut)

			c.Set("Content-Type", "application/json")
			return c.Send(respData)
		}

		lastErr = err
		p.RecordFailure(err.Error())
		stats.RecordModelResult(p.Name, model, latencyMs, false)

		// Continue to next provider
	}

	if lastErr != nil {
		return c.Status(fiber.StatusBadGateway).JSON(fiber.Map{
			"error": fmt.Sprintf("all providers failed, last error: %v", lastErr),
		})
	}
	logReq(c, model, "", "error", "no successful provider response", 0, 0, 0)
	return c.Status(fiber.StatusServiceUnavailable).JSON(fiber.Map{"error": "no successful provider response"})
}

// ---------------------------------------------------------------------------
// forwardRequest — sends non-streaming request to upstream provider
// ---------------------------------------------------------------------------

func (rt *Router) forwardRequest(p *provider.Provider, model string, reqBody map[string]interface{}) ([]byte, error) {
	// Resolve model — only use DefaultModel when no specific model resolved
	upstreamModel := model
	if upstreamModel == "" || upstreamModel == "*" {
		if p.DefaultModel != "" {
			upstreamModel = p.DefaultModel
		} else if len(p.Models) > 0 && p.Models[0] != "*" {
			upstreamModel = p.Models[0]
		}
	}

	// Build request body with remapped model
	bodyCopy := deepCopyMap(reqBody)
	bodyCopy["model"] = upstreamModel

	jsonBody, err := json.Marshal(bodyCopy)
	if err != nil {
		return nil, fmt.Errorf("marshal request: %w", err)
	}

	apiURL := p.ApiBase
	if !strings.HasSuffix(apiURL, "/") {
		apiURL += "/"
	}
	apiURL += "chat/completions"

	req, err := http.NewRequest("POST", apiURL, bytes.NewReader(jsonBody))
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")
	key := p.NextKey()
	if key != "" {
		req.Header.Set("Authorization", "Bearer "+key)
	}

	client := &http.Client{
		Timeout: 120 * time.Second,
		Transport: &http.Transport{
			DialContext: (&net.Dialer{
				Timeout:   10 * time.Second,
				KeepAlive: 30 * time.Second,
			}).DialContext,
			TLSHandshakeTimeout:   10 * time.Second,
			ResponseHeaderTimeout: 0, // no limit for non-streaming
			IdleConnTimeout:       90 * time.Second,
		},
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("upstream request failed: %w", err)
	}
	defer resp.Body.Close()

	respData, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read response: %w", err)
	}

	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("upstream %d: %s", resp.StatusCode, string(respData))
	}

	return respData, nil
}

// ---------------------------------------------------------------------------
// stripNullBytes removes null bytes and leading empty lines from SSE data.
func stripNullBytes(data []byte) []byte {
	cleaned := make([]byte, 0, len(data))
	for _, b := range data {
		if b != 0 {
			cleaned = append(cleaned, b)
		}
	}
	i := 0
	for i < len(cleaned) && cleaned[i] == byte('\n') {
		i++
	}
	return cleaned[i:]
}

// streamUpstream — SSE streaming with pre-first-token failover + TTFT timeout
// ---------------------------------------------------------------------------

func (rt *Router) streamUpstream(c *fiber.Ctx, p *provider.Provider, model string, reqBody map[string]interface{}) error {
	// Resolve model — only use DefaultModel when no specific model resolved
	upstreamModel := model
	if upstreamModel == "" || upstreamModel == "*" {
		if p.DefaultModel != "" {
			upstreamModel = p.DefaultModel
		} else if len(p.Models) > 0 && p.Models[0] != "*" {
			upstreamModel = p.Models[0]
		}
	}

	bodyCopy := deepCopyMap(reqBody)
	bodyCopy["model"] = upstreamModel

	jsonBody, err := json.Marshal(bodyCopy)
	if err != nil {
		return fmt.Errorf("marshal: %w", err)
	}

	apiURL := p.ApiBase
	if !strings.HasSuffix(apiURL, "/") {
		apiURL += "/"
	}
	apiURL += "chat/completions"

	req, err := http.NewRequest("POST", apiURL, bytes.NewReader(jsonBody))
	if err != nil {
		return err
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "text/event-stream")
	req.Header.Set("Cache-Control", "no-cache")
	req.Header.Set("Connection", "keep-alive")
	key := p.NextKey()
	if key != "" {
		req.Header.Set("Authorization", "Bearer "+key)
	}

	ctx, cancel := context.WithTimeout(context.Background(), StreamIdleTimeout)
	defer cancel()
	req = req.WithContext(ctx)

	start := time.Now()
	streamClient := &http.Client{
		Timeout: 120 * time.Second, // overall client timeout
		Transport: &http.Transport{
			DialContext: (&net.Dialer{
				Timeout:   10 * time.Second,
				KeepAlive: 30 * time.Second,
			}).DialContext,
			Proxy:                  nil,
			TLSHandshakeTimeout:   10 * time.Second,
			ResponseHeaderTimeout:  0, // disabled for streaming — slow models need time before first byte
			IdleConnTimeout:       90 * time.Second,
		},
	}
	resp, err := streamClient.Do(req)
	if err != nil {
		p.RecordFailure("stream connect: " + err.Error())
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		respBody, _ := io.ReadAll(resp.Body)
		p.RecordFailure(fmt.Sprintf("stream status %d", resp.StatusCode))
		return fmt.Errorf("upstream %d: %s", resp.StatusCode, string(respBody))
	}

	// Set SSE headers
	c.Set("Content-Type", "text/event-stream")
	c.Set("Cache-Control", "no-cache")
	c.Set("Connection", "keep-alive")
	c.Set("X-Accel-Buffering", "no")
	c.Status(200)

	flusher, _ := c.Context().Response.BodyWriter().(http.Flusher)

	reader := bufio.NewReader(resp.Body)

	// Wait for first byte with TTFT timeout
	firstCh := make(chan struct {
		n   int
		err error
		data []byte
	}, 1)
	go func() {
		buf := make([]byte, 4096)
		n, err := reader.Read(buf)
		firstCh <- struct {
			n   int
			err error
			data []byte
		}{n, err, buf[:n]}
	}()

	ttftTimer := time.NewTimer(TTFTTimeout)
	defer ttftTimer.Stop()

	select {
	case first := <-firstCh:
		ttftMs := float64(time.Since(start).Milliseconds())
		if first.err != nil && first.n == 0 {
			p.RecordFailure("TTFT read error: " + first.err.Error())
			return first.err
		}

		p.RecordSuccess()
		stats.RecordModelResult(p.Name, model, ttftMs, true)
		stats.RecordThroughput(p.Name, model, ttftMs, 0, first.n/4)
		// Estimate prompt tokens from request body size, completion tracked below
		promptTok := len(jsonBody) / 4
		logReq(c, model, p.Name, "ok", "", ttftMs, promptTok, 0)

		// Write first chunk (strip null bytes from upstream)
		cleanFirst := stripNullBytes(first.data)
		if len(cleanFirst) > 0 {
			c.Write(cleanFirst)
		}
		if flusher != nil {
			flusher.Flush()
		}

		// Track tokens from stream content
		totalCompletion := first.n / 4 // estimate from bytes

		// Stream remaining
		buf := make([]byte, 4096)
		for {
			n, err := reader.Read(buf)
			if n > 0 {
				clean := stripNullBytes(buf[:n])
				c.Write(clean)
				totalCompletion += len(clean) / 4 // rough token estimate
				if flusher != nil {
					flusher.Flush()
				}
			}
			if err != nil {
				if err != io.EOF {
					return nil // stream ended
				}
				return nil
			}
		}

	case <-ttftTimer.C:
		// TTFT timeout — this provider is slow, fail over if possible
		p.RecordFailure("TTFT timeout")
		return fmt.Errorf("TTFT timeout from %s", p.Name)
	}
}

// ---------------------------------------------------------------------------
// streamWithFailover — stream from first provider, fail over if TTFT times out
// ---------------------------------------------------------------------------

func (rt *Router) streamWithFailover(c *fiber.Ctx, routes []config.ComboRoute, reqBody map[string]interface{}) error {
	for _, route := range routes {
		p, ok := rt.Providers[route.Provider]
		if !ok || !p.IsHealthy() || !p.HasKeys() {
				continue
		}

		model := route.Model
		if model == "" {
			model, _ = reqBody["model"].(string)
		}

		err := rt.streamUpstream(c, p, model, reqBody)
		if err == nil {
			return nil
		}
		// Fallback on timeout errors (TTFT timeout, context deadline, etc)
		errMsg := err.Error()
		if strings.Contains(errMsg, "TTFT timeout") || strings.Contains(errMsg, "timeout") || strings.Contains(errMsg, "deadline") {
			continue
		}
		return err
	}

	logReq(c, reqBody["model"].(string), "", "error", "all streaming providers failed", 0, 0, 0)
	return c.Status(fiber.StatusBadGateway).JSON(fiber.Map{"error": "all streaming providers failed"})
}

// ---------------------------------------------------------------------------
// hedgeRequest — speculative hedge: fire 2 providers in parallel, return first
// ---------------------------------------------------------------------------

func (rt *Router) hedgeRequest(c *fiber.Ctx, combo *config.Combo, providers []*provider.Provider, reqBody map[string]interface{}) error {
	if len(providers) < 2 {
		return rt.routeWithFallback(c, providers, combo.Name, reqBody)
	}

	type result struct {
		data []byte
		err  error
		name string
		lat  time.Duration
	}

	ch := make(chan result, 2)
	var wg sync.WaitGroup

	for i := 0; i < 2; i++ {
		p := providers[i]
		wg.Add(1)
		go func() {
			defer wg.Done()
			start := time.Now()
			data, err := rt.forwardRequest(p, combo.Name, reqBody)
			ch <- result{
				data: data,
				err:  err,
				name: p.Name,
				lat:  time.Since(start),
			}
		}()
	}

	// Wait for first result
	first := <-ch
	wg.Wait()

	if first.err == nil && first.data != nil {
		fIn, fOut := parseTokens(first.data)
		stats.RecordModelResult(first.name, combo.Name, float64(first.lat.Milliseconds()), true)
		logReq(c, combo.Name, first.name, "ok", "", float64(first.lat.Milliseconds()), fIn, fOut)
		c.Set("Content-Type", "application/json")
		return c.Send(first.data)
	}

	// Try the other one
	second := <-ch
	if second.err == nil && second.data != nil {
		sIn, sOut := parseTokens(second.data)
		stats.RecordModelResult(second.name, combo.Name, float64(second.lat.Milliseconds()), true)
		logReq(c, combo.Name, second.name, "ok", "", float64(second.lat.Milliseconds()), sIn, sOut)
		c.Set("Content-Type", "application/json")
		return c.Send(second.data)
	}

	return c.Status(fiber.StatusBadGateway).JSON(fiber.Map{
		"error": fmt.Sprintf("hedge failed: first=%v, second=%v", first.err, second.err),
	})
}

// ---------------------------------------------------------------------------
// HealthCheck — run health checks on all providers
// ---------------------------------------------------------------------------

func (rt *Router) HealthCheck() {
	timeout := rt.Config.Router.HealthCheckInterval
	if timeout <= 0 {
		timeout = 30
	}

	for _, p := range rt.Providers {
		if !p.IsActive || !p.HasKeys() {
			continue
		}
		go func(p *provider.Provider) {
			client := &http.Client{Timeout: time.Duration(timeout) * time.Second}
			url := p.ApiBase
			if !strings.HasSuffix(url, "/") {
				url += "/"
			}
			url += "models"

			req, err := http.NewRequest("GET", url, nil)
			if err != nil {
				provider.TripCircuit(p, "health check create: "+err.Error(), 0)
				return
			}
			key := p.NextKey()
			if key != "" {
				req.Header.Set("Authorization", "Bearer "+key)
			}

			resp, err := client.Do(req)
			if err != nil {
				provider.TripCircuit(p, "health check: "+err.Error(), 0)
				return
			}
			defer resp.Body.Close()
			if resp.StatusCode >= 500 {
				provider.TripCircuit(p, fmt.Sprintf("health check status %d", resp.StatusCode), 0)
				return
			}
			// Healthy — reset if locked
			if p.IsLocked() {
				provider.ResetCircuit(p, "health check recovered")
			}
		}(p)
	}
}

// StartHealthCheckLoop starts periodic health checks in a goroutine.
func (rt *Router) StartHealthCheckLoop() {
	interval := rt.Config.Router.HealthCheckInterval
	if interval <= 0 {
		interval = 120
	}
	go func() {
		ticker := time.NewTicker(time.Duration(interval) * time.Second)
		defer ticker.Stop()
		for range ticker.C {
			rt.HealthCheck()
		}
	}()
}

// ---------------------------------------------------------------------------
// Token extraction helper
// ---------------------------------------------------------------------------

// parseTokens extracts prompt_tokens and completion_tokens from upstream response JSON.
func parseTokens(data []byte) (prompt, completion int) {
	var resp struct {
		Usage *struct {
			PromptTokens     int `json:"prompt_tokens"`
			CompletionTokens int `json:"completion_tokens"`
		} `json:"usage"`
	}
	if err := json.Unmarshal(data, &resp); err == nil && resp.Usage != nil {
		return resp.Usage.PromptTokens, resp.Usage.CompletionTokens
	}
	return 0, 0
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

func deepCopyMap(m map[string]interface{}) map[string]interface{} {
	out := make(map[string]interface{}, len(m))
	for k, v := range m {
		if sub, ok := v.(map[string]interface{}); ok {
			out[k] = deepCopyMap(sub)
		} else {
			out[k] = v
		}
	}
	return out
}
