# Sign-in and HTTP-only mode

Part of [server requirements](01-server.md): one sign-in path through GitLab, no development access, and an
HTTP-only mode for deployments without TLS. Clients get keys through the device login
([console design](06-opencode-console.md)).

## Decisions

- There is no development token or password. Demos and tests sign in through the [GitLab mock](01-server-mocks.md).
- The dashboard signs in with "Sign in with GitLab" (`GET /auth/gitlab/login?return=<path>`); the callback creates a
  dashboard session and returns to a same-origin path. The device approval page uses the same flow.
- `WRENCH_HTTP_ONLY` (`true`/`false`, default `false`) only changes cookies; it is not tied to `WRENCH_PUBLIC_URL`.
  When `true`, the server logs `HTTP-only mode: session cookies are not Secure; never use it in production` at
  startup.

## Cookies

`auth.Cookies` owns cookie names and attributes; login sets them and the authentication middleware reads them.

| Mode                    | Session cookie          | Login binding cookie  | Attributes                             |
| ----------------------- | ----------------------- | --------------------- | -------------------------------------- |
| default                 | `__Host-wrench_session` | `__Host-wrench_login` | Secure, HttpOnly, SameSite=Lax, Path=/ |
| `WRENCH_HTTP_ONLY=true` | `wrench_session`        | `wrench_login`        | HttpOnly, SameSite=Lax, Path=/         |

The `__Host-` prefix requires `Secure`, so the HTTP-only names drop it.

## Scripts and tests

- `tests/login` walks the GitLab sign-in through the mock picker and the device login, returning a `wrk_` key;
  `tests/cmd/wrench-login` prints one for scripts.
- End-to-end tests obtain one key per run in `TestMain`; one test drives the browser sign-in.
- `WRENCH_TOKEN` passes a ready `wrk_` key to the live check and scripts.
