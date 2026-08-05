# llm-gateway

A lightweight Go middleware that sits between agents and the Gemini API,
enforcing free-tier rate limits (RPM/TPM/RPD) with per-model queueing
and automatic failover.

## Quick start

```bash
cp configs/config.example.yaml configs/config.yaml
# edit configs/config.yaml: confirm the model name strings match real
# Gemini API model IDs, and adjust limits to match your own tier.

export GEMINI_API_KEY=your-key-here

go run ./cmd/gateway
# in another terminal:
curl http://localhost:8080/healthz
```

## Test

```bash
go test ./...
```

## Layout

```
cmd/gateway/            entrypoint
internal/config/        YAML config load + validation
internal/provider/       Provider interface (backend-agnostic)
internal/provider/gemini/  Gemini client: real REST shape, error classification, usage parsing
configs/                 example config
```
