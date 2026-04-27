# chat-harness-skel

A Go harness for LLM providers. Unifies access to multiple chat models behind a normalized API, with routing, fallback, and streaming. Exposes a Go library and an HTTP server.

**Status:** experimental. Public API may break.

## v1 scope

- **Providers:** Anthropic, OpenAI, Ollama.
- **Features:** non-streaming + streaming chat, tool use (incl. parallel), multi-turn via in-memory sessions.
- **Routing:** TOML-declared policy tiers + per-request override; fallback on structured errors; capability-fingerprint-gated candidates.
- **HTTP surface:** `/api/chat`, `/api/chat/stream`, `/api/sessions/*`, `/api/models`, `/healthz`.
- **Auth:** static bearer, default closed.

## Build

```
make build
./bin/chat-harness --config config.example.toml
```

## Devcontainer

```
code .
# Reopen in container — rockylinux:10 + Go 1.26.x with all credential binds.
```

## License

MIT
