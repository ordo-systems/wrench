# Tech stack

- Go module `github.com/ordo-systems/wrench`, `go 1.27`; build with `CGO_ENABLED=0`.
- Libraries: cobra, pgx/v5, yaml.v3 (known fields, duplicate keys rejected), go-oidc/v3, fsnotify; oapi-codegen
  pinned by a `tool` directive.
- PostgreSQL 17; migrations are embedded SQL files in `internal/store/migrations`, applied by the app at startup.
- Dashboard: Vue 3, vue-router 4, ECharts 5 via vue-echarts, lucide-vue-next, Vitest; built into the Go binary.
- Upstream: OpenRouter only, called directly with `net/http`.
- Auth: GitLab OIDC, verified-email allowlist from `config.yaml`.
- Client: unmodified opencode 2.3.x through its OpenCode Console integration.
