# Self-service OAuth enrollment

Self-service enrollment lets a visitor submit a Claude or Codex OAuth account
without a platform account, inference API key or management key. It adds a separate
invitation and review workflow; existing administrator OAuth login, credential
refresh, account isolation, fingerprint settings and scheduling are unchanged.

## Administrator workflow

1. Open **OAuth → Self-service account authorization** in the management panel.
2. Select Claude or Codex and optionally add an internal note. Generate an invitation.
3. Copy the invitation link and send it privately to the account owner. The link is
   displayed only after creation, expires after 24 hours and permits one submission.
   It cannot be recovered from the list; revoke and replace a lost invitation.
4. Refresh the record list after the visitor submits authorization. Inspect the
   provider, email and account identity, then accept or reject the submission.
5. Acceptance creates a **disabled** credential in Auth Files. Assign the intended
   account pool and review the existing access-isolation policies **before enabling**
   the credential. No pool, API-key binding or routing policy is changed automatically.

Duplicate identities are rejected without modifying existing credentials. This is
deliberate: use the original administrator login flow to renew an existing account.
Acceptance is idempotent. A failed persistence hook leaves an `importing` record;
retry acceptance to complete registration without overwriting the imported file,
newer refresh tokens or subsequent administrator edits. Imported records cannot be
rejected through intake; manage those credentials through the existing account tools.

## Visitor workflow and callback limitation

The visitor opens `/oauth-enroll#<invitation>`, reads the authorization notice and
consents to the system retaining identity and access/refresh credentials for proxy
use after administrator approval. They start authorization and open the provider's
official login page. The server records the consent timestamp. The system does not
receive their provider password.

The existing OAuth clients have fixed registered loopback callbacks:

| Provider | Registered callback |
| --- | --- |
| Claude | `http://localhost:54545/callback` |
| Codex | `http://localhost:1455/auth/callback` |

After authorization, a remote visitor copies the complete callback URL from their
browser address bar into the original enrollment page. A browser connection error
on localhost is expected when no local callback listener is running. The page also
accepts the provider's `code#state` representation. A bare code is not accepted.
Use a browser without a competing local OAuth login on those ports. This flow does
not open a server callback port or interfere with the existing login listeners.

The authorization flow expires after 30 minutes. The server exchanges a submitted
grant once, using the original server-held PKCE verifier and exact state. It finishes
saving the result even if the visitor closes the page. A failed or interrupted
exchange requires a new invitation: blindly replaying a consumed grant is unsafe.
An interrupted exchange is shown as failed after two minutes when the visitor polls.
An expired authorization flow also requires a new invitation.

## Permission and storage boundaries

- The invitation is a limited bearer capability, never a management or inference
  credential. It is stored as a URL fragment, not a query parameter. Only its SHA-256
  secret digest is persisted. Treat the original link as private until it expires or
  is revoked; whoever possesses it can submit an account for that invitation.
- The public page is a separate static application. It does not read management
  browser storage or send platform cookies. It discloses only its own provider,
  coarse submission status and link expiry; account lists, internal notes, tokens,
  PKCE verifiers and provider error details stay private.
- Pending credentials are stored in the **sibling** directory
  `<absolute-resolved-auth-dir>.self-service-oauth`, outside the credential watcher.
  They cannot be selected for inference or refreshed before acceptance. Provider
  credential fields are retained in the same representation as normal OAuth login,
  including tokens, expiry, identity and Claude device IDs. This does not collect
  the user's password or unrelated provider account data.
- Records use private files (`0600`) in a private directory (`0700`). Updates use a
  temporary file, file sync and rename. Protect and back up this directory as
  credential material; it is local storage and is not replicated by optional
  credential storage backends. Encryption at rest follows the host/storage policy.
- Acceptance moves credentials into the ordinary auth storage as disabled and clears
  the pending token copy. Rejection/revocation clears pending credentials and PKCE
  state while retaining the review metadata. It does not revoke a grant at the
  provider. Account owners can revoke access through their provider account.
- One server process must own a given auth/intake directory. In-process mutations
  are serialized; shared-directory multi-process or clustered enrollment is not
  supported. Changing `auth-dir` also changes the corresponding intake location.
- There is a 5,000-record limit, including historical records. No automatic deletion
  or background refresh of pending credentials occurs. Review promptly: access
  tokens can expire while awaiting review. Once accepted and enabled, existing
  account refresh rules apply. Archive closed records administratively, with the
  server stopped and a backup, if the record limit is reached.

## Hosting

Use HTTPS for public invitations. The management panel builds links from its API
base URL, which must be reachable by the visitor. A loopback-only administrator API
base requires replacing the link's origin with the correct public origin.

Forward `/oauth-enroll` and `/oauth-enroll/*` to the same backend and preserve the
external `Host` and `Authorization` headers. Do not proxy these public paths to the
management HTML application. Keep management paths behind the existing management
authentication and any existing reverse-proxy restrictions. Public enrollment is
unavailable when management is disabled or the server runs in Home mode.

The application excludes enrollment requests from detailed request-body/header
logging and adds no-store, no-referrer and restrictive page CSP headers. Configure
the reverse proxy and any external observability agent to avoid recording invitation
authorization headers or callback request bodies as well.

## API

Management endpoints use the existing management authentication:

- `GET /v0/management/oauth-intake`: safe record summaries.
- `POST /v0/management/oauth-intake`: `{provider, label?, ttl_hours?}`; returns the
  safe record and the one-time-displayed `invitation_token`. API TTL range: 1–168 hours.
- `POST /v0/management/oauth-intake/:id/accept`: import disabled or resume an import.
- `POST /v0/management/oauth-intake/:id/reject`: reject pending or revoke an invitation.

Public session endpoints require `Authorization: Bearer <invitation_token>`:

- `GET /oauth-enroll/session`: provider, coarse status and expiry.
- `POST /oauth-enroll/start`: `{consent: true}`; returns provider authorization URL.
- `POST /oauth-enroll/complete`: `{callback: "<full callback URL or code#state>"}`.

Public requests are bounded to 8 KiB and validate same-origin browser requests.
No automatic redirect URI customization, device-code login or open public account
registration is introduced. A fully automatic browser return would require a
separately supported provider OAuth client/redirect arrangement.
