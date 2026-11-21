# Authentication

Who may use Wrench, how people sign in from the dashboard and opencode, and how access is revoked.

## Users and identity

- The allowlist is `users[].email`, normalized by trimming whitespace and lowercasing; duplicates after normalization
  are rejected. The default role is `user`; only `role: admin` grants administrative access. Administrators are still
  subject to tier models, their user budget and the global budget.
- Sign-in is GitLab OIDC Authorization Code with PKCE, scopes `openid profile email`, and the callback
  `<WRENCH_PUBLIC_URL>/auth/gitlab/callback`. Wrench validates issuer, audience, signature, nonce and state, and
  requires an email claim with `email_verified: true`. GitLab documents conditions on email-claim availability, so
  check the company instance before rollout.
- The first sign-in binds the email to the stable `(issuer, sub)`; later sign-ins must match it. An existing email is
  never silently rebound to another subject, and an email change never resets spend.
- Account state comes from `GET /api/v4/users/:id` with a separate `read_api` token (no admin token). Only
  `state=active` with `locked=false` is admitted, and a new account needs one successful check before it gets a key.
  The email claim is revalidated on every sign-in because the Users API does not reliably expose private emails.

Sources: [GitLab OIDC](https://docs.gitlab.com/integration/openid_connect_provider/),
[GitLab Users API](https://docs.gitlab.com/api/users/#retrieve-a-single-user).

## Sign-in and keys

- Sign-in is one path, through GitLab; there is no development token or password.
- opencode: `opencode auth login opencode --method device --answer server=<wrench>` starts a device login
  (RFC 8628). The user opens `/device?code=XXXX-XXXX` (signing in with GitLab first if needed, then returning there),
  checks the code and approves it; only a dashboard session with a fresh GitLab status may approve. opencode then
  receives a `wrk_` device key valid for `WRENCH_CLIENT_KEYS_TTL` and a refresh token; each refresh replaces the key
  and the refresh token, and a reused one is refused. Blocked or removed users get no key. Scripts and the live
  check take a ready key in `WRENCH_TOKEN`. See [agent/opencode](../agent/opencode/README.md).
- One device has one active key; other devices' keys stay valid until expiry or revocation, and all share one user
  budget. Clients receive only Wrench device keys, never GitLab tokens or upstream credentials.
- Anyone can start a device login, so the server keeps at most 1000 unexpired codes and answers `503` beyond that.
  A flood therefore blocks new logins for up to the 10-minute code lifetime; rate-limit `/auth/device/*` per client
  address at the reverse proxy if the server is reachable from untrusted networks.
- Dashboard: "Sign in with GitLab" sets a `__Host-wrench_session` cookie (Secure, HttpOnly, SameSite=Lax) for
  `WRENCH_WEB_SESSION_TTL`. Cookie-authenticated mutations must carry the public `Origin`.
- `WRENCH_HTTP_ONLY=true` serves deployments without TLS: cookies become `wrench_session` / `wrench_login` without
  Secure, and the server logs a warning at every start. Never use it in production.
- Every request re-checks the key or session, the YAML allowlist (the role comes from YAML) and the stored GitLab
  state. Inference also needs GitLab status confirmed within three `WRENCH_GITLAB_SYNC_INTERVAL`s; reading data does
  not.
- A background sync checks every account each `WRENCH_GITLAB_SYNC_INTERVAL`. A confirmed block, lock, deactivation or
  removal blocks the user and revokes every key and session in one transaction. Timeouts and 5xx change nothing.
  Re-enabling in GitLab requires a new sign-in.
- Users list and revoke their own device keys on the Keys tab, whose New device key button shows the login command;
  administrators see every key (`?user=*`), can filter by user (`?user=<email>`) and can revoke every credential of a
  user on the Users tab.

## Roles in the dashboard

- **Everyone** gets Usage (own tier's models, own budget windows and usage cards), Sessions and Keys, each limited to
  their own data, and the top-bar status (whether requests run and why they are paused).
- **Administrators** also get Overview (system status, every model, the shared budget, tiers and everyone's usage),
  Users and Alerts. Sessions and Keys can show every user or one, and Usage can show one user or all users.
- The dashboard updates live over `GET /api/events`; there is no Refresh button.
- The API enforces the same boundary; see [HTTP API](api.md#whose-data).

Register the GitLab application with scopes `openid profile email`, the callback
`<WRENCH_PUBLIC_URL>/auth/gitlab/callback`, and a confidential client. The sync token needs `read_api`.
