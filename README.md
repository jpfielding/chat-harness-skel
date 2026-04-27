# chat-harness-skel

A Go harness that unifies access to multiple LLM providers behind a normalized chat API. Routes by policy, falls back on structured errors, and exposes both a Go library and an HTTP server.

**Status:** experimental. Public API may break across minor versions.

---

## Why

Three jobs in one system:

1. **Unified provider adapter** — send the same normalized request to Claude, GPT, or a local Ollama model; caller changes nothing but the model name.
2. **Routing by problem type** — declarative TOML policy tiers (`fast`, `reasoning`, `vision`) that map to ordered model lists.
3. **Fallback on failure** — structured errors drive safe fallback; no silent duplication of side effects once bytes have been emitted.

## Features (v1)

| Feature | Anthropic | OpenAI | Ollama |
|---|---|---|---|
| Non-streaming chat | ✅ | ✅ | ✅ |
| Streaming (SSE / NDJSON) | ✅ | ✅ | ✅ |
| Tool use, parallel calls | ✅ | ✅ | ✅ (server ≥ 0.3.0) |
| Multi-turn sessions | ✅ | ✅ | ✅ |
| Vision (images) | ✅ | ✅ | ✅ (base64 only) |
| Extended thinking | ✅ | — | — |

Additional:

- **Capability fingerprint** gates fallback: the router never tries a model that can't serve the request. `ContextLength` errors only fall back to strictly-larger context windows.
- **Optimistic concurrency** on sessions (`AppendConditional` with `expected_version`).
- **No SDK dependencies** on provider adapters — stdlib `net/http` throughout — so tests are hermetic and SDK churn doesn't propagate.

## Build

```bash
make build
./bin/chat-harness --validate-config --config config.example.toml
CHAT_HARNESS_TOKEN=secret ./bin/chat-harness --config config.example.toml
```

Auth defaults closed. `CHAT_HARNESS_TOKEN` must be set unless `--allow-unauthenticated` or `CHAT_HARNESS_INSECURE_ALLOW_OPEN=1` is passed.

## HTTP API

All endpoints under `/api/`. Auth via `Authorization: Bearer $CHAT_HARNESS_TOKEN`. `/healthz` is always open.

```
GET    /healthz
GET    /api/models
POST   /api/chat                            # normalized chat.Request → chat.Response
POST   /api/chat/stream                     # SSE of StreamEvent
POST   /api/sessions                        # {id?, system?, metadata?}
GET    /api/sessions
GET    /api/sessions/{id}
POST   /api/sessions/{id}/messages          # {expected_version, messages[]}
DELETE /api/sessions/{id}
```

Example:

```bash
curl -s -H "Authorization: Bearer $CHAT_HARNESS_TOKEN" \
     -X POST http://localhost:8080/api/chat \
     -d '{
       "model": "anthropic:claude-haiku-4-5",
       "messages": [{"role":"user","content":[{"type":"text","text":"Hi"}]}]
     }'
```

Streaming:

```bash
curl -N -H "Authorization: Bearer $CHAT_HARNESS_TOKEN" \
     -X POST http://localhost:8080/api/chat/stream \
     -d '{"policy":"fast","messages":[{"role":"user","content":[{"type":"text","text":"Hi"}]}]}'
```

## Config (TOML)

See [config.example.toml](config.example.toml). Three pieces:

- `[providers.<name>]` — enable/disable + optional overrides.
- `[[policy]]` — named tiers, each with an ordered `candidates = ["provider:model", ...]`.
- `[router]` — `default_policy`, `fallback_on_kinds`, `per_attempt_timeout_ms`, `max_attempts`.

Validate without starting: `chat-harness --validate-config --config <path>`.

## Library

```go
import (
    "github.com/jpfielding/chat-harness-skel/pkg/chat"
    "github.com/jpfielding/chat-harness-skel/pkg/providers/openai"
)

p, _ := openai.New(openai.Config{APIKey: os.Getenv("OPENAI_API_KEY")})
h := chat.New(chat.WithProvider(p))
resp, _ := h.Send(ctx, chat.Request{
    Model:    "openai:gpt-5-mini",
    Messages: []chat.Message{chat.UserText("hi")},
})
```

## Devcontainer

```
code .   # Reopen in Container
```

`rockylinux:10` + Go 1.26.x, with credential binds from the host (`~/.netrc`, `~/.ssh`, `~/.claude`, `~/.codex`, `~/.gemini`, `~/.gitconfig`). No corporate-CA `--setopt sslverify` bypasses — the `cas.pem` anchor + `update-ca-trust` handles it cleanly.

## Live smoke

```bash
export ANTHROPIC_API_KEY=sk-...  OPENAI_API_KEY=sk-...
go run ./scripts/smoke --live --provider=all
```

NOT run in CI. Use before releases and after SDK/provider bumps.

## Architecture

```
cmd/chat-harness    HTTP server (stdlib, go-web-service pattern)
pkg/chat            Normalized schema: Message, ContentBlock, StreamEvent,
                    Provider interface, Harness, ProviderError, Catalog
pkg/router          PolicyRouter + RequestCapabilities fingerprint
pkg/session         Store interface + MemoryStore (optimistic concurrency)
pkg/providers/*     Per-provider adapters (stdlib net/http, no SDKs)
pkg/providers/sse   Shared SSE line-protocol parser
pkg/httpapi         HTTP handlers (chat, stream, sessions, models)
pkg/core            Config loader + Service wiring
```

## Known limitations

- **No Bedrock/Azure yet.** Deferred to v1.1 pending Converse-API quirks and Azure deployment-name semantics.
- **No `/api/compare`.** Fan-out benchmark mode is a v1.1 add.
- **No SQLite/Redis session stores.** In-memory only for v1.
- **No observability beyond structured logs.** OTEL + metrics are future work.
- **Tool-call ids are provider-scoped.** Ollama synthesizes fake ids; don't route a mid-conversation tool call across providers.
- **Streaming tool arguments are not guaranteed JSON-valid.** `RawInput` + `ParseError` are exposed on `ToolUse`; strict validation is opt-in.
- **`ProviderMetadata` is an escape hatch.** Callers that read keys from it are outside the normalized contract.

## License

MIT
