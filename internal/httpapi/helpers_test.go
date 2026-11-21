package httpapi

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/ordo-systems/wrench/internal/catalog/catalogtest"
	"github.com/ordo-systems/wrench/internal/config"
	"github.com/ordo-systems/wrench/internal/config/configtest"
	"github.com/ordo-systems/wrench/internal/login"
	"github.com/ordo-systems/wrench/internal/store/storetest"
)

// testServer serves cfg on a disposable database with a fixed clock.
func testServer(t *testing.T, cfg *config.Config) (*httptest.Server, *time.Time) {
	t.Helper()
	clock := time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC)
	h := New(Deps{Store: storetest.Open(t), Auth: users, Proxy: http.NotFoundHandler(), Dashboard: http.NotFoundHandler(),
		Config: config.Fixed(cfg), Catalog: catalogtest.For(t, cfg), PublicURL: publicURL, UpstreamReady: func(context.Context) error { return nil },
		now: func() time.Time { return clock }})
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return srv, &clock
}

// tierYAML puts admin, alice and bob in one tier.
const tierYAML = `models:
  glm: {upstream_id: z-ai/glm-5.3, reasoning_effort: low}
budget:
  currency: USD
  timezone: UTC
  global_limits: {5_hours: 8, day: 16, week: 80, month: 100, warn_at_percent: [80, 95]}
tiers:
  basic:
    default: true
    budget: {5_hours: 0.5, day: 1, week: 5, month: 100}
    models:
      - {name: glm, default: true}
users:
  - {email: admin@example.com, role: admin}
  - email: alice@example.com
  - email: bob@example.com
`

func tierConfig(t *testing.T) *config.Config {
	t.Helper()
	return configtest.Load(t, tierYAML)
}

// noDeviceLogin refuses every device login, for login fakes whose tests exercise other routes.
type noDeviceLogin struct{}

func (noDeviceLogin) DeviceCode(context.Context, string) (login.DeviceCodeResponse, error) {
	return login.DeviceCodeResponse{}, &login.DeviceTokenError{Code: "invalid_client"}
}

func (noDeviceLogin) DeviceToken(context.Context, login.DeviceTokenRequest) (login.DeviceTokenResponse, error) {
	return login.DeviceTokenResponse{}, &login.DeviceTokenError{Code: "invalid_client"}
}
