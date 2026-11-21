# OpenAPI contract

The Wrench JSON API is described once, in an OpenAPI document, and everything that speaks it is generated from that
document: the server's routing, request decoding and response types, the dashboard's TypeScript types, and the Go
client of the end-to-end tests and tools.

## Decisions

- The source of truth is `api/openapi.yaml`, OpenAPI 3.0.3 (oapi-codegen's 3.1 support is partial). The server
  embeds it (`api.Document`).
- In the document: every `/api/*` route, `GET /healthz`, `GET /readyz` and the device login
  (`POST /auth/device/code`, `POST /auth/device/token`). Not in the document: `POST /v1/chat/completions` (an
  OpenAI-compatible proxy whose contract is upstream's), the browser `/auth/gitlab/*` routes and `POST /auth/logout`
  (redirects and cookies), `GET /zen/go/v1/usage` and the embedded dashboard files. They stay hand-routed.
- Generated code is committed. Docker images and CI builds never run a generator; `make check-generated` fails when
  the committed output differs from what the document produces.
- A route whose collaborator is not configured answers `404` with the `Error` body instead of the mux's plain-text
  `404`.
- Out of scope: runtime validation against the document, serving the document over HTTP, generated client code in
  the dashboard (types only), API versioning.
- Accepted generator effects: JSON object keys come in the generated structs' order; optional fields absent on the
  server side are omitted rather than sent as `null`.

## Generators

| Output          | Generator                                                     | Location                                    | Contents                                   |
| --------------- | ------------------------------------------------------------- | ------------------------------------------- | ------------------------------------------ |
| Server          | `oapi-codegen` v2, pinned with a `tool` directive in `go.mod` | `api/api.gen.go`                            | models, `std-http-server`, `strict-server` |
| Go client       | the same `oapi-codegen`                                       | `tests/internal/wrenchapi/wrenchapi.gen.go` | models, `client` (with responses)          |
| Dashboard types | `openapi-typescript`, pinned devDependency of `webapp`        | `webapp/src/api.gen.ts`                     | `paths`, `components` types only           |

Generator configuration files sit next to their outputs (`oapi-codegen.yaml`). Package `api` holds the document,
the generated code and `spec.go`, which embeds the document; nothing else in it is hand-written. The black-box tests
get their own generated client and never import server packages (`make check-blackbox`).

## Document conventions

- Every operation has an `operationId` in lowerCamelCase (`getBudget`, `listSessions`); it names the generated Go
  method and the TypeScript operation.
- Shared shapes are `components/schemas`; amounts are `type: string` (exact decimal USD), timestamps are
  `type: string, format: date-time`, enumerations are `enum`s.
- One error schema: `{error: {message, type?, code?}}`. Every response the server can send is listed, all
  non-success bodies as `Error`. The device login answers RFC 8628 error objects instead.
- Two security schemes: `deviceKey` (`http`, `bearer`) and `session` (`apiKey` in cookie). Authenticated operations
  declare `security: [{deviceKey: []}, {session: []}]`; `POST /api/device` declares only `session`. Operations
  without `security` are public: `getHealthz`, `getReadyz` and the device login.
- Operations for administrators only declare `x-wrench-admin: true`.

## Server

- `internal/httpapi` implements `api.StrictServerInterface`; each handler takes the generated request object and
  returns a generated response object.
- `httpapi.New` mounts the generated handler on one `http.ServeMux` next to the hand-routed `/v1` proxy, `/auth/*`
  routes, the `GET /api/` 404 and the dashboard.

### Authentication and authorization

- At startup `httpapi` reads the embedded document and registers every operation that declares `security` on a
  private `http.ServeMux`. A guard in front of the generated routes asks that mux whether it has a pattern for the
  request; if so, the request is authenticated before any parameter or body is parsed, so an unauthenticated request
  gets `401`, never `400`.
- Every authenticated operation whose method is not `GET` runs the same-origin check: cookie-authenticated requests
  must carry the public `Origin`.
- Operations with `x-wrench-admin: true` are refused by the same guard with `403` (`administrator role required`)
  before the request is parsed. Scoping of lists is described in [dashboard roles](05-dashboard-roles.md).

### Errors

- Handlers return typed error responses with the `Error` body, through one small constructor.
- A malformed JSON body or an unparsable query or path parameter is `400` with the `Error` body.
- A Go error returned by a handler is `500` with the `Error` body and is logged; storage failures answer `503`
  explicitly.

## Clients

- **Dashboard.** `webapp/src/api.ts` aliases generated types (`export type Session =
  components["schemas"]["Session"]`) and owns the fetch wrapper. `vue-tsc` in `build` and `lint` checks the
  dashboard against the document.
- **Go.** `tests` (e2e, livecheck, `login`, `cmd/wrench-login`) uses the generated client for documented routes;
  `/v1/chat/completions` and the browser `/auth/*` routes stay hand-written.

## Tooling

- `make generate` runs every generator (needs `npm ci` in `webapp`).
- `make check-generated`, part of `make lint`, regenerates and fails when that changed or created a generated file.
- Generated files carry the standard `Code generated ... DO NOT EDIT.` header, so linters and coverage skip them.
- To change the API: edit the document, run `make generate`, implement the new or changed interface method, and
  commit the document and the generated files together.
