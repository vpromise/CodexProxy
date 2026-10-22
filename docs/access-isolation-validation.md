# Account isolation validation

Branch: `codex/user-provider-isolation`.

## Implementation coverage

| Ticket | Implemented behavior | Automated evidence |
| --- | --- | --- |
| #2 | Access-policy changes preserve active generations | Real service tests with local HTTP, SSE and WebSocket upstreams; management and file reload paths |
| #3 | Named single-provider pools, overlap, empty pools, persistent record identities | Management round trips, stale-save rejection, foreign-member rejection, configuration restart reads |
| #4 | Client-key names, notes and one-pool bindings | Binding round trips; referenced-pool deletion rejected through dedicated and full-YAML APIs |
| #5 | Same-account maintenance and lifecycle revocation | Synthetic OAuth update, file rename, delete/reimport; refresh cannot recreate a removed source file |
| #6 | Shared native selection boundary | Execution rejects foreign provider/pins and revoked keys; no upstream calls for empty pools |
| #7 | Scoped client catalogs and separate management catalog | Public HTTP catalog changes with pool membership; projections reuse registered authorized models |
| #8 | Existing Responses sockets use current grants | Activation on an already-open socket and subsequent revocation; current generation completes and next request is denied |
| #9 | Search uses the same native authorization boundary | Public empty-pool search rejection and shared credential-policy selection |
| #10 | Realtime operations and derived credentials remain scoped | Local WebSocket messages denied after membership removal; Claude keys cannot mint Codex credentials; derived grants cannot expand after issuer reassignment |
| #11 | Management workflow, localization, configuration and operator documentation | Four locales; frontend tests, lint, type check and production build |

Caller-supplied session hints were also tested with two keys sharing an overlapping
pool: each caller retains its own affinity binding.

## Checks run

- `go test ./... -count=1 -timeout=240s`: two existing platform-dependent tests
  failed because this macOS host cannot bind `127.0.0.2`:
  `TestClaudeStandardRoundTripperBindsLocalIP` and
  `TestClaudeStandardRoundTripperHTTPSProxyKeepsBindAndProxyTLS`, both in
  `internal/runtime/executor/helps/utls_client_test.go`. Those tests were not
  modified.
- `go test ./... -skip
  '^(TestClaudeStandardRoundTripperBindsLocalIP|TestClaudeStandardRoundTripperHTTPSProxyKeepsBindAndProxyTLS)$'
  -count=1 -timeout=240s`: passed.
- Focused full-package regressions for `sdk/cliproxy`, `sdk/cliproxy/auth`,
  `internal/client/codex/live`, and `internal/api/handlers/management`: passed,
  including after final policy validation changes.
- Race checks for access reload, pool management, named bindings, derived grants,
  and Realtime operations in those four packages: passed.
- `go build -o /tmp/codexproxy-isolation-server ./cmd/server`: passed.
- `bun run verify` in `management-webui`: 366 tests passed, followed by lint,
  TypeScript compilation and Vite production build.
- `git diff --check`: passed.

The Go suite ran with outbound network restricted to loopback and real local
credential/configuration files denied by an OS sandbox. Feature fixtures used
synthetic keys, temporary storage and local mock upstreams. No real account,
OAuth login, provider quota probe or paid request was used.

## Remaining acceptance

Browser acceptance is pending. A separate local service with synthetic accounts
and the built management UI is available at
`http://127.0.0.1:18764/management.html#/access-isolation`. Browser security policy
rejected opening the local origin, including after explicit user consent,
because of a saved permission or managed policy. No alternate browser, direct
browser protocol, or other workaround was attempted.

Once browser access is available, verify pool creation/rename, provider-filtered
account selection, overlapping and empty pools, key labels and assignments,
referenced deletion, activation warnings, stale-save handling, and reload.
No screenshots or completed browser scenario are claimed.

The local UI refinement reuses the shared inputs, selectors, checkboxes, toggle,
confirmation dialogs, and Configuration page's floating save bar. It adds a
pool list with a focused editor, account and key filtering, credential references,
assignment status, unsaved-edit protection, and responsive layouts. Review fixes
use the shared client-ID fallback for HTTP environments, retain edited key rows
until the search or status filter changes, and match Configuration's outer
spacing. Native credentials now display masked keys, provider-page auth indexes,
and prefixes; short keys are fully masked. Seven additional frontend tests cover
ID fallback and filter/edit interactions. The public management handler test
checks credential correspondence and secret masking using synthetic records.
All 366 frontend tests, lint, TypeScript checks, the production build, and the
management Go package tests passed after these fixes. Visual and interactive
browser acceptance remains pending.

The two platform-dependent tests also remain unverified on a host with the
required loopback address. The implementation should not be described as having
completed every acceptance criterion until these outstanding checks are resolved.

## Production findings and repair plan — 2026-10-22

| Finding | Repair | Acceptance |
| --- | --- | --- |
| Successful policy saves can precede runtime revocation | Publish an immutable access-policy snapshot and client authentication synchronously; isolate executor reloads; reject stale reload grants against persisted policy | Delayed reload tests for enable/revoke through dedicated and full-YAML APIs; immediate HTTP key replacement and scoped catalogs; stale replay and external edits; active generation preservation |
| File log viewer disabled and misleading product wording | Add a localized enable action and explain future-only file logs; enable file logging on dmit with a protected configuration backup | Local logging toggle/query regression; verify production `/logs` returns 200 and main.log exists while all other effective settings remain equal |
| Missing request-to-account audit trail | Emit structured, non-secret decision references, policy fingerprint and per-operation counter at selection/rejection/session boundaries | Real mocked execution and revocation assert selected identity, changed revision, no denied upstream attempt, and no private values in formatted output |
| Remote catalog rejected for missing a fixed default model | Keep record validation; select a deterministic fallback template | Complete alternate-slug catalogs accepted; malformed catalogs rejected; usable fallback template and existing template refresh tests |

Production configuration repair: dmit application file logging was enabled without
restarting the container. The log query returned 200, main.log was created, health
remained healthy, and the container ID/restart count were unchanged. All other
effective configuration fields, including isolation bindings and full request
logging, compared equal before/after. Backup:
`/root/cpa-deploy/releases/file-logging-fix-20261022T085320Z/config.before.yaml`.
The same directory contains a verification receipt. This was a configuration
repair, not activation of the new source code.

The repair tests use synthetic credentials only, with real credential paths and
non-loopback network access denied. A source-only temporary checkout is used for
the full Go suite so package discovery does not enter the real auth directory.
Browser acceptance retains the previously documented managed-policy limitation.

Repair verification results:

- The new enable/revoke acknowledgment regressions were first observed failing;
  they now pass for both management endpoints, including checks during stale
  runtime replay and after completion.
- Immediate HTTP key replacement and catalog restriction passed five repeated
  runs, with no propagation sleeps in the catalog acceptance test.
- Eight affected Go packages passed their complete test suites.
- Full source-only Go suite passed with only the two previously documented
  macOS `127.0.0.2` binding tests excluded. Their unmodified failures were also
  observed on the initial complete run; Linux execution is not claimed.
- Race-detector checks passed for management acknowledgment, file logging,
  immediate HTTP key authentication, audit redaction, policy snapshot ownership,
  existing HTTP/WebSocket generation behavior, and Realtime operations.
- Frontend verification passed all 366 tests, ESLint, TypeScript and production
  build. The Go-embedded page was regenerated with `VERSION=__CLI_PROXYAPI_VERSION__`.
- Server compilation, embedded-page contract tests and `git diff --check` passed.

New source activation on dmit is separate from the completed file-logging
configuration repair. No new source deployment or real-provider inference was
performed as part of these code fixes.
