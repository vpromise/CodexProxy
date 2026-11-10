# Client account isolation

Account isolation gives each client API key access to one named pool of native
Codex or Claude accounts. It is disabled by default. Existing `api-keys` remain a
list of strings, so older clients and key-management workflows continue to work.

## Prepare and enable

1. Create client keys in **Configuration**.
2. Open **Account isolation** and create pools. Select one provider per pool,
   then select its native accounts. Empty pools are valid during preparation.
3. Give each client key a name and optional note, and assign a pool. Several keys
   can use one pool. An account can belong to several pools; those users share
   that account's upstream capacity, quota and cooldowns.
4. Review the counts of unassigned keys and empty pools. Select **Enable account
   isolation** and save. Unassigned keys will be denied. An empty or unavailable
   pool never falls back to an account outside the pool.

Saving uses a configuration revision. If another administrator or a file edit
changed the configuration, reload before saving again. Pool edits preserve
unrelated configuration. Referenced pools cannot be deleted without removing or
changing their key bindings in the same valid configuration.

The management dashboard uses `/v0/management/models` with management
authentication. Its catalog does not depend on a client key's assigned pool.
Client model lists contain only models registered to authorized accounts,
including accounts temporarily unavailable due to quota or cooldown.

## Configuration and management API

```yaml
api-keys:
  - synthetic-client-one
  - synthetic-client-two
access-isolation:
  enabled: false
  pools:
    - id: team-codex
      name: Team Codex
      provider: codex
      accounts: []
    - id: team-claude
      name: Team Claude
      provider: claude
      accounts: []
  keys:
    - key: synthetic-client-one
      name: Codex user
      note: Example only
      pool: team-codex
    - key: synthetic-client-two
      name: Claude user
      pool: team-claude
```

Obtain account identities from authenticated `GET
/v0/management/access-isolation`. The response contains `revision`, `settings`,
`accounts`, and the existing `api-keys`. Account entries include optional
`api_key_preview`, `auth_index`, and `prefix` display fields. Use the masked key
and auth index to match native credentials with the provider page; pool
membership still uses the account's `id`. Write the complete access policy with
`PUT /v0/management/access-isolation` using
`{"revision":"<current revision>","settings":{...}}`. A stale revision returns
409. Invalid providers, new unknown members, duplicate identities, or dangling
pool bindings return 422. Do not use runtime account IDs or filenames as pool
members. A full YAML save also validates the policy.

## Replacing client keys and removing old records

Replacing or regenerating a client key in Configuration removes the old secret
from the active key list. Its saved isolation metadata may remain visible as
**Revoked**; that record does not authenticate the old key. Assign the new key to
its intended pool explicitly.

For a revoked row, choose **Remove record**, confirm, then **Save changes** to
remove its saved name, note and pool binding. **Discard** restores the unsaved
record. This does not delete active client keys, account pools or upstream
accounts. Saving still checks the configuration revision, so concurrent key or
policy edits require a reload instead of being overwritten.

## Account identity and maintenance

Membership combines a persistent local record ID with a provider-specific
fingerprint of a verified native principal: Codex
OAuth account ID, Claude OAuth account UUID plus organization UUID, or a native
upstream API key. The pool editor enrolls existing records by persisting `pool-account-id` in
native key configuration and `pool_account_id` in OAuth files. The identity is
independent of a credential filename, display name,
proxy, model aliases, or routing settings. Refreshing access tokens, renaming an
OAuth file, or verified login to the same account preserves identity. Changing
the upstream API key or logging in to a different account creates a different
identity. Missing identity fields are never filled from an older login record.

Legacy OAuth records without a verifiable identity are excluded from the pool
picker until reauthentication supplies that identity. Email and filename are
not identity evidence. Generic compatibility and plugin accounts cannot be pool
members.

Deleting an account removes its membership. Reimporting it requires assigning it
again. File watcher removal checks the current native inventory, so an ordinary
file rename preserves membership. Deliberate removal followed by reimport must
be allowed to finish processing before reimport; replacing files while the
service is stopped cannot establish a deletion event. Administrators should use
the management deletion API for explicit lifecycle revocation.

## Request boundaries

HTTP, SSE, count/compact operations, Responses WebSocket, Codex Alpha Search,
and Live/Realtime native operations use the same account eligibility filter.
Pins, sticky sessions, retry and failover cannot add accounts to the grant.
Session affinity is scoped to the authenticated caller. Direct plugin executors,
compatibility accounts and Home dispatch cannot bypass isolation.

A generation already accepted keeps its captured grant and may finish. New HTTP
requests and new operations on an existing socket use the latest policy.
A successful management save publishes client authentication and pool grants
before returning, including full YAML saves. Executor reloads run separately and
cannot restore grants from an older configuration snapshot.
Responses continuations that require an unauthorized account are rejected; a
new full request can select from the current pool. Realtime credentials are
bound to their issuer and issuance pool/members. Changing the issuer's pool does
not expand an existing temporary credential. Issue a new credential after
reassignment. Legacy temporary credentials cannot acquire a grant merely because
isolation was subsequently enabled.

Disabling isolation restores legacy client access. Previously issued scoped
Realtime credentials remain constrained and must be reissued when changing
modes. Pool definitions and key labels remain available for later activation.

## Pool IPv4 egress

A pool can optionally bind its inference connections to a local IPv4 address:

```yaml
egress:
  mode: direct-ip
  source-ip: 203.0.113.10
```

The pool editor suggests addresses on the running server's active interfaces.
Omitting `egress`, or using `mode: inherit` without a source address, preserves
existing account/global network settings. A `direct-ip` pool explicitly bypasses
account, global and environment proxies, including an account's Claude bind-IP
setting. This is a local source-address selection, not an HTTP/SOCKS proxy.

The source address must be available in the process's network namespace, with a
working route to the upstream. A container must have access to that address.
An upstream NAT can rewrite the public address; selecting an unassigned public
address cannot configure the gateway's NAT. Check the observed public exit when
provisioning the host. The application does not change interfaces, routes or NAT.

HTTP, SSE, count/compact, images, search and native WebSockets retain the captured
pool route across credential failover. A bind/dial failure returns an egress
error without falling back to another source, proxy or IPv6. Connection and TLS
caches are partitioned by pool and source address. Changing the pool's exit
applies to new operations after a successful save; an active generation may
finish through its captured exit. Responses sockets reconnect for full requests
and require replay when an incremental continuation cannot reuse its connection.
Existing Live/Realtime sockets reject new operations after an exit change and
must reconnect. The WebRTC relay checks outgoing data-channel messages and audio
packets against the current grant and exit, closing a stale session before
forwarding new input. Existing upstream output can finish until that session is
closed.

WebRTC requires the media relay for a `direct-ip` pool. Its upstream media uses
only the selected IPv4, with direct host/STUN candidates; TURN relay candidates
and global advertised-IP overrides are excluded for that upstream leg. When the
media relay is disabled, WebRTC calls from such a pool are rejected rather than
forwarding media outside the selected server route.

Pool egress applies to inference traffic. OAuth login/renewal, account profile
maintenance, management quota checks and global background downloads retain their
existing account/global network settings. Shared accounts can serve pools with
different inference exits; they still share account quota and cooldowns. Do not
interpret this setting as giving a shared account one fixed IP for every type of
network activity. Isolation decision logs include `egress_mode` and
`egress_source_ip` for explicitly routed operations.

## Verification

Automated acceptance uses synthetic keys and OAuth records, temporary storage,
and local HTTP/WebSocket upstreams. No real OAuth login, account, quota probe, or
provider request is necessary. The regression suite covers policy persistence,
stale saves, provider mismatch, fixed accounts, revocation, empty pools,
catalogs, active generation completion, existing socket reauthorization,
Realtime messages, and account rename/delete/reimport.

See [validation notes](access-isolation-validation.md) for executed checks and
outstanding acceptance, including the browser permission block.

## Application logs and isolation audit

The Logs page reads application **file** logs. When file logging is disabled,
use **Enable file logging** on that page (or Configuration). This records future
application logs; it does not import Docker stdout history or enable full request
logging. Keep the configured log-size limit and filesystem permissions in place.

With isolation enabled, `account isolation decision` events record selection and
rejection decisions at info/warn level, independently of debug and request-body
logging. Correlate `request_id` and the process-local `access_operation` counter;
new socket operations receive distinct counters. `policy_revision` fingerprints
the immutable grant snapshot, so active generations retain their original
revision even after a policy save.

`client_ref` is SHA-256 of the authentication provider, a NUL separator, and the
client key. `pool_ref` is SHA-256 of the pool ID. `account_ref` is the native pool
membership identity exposed in the management inventory. These references avoid
logging keys, account filenames, emails, prompts, and caller-supplied session
hints. Selection events are evidence of the selected credential, not proof that
an upstream response completed successfully. Multiple events can belong to a
single operation (selection, HTTP preparation/execution, or a refresh retry).

Codex client catalog refresh validates complete model records without requiring
one fixed model slug. Template selection prefers the established `gpt-5.5`
template when present, otherwise the lowest numeric priority, breaking ties by
slug. Invalid or empty catalogs still leave the last valid snapshot intact.
