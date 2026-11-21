// Package gitlabtest runs the GitLab mock for tests: accounts held in memory, knobs for failure cases, and an
// Authorize helper that approves a login without the picker.
package gitlabtest

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"

	mockgitlab "github.com/ordo-systems/wrench/internal/mock/gitlab"
)

const (
	ClientID     = "wrench-test-client"
	ClientSecret = "wrench-test-secret"
	SyncToken    = "wrench-test-sync-token"
)

// Account is a GitLab user as the fake reports it.
type Account = mockgitlab.Account

// Fake is a running fake GitLab. Field changes take effect on the next request.
type Fake struct {
	*httptest.Server
	mock *mockgitlab.Mock

	mu       sync.Mutex
	accounts map[int64]*Account
	// StatusDown makes the Users API answer 503.
	StatusDown bool
	// Issuer overrides the iss claim, to test issuer validation.
	Issuer string
	// Audience overrides the aud claim.
	Audience string
	// Nonce overrides the nonce claim.
	Nonce string
}

// New starts a fake GitLab that stops with the test.
func New(t testing.TB) *Fake {
	t.Helper()
	f := &Fake{accounts: map[int64]*Account{}}
	f.Server = httptest.NewUnstartedServer(nil)
	m, err := mockgitlab.New(mockgitlab.Options{
		Issuer: "http://" + f.Listener.Addr().String(), ClientID: ClientID, ClientSecret: ClientSecret,
		SyncToken: SyncToken, Directory: directory{f}, Claims: f.override,
	})
	if err != nil {
		t.Fatal(err)
	}
	f.mock = m
	f.Config.Handler = f.statusDown(m.Handler())
	f.Start()
	t.Cleanup(f.Close)
	return f
}

// Add registers an account and returns it for later changes.
func (f *Fake) Add(a Account) *Account {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.accounts[a.ID] = &a
	return &a
}

// Update changes an account under the fake's lock.
func (f *Fake) Update(id int64, change func(*Account)) {
	f.mu.Lock()
	defer f.mu.Unlock()
	change(f.accounts[id])
}

// Authorize plays the user approving the login as account id at the authorization URL Wrench produced.
// It returns the callback URL GitLab would redirect the browser to.
func (f *Fake) Authorize(t testing.TB, authURL string, id int64) string {
	t.Helper()
	u, err := url.Parse(authURL)
	if err != nil {
		t.Fatal(err)
	}
	cb, err := f.mock.IssueCode(id, u.Query())
	if err != nil {
		t.Fatalf("unexpected authorization request %s: %v", authURL, err)
	}
	return cb
}

// override applies the claim knobs.
func (f *Fake) override(c map[string]any) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.Issuer != "" {
		c["iss"] = f.Issuer
	}
	if f.Audience != "" {
		c["aud"] = f.Audience
	}
	if f.Nonce != "" {
		c["nonce"] = f.Nonce
	}
}

func (f *Fake) statusDown(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		down := f.StatusDown
		f.mu.Unlock()
		if down && strings.HasPrefix(r.URL.Path, "/api/v4/users/") {
			http.Error(w, "unavailable", http.StatusServiceUnavailable)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// directory serves the fake's in-memory accounts.
type directory struct{ f *Fake }

func (d directory) Lookup(id int64) (Account, bool) {
	d.f.mu.Lock()
	defer d.f.mu.Unlock()
	a := d.f.accounts[id]
	if a == nil {
		return Account{}, false
	}
	return *a, true
}

func (d directory) Candidates() []string {
	d.f.mu.Lock()
	defer d.f.mu.Unlock()
	var out []string
	for _, a := range d.f.accounts {
		out = append(out, a.Email)
	}
	return out
}

func (d directory) SignIn(email string) (int64, error) {
	d.f.mu.Lock()
	defer d.f.mu.Unlock()
	for _, a := range d.f.accounts {
		if a.Email == email {
			return a.ID, nil
		}
	}
	return 0, errors.New("no such account")
}
