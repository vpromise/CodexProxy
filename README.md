# CodexProxy

English | [中文](README_CN.md)

CodexProxy is a focused fork of [CLIProxyAPI](https://github.com/router-for-me/CLIProxyAPI). It exposes OpenAI, Codex, and Claude-compatible APIs while limiting native upstream integrations to Codex and Claude.

## Supported scope

| Upstream | Access methods | Downstream APIs |
| --- | --- | --- |
| Codex | OAuth, device OAuth, Codex-compatible API key | OpenAI Chat Completions, Responses, Codex direct aliases, images, realtime |
| Claude | OAuth, Claude-compatible API key | Claude Messages and OpenAI-compatible APIs through translation |
| Generic compatible gateway | `openai-compatibility` config, plugin, or SDK executor | OpenAI Chat Completions and Responses-compatible paths |

Gemini, Interactions, Vertex, AI Studio, Antigravity, Kimi, xAI, and Grok are not part of the supported native runtime. Legacy implementations may remain in the source tree during staged cleanup, but the scoped server does not register their CLI flags, HTTP routes, management routes, baseline executors, credentials, or models.

## Main capabilities

- OpenAI-compatible `/v1/chat/completions`, `/v1/completions`, `/v1/responses`, and `/v1/models`
- Claude-compatible `/v1/messages` and `/v1/messages/count_tokens`
- Codex direct route aliases under `/backend-api/codex`
- Streaming, non-streaming, and supported Responses WebSocket requests
- Tool calls, text and image input, and Codex image generation
- Multiple credentials with round-robin, weighted round-robin, or fill-first routing
- Retry, cooldown, failover, priority, prefix, alias, and optional session affinity
- Configuration and credential hot reload
- Optional Management API, TUI, plugins, and embeddable Go SDK
- File, Postgres, Git, and object-store persistence backends

## Quick start

Requirements: Go 1.26 or newer.

```bash
cp config.example.yaml config.yaml
# Replace the example api-keys value before starting.
go run ./cmd/server --config config.yaml --local-model
```

The template uses port `8317`; set `host: "127.0.0.1"` for a local-only deployment. Check health:

```bash
curl http://127.0.0.1:8317/healthz
```

List models after configuring credentials:

```bash
curl http://127.0.0.1:8317/v1/models \
  -H 'Authorization: Bearer YOUR_PROXY_API_KEY'
```

## OAuth login

```bash
go run ./cmd/server --config config.yaml --codex-login
go run ./cmd/server --config config.yaml --codex-device-login
go run ./cmd/server --config config.yaml --claude-login
```

Use `--no-browser` or `--oauth-callback-port <port>` when needed.

## OpenAI-compatible gateway

Third-party platforms should use the generic compatibility configuration instead of a native adapter:

```yaml
openai-compatibility:
  - name: "example"
    base-url: "https://gateway.example.com/v1"
    api-key-entries:
      - api-key: "sk-..."
    models:
      - name: "upstream-model-id"
        alias: "example-model"
```

See [config.example.yaml](config.example.yaml) for routing, retry, model alias, and provider options.

## Architecture

- `cmd/server/` — process entry, flags, configuration, and persistence setup
- `internal/api/` — Gin routes, middleware, and Management API wiring
- `sdk/api/handlers/` — shared OpenAI and Claude request handling
- `sdk/cliproxy/auth/` — credential lifecycle, selection, retry, and cooldown
- `internal/runtime/executor/` — upstream executors
- `internal/translator/` — protocol translation registry and implementations
- `internal/thinking/` — canonical reasoning configuration and provider translation
- `internal/registry/` — dynamic model registry and model catalogs
- `sdk/cliproxy/` — embeddable service lifecycle and hot reload

## Development

See the [upstream sync ledger](docs/upstream-sync.md) for the last reviewed upstream commit, published backports, publication status, and deferred work.

```bash
gofmt -w .
go test ./...
go build -o test-output ./cmd/server && rm test-output
```

## License

This project follows the upstream repository's license. See [LICENSE](LICENSE).
