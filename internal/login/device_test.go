package login

import (
	"context"
	"errors"
	"net/http"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/ordo-systems/wrench/internal/auth"
	"github.com/ordo-systems/wrench/internal/gitlab/gitlabtest"
	"github.com/ordo-systems/wrench/internal/store"
)

// deviceEnv is a login handler with a controllable clock and alice signed in once, so she exists in the store.
type deviceEnv struct {
	*env_
	clock *time.Time
	alice store.User
}

func setupDevice(t *testing.T) *deviceEnv {
	t.Helper()
	e := setup(t)
	e.fake.Add(gitlabtest.Account{ID: 7, Email: "alice@example.com", EmailVerified: true, State: "active"})
	if resp := e.login(newBrowser(t), "/auth/gitlab/login", 7); resp.StatusCode != http.StatusFound {
		t.Fatalf("login: %d", resp.StatusCode)
	}
	alice, err := e.store.UserByID(context.Background(), 1)
	if err != nil || alice.Email != "alice@example.com" {
		t.Fatalf("alice = %+v, %v", alice, err)
	}
	clock := time.Now().UTC()
	e.handler.now = func() time.Time { return clock }
	return &deviceEnv{env_: e, clock: &clock, alice: alice}
}

func (d *deviceEnv) code(t *testing.T) DeviceCodeResponse {
	t.Helper()
	c, err := d.handler.DeviceCode(context.Background(), "opencode-cli")
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func (d *deviceEnv) poll(code string) (DeviceTokenResponse, string) {
	return d.token(DeviceTokenRequest{GrantType: DeviceCodeGrant, ClientID: "opencode-cli", DeviceCode: code})
}

func (d *deviceEnv) token(req DeviceTokenRequest) (DeviceTokenResponse, string) {
	out, err := d.handler.DeviceToken(context.Background(), req)
	var refused *DeviceTokenError
	if errors.As(err, &refused) {
		return out, refused.Code
	}
	if err != nil {
		d.t.Fatal(err)
	}
	return out, ""
}

func (d *deviceEnv) decide(t *testing.T, userCode string, approve bool) {
	t.Helper()
	if err := d.store.DecideDeviceCode(context.Background(), UserCodeHash(userCode), d.alice.Email, approve, *d.clock); err != nil {
		t.Fatal(err)
	}
}

func (d *deviceEnv) wait(dt time.Duration) { *d.clock = d.clock.Add(dt) }

var userCodeForm = regexp.MustCompile(`^[BCDFGHJKLMNPQRSTVWXZ]{4}-[BCDFGHJKLMNPQRSTVWXZ]{4}$`)

func TestDeviceCode(t *testing.T) {
	d := setupDevice(t)
	c := d.code(t)
	if !userCodeForm.MatchString(c.UserCode) || c.DeviceCode == "" || c.ExpiresIn != 600 || c.Interval != 5 ||
		c.VerificationURIComplete != d.server.URL+"/device?code="+c.UserCode {
		t.Fatalf("code = %+v", c)
	}
	if _, err := d.handler.DeviceCode(context.Background(), "someone-else"); !isRefusal(err, "invalid_client") {
		t.Fatalf("foreign client = %v", err)
	}
}

func TestDeviceTokenStates(t *testing.T) {
	d := setupDevice(t)
	c := d.code(t)
	if _, code := d.poll(c.DeviceCode); code != "authorization_pending" {
		t.Fatalf("pending = %q", code)
	}
	if _, code := d.poll(c.DeviceCode); code != "slow_down" {
		t.Fatalf("fast poll = %q", code)
	}
	d.wait(6 * time.Second)
	// The user may type the code in lower case and without the dash.
	d.decide(t, strings.ToLower(strings.ReplaceAll(c.UserCode, "-", "")), true)
	out, code := d.poll(c.DeviceCode)
	if code != "" || !strings.HasPrefix(out.AccessToken, auth.KeyPrefix) || out.RefreshToken == "" ||
		out.ExpiresIn != int((168*time.Hour).Seconds()) || out.OrgID != "wrench" {
		t.Fatalf("approved = %+v %q", out, code)
	}
	keys, err := d.store.ListClientKeys(context.Background(), "alice@example.com")
	if err != nil || len(keys) != 1 || !strings.HasPrefix(keys[0].DeviceID, "opencode-") || keys[0].DeviceName != "opencode" {
		t.Fatalf("keys = %+v, %v", keys, err)
	}
	d.wait(6 * time.Second)
	if _, code := d.poll(c.DeviceCode); code != "expired_token" {
		t.Fatalf("consumed = %q", code)
	}
	if _, code := d.poll("unknown"); code != "expired_token" {
		t.Fatalf("unknown = %q", code)
	}
	if _, code := d.token(DeviceTokenRequest{GrantType: "password", ClientID: "opencode-cli"}); code != "unsupported_grant_type" {
		t.Fatalf("grant = %q", code)
	}
	if _, code := d.token(DeviceTokenRequest{GrantType: DeviceCodeGrant, ClientID: "x", DeviceCode: c.DeviceCode}); code != "invalid_client" {
		t.Fatalf("client = %q", code)
	}
}

func TestDeviceTokenDenied(t *testing.T) {
	d := setupDevice(t)
	c := d.code(t)
	d.decide(t, c.UserCode, false)
	if _, code := d.poll(c.DeviceCode); code != "access_denied" {
		t.Fatalf("denied = %q", code)
	}
	expiring := d.code(t)
	d.wait(11 * time.Minute)
	if _, code := d.poll(expiring.DeviceCode); code != "expired_token" {
		t.Fatalf("expired = %q", code)
	}
}

func TestDeviceTokenRefusesBlockedOrRemovedUsers(t *testing.T) {
	d := setupDevice(t)
	c := d.code(t)
	d.decide(t, c.UserCode, true)
	if err := d.store.RecordStatus(context.Background(), d.alice.ID, false, *d.clock); err != nil {
		t.Fatal(err)
	}
	if _, code := d.poll(c.DeviceCode); code != "access_denied" {
		t.Fatalf("blocked = %q", code)
	}
	if keys, _ := d.store.ListClientKeys(context.Background(), "alice@example.com"); len(keys) != 0 {
		t.Fatalf("a blocked user got a key: %+v", keys)
	}
}

func TestDeviceTokenRefresh(t *testing.T) {
	d := setupDevice(t)
	c := d.code(t)
	d.decide(t, c.UserCode, true)
	first, _ := d.poll(c.DeviceCode)
	refresh := func(token string) (DeviceTokenResponse, string) {
		return d.token(DeviceTokenRequest{GrantType: RefreshGrant, ClientID: "opencode-cli", RefreshToken: token})
	}
	second, code := refresh(first.RefreshToken)
	if code != "" || second.AccessToken == first.AccessToken || second.RefreshToken == first.RefreshToken {
		t.Fatalf("refresh = %+v %q", second, code)
	}
	if _, err := d.store.LookupClientKey(context.Background(), auth.Hash(first.AccessToken), *d.clock); err == nil {
		t.Fatal("the old key still works")
	}
	if _, code := refresh(first.RefreshToken); code != "invalid_grant" {
		t.Fatalf("reused refresh = %q", code)
	}
	if err := d.store.RecordStatus(context.Background(), d.alice.ID, false, *d.clock); err != nil {
		t.Fatal(err)
	}
	if _, code := refresh(second.RefreshToken); code != "invalid_grant" {
		t.Fatalf("blocked refresh = %q", code)
	}
}

func TestSafeReturn(t *testing.T) {
	tests := map[string]string{
		"/device?code=ABCD-EFGH": "/device?code=ABCD-EFGH",
		"//evil.example":         "/",
		"https://evil.example":   "/",
		`/\evil.example`:         "/",
		// Browsers drop tabs and newlines from URLs, which would turn these into //evil.example.
		"/\t/evil.example":   "/",
		"/\n/evil.example":   "/",
		"/\r/evil.example":   "/",
		"/device\x7f":        "/",
		"/device?code=\x00x": "/",
		"":                   "/",
		"device":             "/",
	}
	for in, want := range tests {
		if got := safeReturn(in); got != want {
			t.Errorf("safeReturn(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestWebLoginReturnsToThePage(t *testing.T) {
	e := setup(t)
	e.fake.Add(gitlabtest.Account{ID: 7, Email: "alice@example.com", EmailVerified: true, State: "active"})
	resp := e.login(newBrowser(t), "/auth/gitlab/login?return=%2Fdevice%3Fcode%3DABCD-EFGH", 7)
	if resp.StatusCode != http.StatusFound || resp.Header.Get("Location") != "/device?code=ABCD-EFGH" {
		t.Fatalf("callback: %d %s", resp.StatusCode, resp.Header.Get("Location"))
	}
}

func isRefusal(err error, code string) bool {
	var refused *DeviceTokenError
	return errors.As(err, &refused) && refused.Code == code
}
