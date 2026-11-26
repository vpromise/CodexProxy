# Account isolation validation

## Follow-up runtime and static-check fixes — 2026-11-10

The auth-upload concurrency finding and all three static diagnostics from the
expanded pass are now resolved.

- Auth uploads acquire a cloned config, auth manager and persistence hook together
  under the handler mutex. Raw JSON and multipart batches retain that snapshot
  through identity enrollment, synthesis, file storage and registration. Body
  reads and persistence run without holding the config mutex. Regression tests
  cover concurrent reloads and a blocked upload while the directory, policy,
  manager and hook are replaced.
- `FileBodySource.WriteTo` now implements `io.WriterTo`, reporting both bytes and
  errors, including partial writes and separators. All callers consume the new
  signature; tests preserve existing log formatting and missing-part behavior.
- HTTP/model streaming callbacks defer cleanup until successful ownership
  transfer to the stream bridge. Startup/registration/encoding failures clean
  up; successful streams remain active until EOF, explicit close or the existing
  callback lifecycle cleanup. Tests cover unavailable bridges and retain the
  existing successful-stream lifetime assertions.

Verification on the fixed source:

Evidence and source hashes are retained at `/var/folders/b_/t12ytcwj16g3_wglff38ljj80000gn/T/codexproxy-fix-runtime-89or6xvi`. Remote temporary
files and local disposable builds/snapshots were removed after verification.

## Extended verification — 2026-11-10

| Check | Observed result |
| --- | --- |
| Final complete Go suite, shuffled | 68 packages; 7,501 test/subtest passes; four runtime skips; two previously documented macOS bind tests excluded |
| Initial complete Go race suite, shuffled | 68 packages; 7,495 test/subtest passes; seven runtime skips; the same two macOS exclusions. Later SDK repetition found the outstanding race below, so this first pass is not an all-clear |
| Egress/identity/cache race repetition | Seven packages, 25 iterations, 2,225 test/subtest passes; 75 expected skips for optional public probes and unavailable macOS secondary loopback addresses |
| New real-service egress integration | Three race iterations passed; covers 32 concurrent requests per wave across overlapping pools, source changes/unavailable source, active SSE completion after revocation, and restoring the inherited global proxy |
| Linux complete affected packages | All 11 passed; 4,423 test/subtest passes and two optional runtime skips; no macOS exclusions. Both previously excluded bind tests passed |
| Actual public IPv4 exits | `14.225.71.134`, `14.225.71.135`, and `14.225.71.136` each returned its selected source through the new HTTP transport |
| Frontend | All 369 tests, 1,183 assertions, lint, TypeScript and production build passed; production HTML equals the embedded asset byte-for-byte with the version placeholder |
| Builds | Darwin/arm64 and static Linux/amd64 server builds passed |
| Browser | Browser inventory reports `The browser access host is unavailable`; interactive acceptance remains pending |

### Concurrency finding (resolved by the follow-up above)

**P2: auth upload reads runtime references without synchronization.** A repeated
SDK race run of `TestAccessIsolationOAuthMaintenance` reported concurrent reads
in `UploadAuthFile` and `writeAuthFile` against `SetAuthManager`/`SetConfig` during
reload. The affected reads include the manager reference, auth directory,
isolation settings and config used for auth synthesis. These operations need a
consistent config/manager snapshot acquired under the handler mutex.

A deterministic synthetic reproducer concurrently calls the public upload
handler, invalid-JSON auth parsing, `SetConfig` and `SetAuthManager`. It reports
six races in the current snapshot and seven in the unchanged baseline
`5e986f5e432f96abd8abda69a2783cd5abc65b07`. Thus this is an existing runtime
problem exposed by the expanded checks. It was left unfixed in that test-only
pass; the follow-up above now records its remediation. A short baseline integration repetition happened to pass; that does not
invalidate the deterministic race evidence.

At the end of that pass, `go vet ./...` reported three existing locations, with output identical
to the unchanged HEAD: the nonstandard `WriteTo` signature in
`internal/logging/request_logger_body_source.go:171`, and cancellation paths in
`internal/pluginhost/host_callbacks.go:164` and
`internal/pluginhost/host_model_stream_callbacks.go:29`. No new vet diagnostic
was introduced by these changes.

### Test isolation corrections and evidence

The first new SSE fixture emitted only `response.created` before waiting, so it
failed its downstream-content visibility gate. It now emits a text delta before
waiting, matching the existing native SSE fixtures; all route and revocation
assertions remain intact. The first Linux namespace had only loopback, while
the default media relay intentionally excludes loopback candidates. Adding the
temporary veth inside that namespace made both the existing audio/data bridge
and the new egress tests pass without changing application behavior.

A shuffled Linux run also exposed a pre-existing test cleanup omission:
`TestServiceSerializesHomeAndWatcherConfigRuntimeApply` retained its published
Home client globally. The following pre-ACK test then observed that old client.
The unchanged HEAD reproduces the same failure with the two tests shuffled under
seeds 1, 2 and 4. The publishing test now clears only its own client on cleanup;
the pair passed ten race iterations, and the complete Linux SDK package passed
with the original failing full-suite shuffle seed. No runtime assertion was
removed or weakened.

Raw logs, source hashes, the deterministic race reproducer, original failures,
baseline comparisons and final receipts are retained locally at
`/var/folders/b_/t12ytcwj16g3_wglff38ljj80000gn/T/codexproxy-comprehensive-fu82d1cm`.

## Pool IPv4 egress — 2026-11-10

The implementation adds per-pool `inherit` and `direct-ip` settings, server-local
IPv4 suggestions, immutable request routing, pool/source transport partitioning,
Responses WebSocket reconnection, and Live/Realtime reauthorization. Direct
WebRTC requires the media relay and gathers host/STUN candidates only on the
selected IPv4. OAuth renewal and management/background traffic keep their
existing account routes, as documented in the editor and operator guide.

- Synthetic regressions cover source binding, proxy overrides, unavailable-source
  rejection, IPv6 rejection, connection reuse, immediate policy publication through
  both management save endpoints, active request snapshots and existing sockets.
- The dedicated egress race checks passed in six affected Go packages.
- The source-only Go suite passed in 68 packages on macOS, excluding the two
  previously recorded `127.0.0.2` tests. Both excluded tests passed separately
  using a cross-compiled test binary on the inspected Linux host.
- The new transport implementation also passed Linux tests with two loopback
  source addresses and opt-in HTTPS verification of three locally assigned
  public IPv4 addresses. Every public check observed its selected source address.
  These checks sent no provider credentials, inference or OAuth refresh requests.
- SDK transport regression coverage includes inherited legacy `DialTLS` hooks.
  Applying an explicit base dialer clears both TLS dial hooks, so neither standard
  HTTPS nor uTLS fallback requests can bypass the selected local source. Tests
  cover successful bound HTTPS and unavailable-source rejection using a trusted
  local TLS fixture, without disabling certificate validation.
- Bound standard and uTLS-fallback transports allocate independent TLS session
  caches when an SDK default supplies one. A trusted TLS fixture verifies reuse
  within one pool and fresh sessions across pools.
- Full-YAML saves reconcile normalized native records before acknowledging
  revocation. Empty and whitespace-only Codex base URLs are covered with runtime
  reload blocked, including overlapping pools and stale runtime replay.
- A local three-peer WebRTC fixture verifies both data-channel events and RTP
  audio against exit changes and membership revocation. Active upstream output
  can finish; new unauthorized input closes the session without being forwarded.
- Frontend verification passed 369 tests, lint, TypeScript and production build;
  the embedded management page was regenerated with the version placeholder.
- Server compilation passed. Browser verification was attempted against a local
  synthetic fixture but the browser access host was unavailable. No screenshot
  or completed interactive browser acceptance is claimed.

The server inspection and temporary test execution did not change its network
configuration or activate a CodexProxy service. Full Linux repository-suite
execution and a live-provider inference request are not claimed.

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

Browser acceptance remains pending. The latest CUA inventory reports
`The browser access host is unavailable`. Earlier attempts also encountered a
browser policy rejection for the local fixture. No acceptance service is left
running by this test pass, and no alternate browser or browser-protocol workaround
was used.

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

## Integration with updated main — 2026-10-22

Validation of the combined source in an isolated worktree:

- `go test ./... -skip
  '^(TestClaudeStandardRoundTripperBindsLocalIP|TestClaudeStandardRoundTripperHTTPSProxyKeepsBindAndProxyTLS)$'
  -count=1 -timeout=240s`: all 68 packages with tests passed. A local socket
  probe confirmed this macOS host still cannot bind `127.0.0.2`.
- Focused `go test -race` passed in `sdk/api/handlers`, `sdk/cliproxy`,
  `sdk/cliproxy/auth`, `internal/api`, `internal/api/handlers/management` and
  `internal/client/codex/live`, covering host reload/callbacks, access policy,
  isolation, audit, logging and derived-credential operations.
- `VERSION=__CLI_PROXYAPI_VERSION__ bun run verify`: 366 tests, ESLint,
  TypeScript and production build passed. The rebuilt HTML exactly matches
  the tracked Go-embedded management page.
- Required server build and `git diff --check` passed.

## Revoked client key cleanup — 2026-10-23

The root cause is an omitted UI cleanup action: replacing a client secret keeps
its old isolation metadata, and KeyBindings displays that metadata as Revoked
without offering removal. The existing access-isolation PUT endpoint already
accepts removal and persists it. No backend production behavior needed to change.
The fix adds Remove record only to revoked rows, uses the existing confirmation
modal and pending-change save/discard flow, and guards against removing active
key bindings through this action. All four supported locales and the embedded
management page are updated.

- Final frontend `bun run verify` passed **369 tests**, ESLint, TypeScript and the
  production build with the CI-pinned Bun **1.3.14** and
  `VERSION=__CLI_PROXYAPI_VERSION__`. The official Bun archive SHA-256 was verified
  against its release metadata before execution. Initial development checks also
  passed with installed Bun 1.4.0.
- Complete management-handler, configuration and management-asset package tests
  passed. Server `go build -o <temporary-output> ./cmd/server` passed after the
  generated page was embedded; the disposable build executable was removed.
- Browser acceptance ran in a new Chrome Guest window against a localhost
  synthetic fixture using the real page and components. It verified cancel,
  confirm, unsaved removal, discard restoration, save and reload. The saved
  payload preserved both active keys, the other binding and the pool. The old
  record did not return after reload; no real account or upstream was involved.
- Visual inspection confirmed the shared button/confirmation styles fit the
  existing page. The temporary browser window and both fixture servers were
  closed after validation. No debug instrumentation was added to production code.

## Claude session identity repair — 2026-10-24

The isolation caller prefix used by account affinity also reached Claude's
outbound UUID generation. Confirmed native sessions were therefore hashed again,
while a child request retained its original `parent_session_id`. The repair
extracts the native protocol signal separately and preserves its UUID; local
affinity and non-native session derivation retain their existing caller scope.
Claude continuity and fallback diagnostics independently namespace their local
cache keys with the authorized access scope, so two keys sharing an account and
an explicit session UUID cannot inherit each other's cached request/message IDs.

`TestClaudeIsolationPreservesNativeSessionIdentity` exercises the real manager
and executor with an in-memory upstream and synthetic credentials. Its Messages
and streaming cases reproduced the session/parent mismatch before the repair.
Afterward, Messages, streaming, and count_tokens preserve the native session
header; Messages also preserves the parent/child metadata relationship. The
count_tokens request retains its metadata-free shape. Additional regressions
cover JSON and legacy metadata, header priority, unconfirmed callers, credential
and device identity, and independent continuity for two keys sharing an account.

Validation on `codex/fix-isolation-claude-session`, based on main `602bc44f`:

- Complete executor, authorization, session, and service package tests passed.
- The helper package passed after excluding the same two macOS loopback-binding
  tests documented above; the unrestricted run reproduced only those failures.
- Race checks passed for the new identity/continuity regressions, native session
  helpers, caller affinity, overlapping pools, and account-pool authorization.
- Server build, formatting of changed Go files, and `git diff --check` passed.

All provider requests in the new tests use an in-memory RoundTripper; the broader
regressions use synthetic fixtures. No real account was tested, production was
not modified, and no disabled credential was re-enabled. This repair addresses a
reproduced compatibility defect; it does not establish the reason for the
reported account ban.

## Main integration and PR validation — 2026-11-10

The five isolation feature/runtime/deployment commits were rebased onto published
main `474fdd1d1535a79bc2194f69d8c8f342bd83a396`. This incorporates main's branding,
revoked-key cleanup, memory-retention repair, native Claude session preservation,
and later selective upstream fixes. The previous feature tip is retained in a
local recovery reference and bundle; the three fully merged remote branches were
removed, preserving their local worktrees and all commits reachable from main.

Fresh validation covers implementation `418bf06dba03b38bdc76ed4d55687070d201df3d`:

- Complete `go test -race -shuffle=on -p 4 -count=1 -timeout=240s ./...` passed
  across **68 test packages / 8443 test and subtest passes**, with exactly the
  two previously documented macOS bind-test names excluded using `-skip`.
  The event log also records seven intrinsic optional/platform/race-mode skips;
  these were not converted into passes. The initial compile failure from the
  changed plugin close signature is retained; the complete suite passed after
  correcting the call.
- `go vet ./...` passed without diagnostics. Required Darwin/arm64 and static
  Linux/amd64 server builds passed with Go **1.27.3**.
- **Seven Linux package suites / 4811 test and subtest passes** cover executor
  helpers, native executors, authorization, service integration, Live/Realtime,
  management and plugins. No macOS exclusions were applied. The two opt-in helper
  tests (external public-IP and ClientHello capture) remain skipped. Previously
  excluded binding tests and secondary-loopback subtests passed. Test binaries
  ran as an unprivileged user with temporary storage and synthetic credentials.
  The runner supplies the tracked source/JSON fixtures used by structural and
  concurrency tests; the initial missing-fixture failure is preserved alongside
  the corrected run. The unchanged successful helper run was reused by verified
  binary hash. Production service/configuration/account identities were unchanged
  and temporary test files were removed.
- `VERSION=__CLI_PROXYAPI_VERSION__ bun run verify` passed **372 frontend tests /
  1190 assertions**, ESLint, TypeScript and production build using Bun **1.4.4**.
  Workflow YAML parsing, Go formatting, embed equality and `git diff --check`
  passed. No new browser acceptance or live-provider inference is claimed.
