# Claude Responses replay

Claude can return multiple adjacent signed thinking blocks before a tool call. The Responses translation returns these as separate items. A later tool turn can recover an omitted block from the original Claude response when it can verify the owner, upstream binding, conversation prefix and tool pairing.

This feature applies to Responses requests served by the native Claude executor in an explicitly opted-in, isolated Claude account pool. Each request uses the authenticated caller and pool grants plus the selected account, endpoint and resolved upstream model. The shared database can serve several proxy nodes when those values and the replay key agree. The existing account selection and pool configuration determine where inference runs.

## Configuration

Configure the following environment variables on the participating proxy processes. The default is off. Start with one named pool and a dedicated PostgreSQL database or schema on a private connection. Use a separately authorized database role with access to the versioned replay table and its indexes. Remote connections should use certificate verification. The service creates the table and indexes when it opens the store. A database initialization error prevents an explicitly enabled process from starting.

| Environment variable | Value |
| --- | --- |
| `CODEXPROXY_CLAUDE_REPLAY_POOLS` | Comma-separated account isolation pool IDs. Each pilot pool must be a native Claude pool with an authenticated client key. |
| `CODEXPROXY_CLAUDE_REPLAY_POSTGRES_DSN` | PostgreSQL connection string for this feature. |
| `CODEXPROXY_CLAUDE_REPLAY_KEY` | Base64-encoded random 32-byte key, for example from `openssl rand -base64 32`. Use the same key on authorized replicas and distinct keys for separate trust domains. |

Keep the DSN and replay key in the service's secret environment. A key change invalidates receipts issued with the previous key. One upstream-facing client key identifies one caller from CodexProxy's perspective. A gateway serving isolated groups should use separate CodexProxy keys for those groups.

## Data and failure handling

- Each eligible completed response stores one encrypted assistant block array, its scope binding, and a digest of the preceding Claude messages, system and tools before transport signing. The application stores neither raw earlier user turns nor OAuth credentials in this table. Verified replay uses the original signed blocks and the current matching visible blocks. Cache annotations remain on current visible content.
- The authenticated reasoning item reference uses the existing Responses item ID field. The normal response ID, tool call ID and reasoning payload stay usable by clients. If both reasoning items were omitted, a tool call ID can locate a live record scoped to that caller, provided the transcript still contains a matching tool call and prefix. Clients still pass the visible conversation. A bare `previous_response_id` cannot reconstruct the entire transcript.
- The retention ceiling is one hour, with at most 128 records per pool and at most 512 KiB of encoded data per record. Heavy pools can hit capacity before the time limit. Expired records are invisible to reads and enabled processes remove them periodically. If every participating process is stopped, configure database maintenance to remove expired ciphertext.
- If a recognized tool continuation cannot be verified, including after expiry or a storage error, it returns request-scoped `claude_replay_unavailable`. The caller can resend the original conversation or start a new tool turn. Requests with no recognized continuation can use the existing compatibility path. Restored request bodies are omitted from raw upstream request logging.
- Removing the pilot pool from the environment and restarting reverts that pool to its existing compatibility path. Existing encrypted rows expire independently. Coordinate key and configuration changes across nodes before a pilot.

## Verification

Use an isolated PostgreSQL instance and set `CODEXPROXY_TEST_CLAUDE_REPLAY_POSTGRES_DSN` to run the optional integration cases in `./internal/claudereplay` and `./internal/runtime/executor`. The tests use synthetic Claude signatures, callers and tool results. The latter package runs two independent stores and managers against a local Claude SSE fixture.
