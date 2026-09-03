# VRouter-Go 🚀

**High-performance OpenAI-compatible LLM routing gateway** — rebuilt in Go for maximum speed and minimal resource usage.

A complete Go reimplementation of [VRouter](https://github.com/rezkyrafael2901/vrouter) with 10x better performance characteristics.

## Features

- **Multi-provider routing** — route requests across multiple LLM providers
- **Model combos** — combine multiple models/providers into a single endpoint
- **Circuit breaker** — automatic provider locking on repeated failures
- **Health check** — periodic provider liveness probing
- **Dead model detection** — auto-disable broken models, auto-revive after cooldown
- **Tok/s meter** — real-time throughput monitoring per model
- **Health score** — composite 0-100 score (reliability 45% + latency 25% + throughput 20% + freshness 10%)
- **Cost-aware routing** — pick cheapest healthy model automatically
- **SSE streaming** — full OpenAI-compatible streaming with pre-first-token failover
- **Speculative hedge** — fire 2 providers in parallel, take first response
- **Key rotation** — round-robin API key rotation per provider
- **Dashboard API** — REST endpoints for monitoring and management

## Quick Start

```bash
# Build
go build -o vrouter ./cmd/vrouter

# Run with config
VROUTER_CONFIG=config.yaml ./vrouter
```

## API Endpoints

| Method | Endpoint | Description |
|--------|----------|-------------|
| POST | `/v1/chat/completions` | OpenAI-compatible chat completions |
| GET | `/api/status` | Full system status |
| GET | `/api/model-stats` | Per-model performance stats |
| GET | `/api/health-score` | Composite health scores |
| GET | `/api/circuit-breakers` | Circuit breaker states |
| GET | `/api/throughput` | Tok/s meter |
| GET | `/v1/health` | Health check |

## Configuration

See `config.yaml` for configuration reference.

## Performance

| Metric | Python VRouter | Go VRouter |
|--------|---------------|------------|
| Memory | ~80MB | ~8MB |
| Latency overhead | ~10-20ms | ~0.5-1ms |
| Startup time | ~2-3s | ~0.1s |
| Binary size | N/A (needs Python) | ~10MB standalone |

## License

MIT
