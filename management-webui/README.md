# Scoped Management Web UI

This is the management frontend bundled with this CodexProxy fork.

## Supported surface

- Codex API-key and OAuth configuration
- Claude API-key and OAuth configuration
- OpenAI-compatible upstream providers
- Auth files, quota, logs, scoped configuration, and runtime information

Gemini, Vertex, AI Studio, Antigravity, Kimi, xAI/Grok, plugin management, and the plugin store are intentionally excluded.

## Development

```bash
bun install --frozen-lockfile
bun run dev
bun run verify
```

The production build is a single HTML file at `dist/index.html`.

To refresh the Go-embedded standalone asset:

```bash
VERSION=__CLI_PROXYAPI_VERSION__ \
  MANAGEMENT_BUNDLE_OUT_DIR=../internal/managementasset/bundled \
  bun run build
```

The release workflow verifies that the checked-in embedded asset exactly matches a fresh frontend build.
