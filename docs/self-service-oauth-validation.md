# Self-service OAuth validation

Validated on 2026-11-15 (Asia/Shanghai), on branch `codex/self-service-oauth`,
based on `554a0aa93efafc23ba87a30bd605d738eb10b3b1` from `main`.
Host: macOS arm64. Go: `go1.27.3`. Frontend verification used the pinned Bun
`1.3.14`; an initial Bun `1.4.4` verification also passed and produced the identical
embedded management HTML.

## Automated checks

- Full Go suite: **8,892 passing test/subtest events, 69 passing packages**.
  Command: `go test -json ./... -skip '^TestClaudeStandardRoundTripper(BindsLocalIP|HTTPSProxyKeepsBindAndProxyTLS)$' -count=1`.
  The two excluded tests need local address `127.0.0.2`, which the host cannot bind
  (`errno 49`); a fresh socket probe confirmed this limitation. Four additional
  test/subtest events self-skipped: two other `127.0.0.2` cases and the optional
  public-egress IPv4 / native Claude TLS-capture checks. No failing test events.
- Race checks: **1,802 passing test/subtest events across five packages**, no race
  report. Command: `go test -race -json ./internal/api ./internal/api/handlers/management ./internal/api/middleware ./sdk/cliproxy/auth ./internal/watcher -count=1`.
- Frontend: **374 passing tests across 51 files**, ESLint, TypeScript and Vite
  production build passed with `VERSION=__CLI_PROXYAPI_VERSION__ bun run verify`.
  The newly added public-page tests execute its actual script against an isolated
  DOM/fetch fixture and cover consent, limited bearer use, callback submission,
  completion cleanup and rejecting an unexpected authorization-link origin.
- Server compilation: `go build -o <temporary-output>/codexproxy ./cmd/server` passed.
- Embedded management bundle matches the pinned production build byte for byte:
  SHA-256 `e9ac134deb25e2cd9f1a2d12df2685662c08518515bbbbbdda7cf65479cca3a6`.
- `git diff --check` passed.

Backend regressions cover both providers, restart-persistent PKCE, consent,
invitation expiry/revocation, callback/state validation, encoded state override
prevention, body limits, origin checks, secret-free responses, file permissions,
quarantining pending credentials, disabled import, unchanged pools, duplicate-account
preservation, idempotence, partial-import recovery without overwriting newer edits,
single exchange under concurrent submission, concurrent revocation, provider failure,
rejection and interrupted-exchange recovery. Real router tests confirm the public
capability cannot access management/inference and existing login still requires its
original management authentication. Detailed request logging excludes enrollment.

## Browser checks

A local server bound only to `127.0.0.1:18579` used an isolated temporary auth
directory, synthetic management/inference keys and no real provider credentials.
The server was stopped after verification.

- Public page: invitation validation, Chinese/English switch, required consent,
  authorization URL generation, official provider link and callback instructions.
- Management OAuth page: original Codex/Anthropic login controls remain visible;
  create/hide invitation, safe record rendering and review confirmation work.
- A synthetic pending Codex record was accepted through the browser. Auth Files
  showed **DISABLED**, zero active credentials and the enable switch off.
- The disabled credential could be assigned to an existing-style account pool
  before enablement. Saving preserved its disabled state; the pool explicitly
  reported that the member could not serve new requests.
- Screenshots were saved outside the repository in the task output directory.

The synthetic pending fixture is not evidence of a real provider OAuth exchange.
No real Claude/OpenAI login, token exchange, refresh or inference call was performed
for this feature. Before production rollout, complete one real invitation per
provider, accept disabled, assign the intended pool, then explicitly enable and
verify a permitted request and an isolated-key denial. Also verify HTTPS, proxy
Host/Authorization forwarding and credential-directory backup permissions.

## Operational limits and rollback

See [the feature guide](self-service-oauth.md) for fixed localhost callbacks,
manual callback paste, single-process directory ownership, the 5,000-record limit,
local pending storage and expiry while awaiting review. These limits are deliberate
and were not replaced with changes to existing login, refresh or routing behavior.

Before rollback, revoke open invitations and resolve or securely back up pending
records. Then restore the previous binary and management bundle. Existing accounts,
login routes and configuration formats remain compatible. Accepted credentials
remain ordinary auth files; disable/remove them and adjust pools through existing
administrator controls if they should no longer be used. Preserve the sibling
intake directory as sensitive data until its retention is decided. Rolling back
the software alone does not revoke provider grants or erase credentials.
