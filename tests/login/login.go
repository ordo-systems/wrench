// Package login signs a test user in to Wrench the way opencode does: the device login, approved on the dashboard
// after a sign-in through the GitLab mock's account picker. End-to-end tests, the live check login and the backup
// drill use it.
package login

import (
	"context"
	"fmt"
	"html"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/ordo-systems/wrench/tests/internal/wrenchapi"
)

// hiddenInput matches the picker's hidden authorization parameters.
var hiddenInput = regexp.MustCompile(`<input type="hidden" name="([^"]+)" value="([^"]*)">`)

// DeviceKey signs email in through the device login opencode uses and returns the device key.
func DeviceKey(ctx context.Context, wrenchURL, gitlabURL, email string) (string, error) {
	key, _, err := DeviceTokens(ctx, wrenchURL, gitlabURL, email)
	return key, err
}

// DeviceTokens runs the whole device login as email: a code pair from /auth/device/code, the approval on the
// dashboard (ApproveDevice) and one poll of /auth/device/token. It returns the device key and its refresh token.
func DeviceTokens(ctx context.Context, wrenchURL, gitlabURL, email string) (string, string, error) {
	api, err := wrenchapi.NewClientWithResponses(wrenchURL)
	if err != nil {
		return "", "", fmt.Errorf("login: device: %w", err)
	}
	code, err := api.StartDeviceLoginWithResponse(ctx, wrenchapi.DeviceCodeRequest{ClientId: "opencode-cli"})
	if err != nil {
		return "", "", fmt.Errorf("login: device code: %w", err)
	}
	if code.JSON200 == nil {
		return "", "", fmt.Errorf("login: device code: status %d", code.StatusCode())
	}
	if err := ApproveDevice(ctx, wrenchURL, gitlabURL, email, code.JSON200.UserCode); err != nil {
		return "", "", err
	}
	tok, err := api.DeviceTokenWithResponse(ctx, wrenchapi.DeviceTokenRequest{ClientId: "opencode-cli",
		GrantType: "urn:ietf:params:oauth:grant-type:device_code", DeviceCode: &code.JSON200.DeviceCode})
	if err != nil {
		return "", "", fmt.Errorf("login: device token: %w", err)
	}
	if tok.JSON200 == nil {
		return "", "", fmt.Errorf("login: device token: status %d: %s", tok.StatusCode(), strings.TrimSpace(string(tok.Body)))
	}
	return tok.JSON200.AccessToken, tok.JSON200.RefreshToken, nil
}

// ApproveDevice approves a device login's user code as email, the way a person does: the dashboard sign-in through
// the GitLab mock picker, which returns to /device, then the Approve button (POST /api/device with the session
// cookie and the public Origin). The public origin is read from the redirect URI the sign-in hands to GitLab.
func ApproveDevice(ctx context.Context, wrenchURL, gitlabURL, email, userCode string) error {
	b := &browser{ctx: ctx, client: &http.Client{Timeout: 30 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}
	start := wrenchURL + "/auth/gitlab/login?" + url.Values{"return": {"/device?code=" + userCode}}.Encode()
	resp, err := b.do(http.MethodGet, start, nil)
	if err != nil {
		return err
	}
	authorize, err := url.Parse(resp.location)
	if err != nil || resp.location == "" {
		return fmt.Errorf("login: device sign-in: status %d", resp.status)
	}
	public, err := url.Parse(authorize.Query().Get("redirect_uri"))
	if err != nil || public.Host == "" {
		return fmt.Errorf("login: device sign-in: no redirect_uri in %s", authorize.Path)
	}
	if err := b.pick(resp.location, wrenchURL, gitlabURL, email); err != nil {
		return err
	}
	body := strings.NewReader(`{"user_code":"` + userCode + `","decision":"approve"}`)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, wrenchURL+"/api/device", body)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Origin", public.Scheme+"://"+public.Host)
	for _, c := range b.cookies {
		req.AddCookie(&http.Cookie{Name: c.Name, Value: c.Value})
	}
	decided, err := b.client.Do(req)
	if err != nil {
		return fmt.Errorf("login: approve: %w", err)
	}
	defer decided.Body.Close()
	if decided.StatusCode != http.StatusNoContent {
		msg, _ := io.ReadAll(decided.Body)
		return fmt.Errorf("login: approve: status %d: %s", decided.StatusCode, strings.TrimSpace(string(msg)))
	}
	return nil
}

// pick completes a dashboard sign-in from its GitLab authorization URL: the mock picker as email, then Wrench's
// callback, which starts the session.
func (b *browser) pick(authorizeURL, wrenchURL, gitlabURL, email string) error {
	picker, err := rebase(authorizeURL, gitlabURL)
	if err != nil {
		return err
	}
	resp, err := b.do(http.MethodGet, picker, nil)
	if err != nil {
		return err
	}
	if resp.status != http.StatusOK {
		return fmt.Errorf("login: picker: status %d", resp.status)
	}
	form := url.Values{}
	for _, m := range hiddenInput.FindAllStringSubmatch(resp.body, -1) {
		if !form.Has(m[1]) {
			form.Set(m[1], html.UnescapeString(m[2]))
		}
	}
	form.Set("email", email)
	if resp, err = b.do(http.MethodPost, gitlabURL+"/oauth/authorize", form); err != nil {
		return err
	}
	callback, err := rebase(resp.location, wrenchURL)
	if err != nil {
		return fmt.Errorf("login: picker post: status %d: %w", resp.status, err)
	}
	if resp, err = b.do(http.MethodGet, callback, nil); err != nil {
		return err
	}
	if resp.status != http.StatusFound {
		return fmt.Errorf("login: callback: status %d: %s", resp.status, strings.TrimSpace(resp.body))
	}
	return nil
}

// browser carries cookies by hand: Wrench's cookies are Secure, which Go's cookie jar drops over plain HTTP.
type browser struct {
	ctx     context.Context
	client  *http.Client
	cookies []*http.Cookie
}

// reply is what a sign-in step needs from a response; bodies are read and closed inside do.
type reply struct {
	status   int
	location string
	body     string
}

func (b *browser) do(method, target string, form url.Values) (reply, error) {
	var body io.Reader
	if form != nil {
		body = strings.NewReader(form.Encode())
	}
	req, err := http.NewRequestWithContext(b.ctx, method, target, body)
	if err != nil {
		return reply{}, err
	}
	if form != nil {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	for _, c := range b.cookies {
		req.AddCookie(&http.Cookie{Name: c.Name, Value: c.Value})
	}
	resp, err := b.client.Do(req)
	if err != nil {
		return reply{}, fmt.Errorf("login: %s %s: %w", method, req.URL.Path, err)
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	b.cookies = append(b.cookies, resp.Cookies()...)
	return reply{status: resp.StatusCode, location: resp.Header.Get("Location"), body: string(data)}, nil
}

// rebase points a browser-facing URL at base, keeping its path and query.
func rebase(raw, base string) (string, error) {
	if raw == "" {
		return "", fmt.Errorf("no redirect")
	}
	u, err := url.Parse(raw)
	if err != nil {
		return "", err
	}
	b, err := url.Parse(base)
	if err != nil {
		return "", err
	}
	u.Scheme, u.Host = b.Scheme, b.Host
	return u.String(), nil
}
