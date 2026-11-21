package gitlab

import (
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"testing"
)

// noRedirect returns redirects to the test instead of following them.
var noRedirect = &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}

func body(t *testing.T, resp *http.Response) string {
	t.Helper()
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return string(b)
}

// hiddenFields reads the hidden inputs of the first form on a picker page.
func hiddenFields(page string) url.Values {
	out := url.Values{}
	for _, m := range regexp.MustCompile(`<input type="hidden" name="([^"]+)" value="([^"]*)">`).FindAllStringSubmatch(page, -1) {
		if out.Has(m[1]) {
			break
		}
		out.Set(m[1], htmlUnescape(m[2]))
	}
	return out
}

func htmlUnescape(s string) string {
	return strings.NewReplacer("&amp;", "&", "&lt;", "<", "&gt;", ">", "&#34;", `"`, "&#39;", "'", "&#43;", "+").Replace(s)
}

func TestPickerLists(t *testing.T) {
	_, srv := newMock(t, Options{Directory: ConfigDirectory([]string{"user1@example.com", "<b>x@y</b>"})})
	resp, err := http.Get(srv.URL + "/oauth/authorize?" + authQuery().Encode())
	if err != nil {
		t.Fatal(err)
	}
	page := body(t, resp)
	if resp.StatusCode != http.StatusOK || !strings.Contains(page, "user1@example.com") {
		t.Fatalf("status %d page %s", resp.StatusCode, page)
	}
	if strings.Contains(page, "<b>x@y</b>") || !strings.Contains(page, "&lt;b&gt;x@y&lt;/b&gt;") {
		t.Errorf("candidate not escaped: %s", page)
	}
	if hiddenFields(page).Get("state") != "st" {
		t.Errorf("state not carried: %v", hiddenFields(page))
	}
}

func TestPickerRejectsBadRequest(t *testing.T) {
	_, srv := newMock(t, Options{Directory: ConfigDirectory(nil)})
	q := authQuery()
	q.Set("client_id", "other")
	resp, err := http.Get(srv.URL + "/oauth/authorize?" + q.Encode())
	if err != nil {
		t.Fatal(err)
	}
	if _ = body(t, resp); resp.StatusCode != http.StatusBadRequest {
		t.Errorf("status %d", resp.StatusCode)
	}
}

func TestPickerPost(t *testing.T) {
	_, srv := newMock(t, Options{Directory: ConfigDirectory([]string{"user1@example.com"})})
	form := authQuery()
	form.Set("email", "user1@example.com")
	resp, err := noRedirect.PostForm(srv.URL+"/oauth/authorize", form)
	if err != nil {
		t.Fatal(err)
	}
	_ = body(t, resp)
	loc, _ := url.Parse(resp.Header.Get("Location"))
	if resp.StatusCode != http.StatusFound || loc.Host != "app" || loc.Query().Get("state") != "st" {
		t.Fatalf("status %d location %s", resp.StatusCode, loc)
	}
	status, out := exchange(t, srv, loc.Query().Get("code"), "secret", verifier)
	if status != http.StatusOK || idClaims(t, out["id_token"].(string))["email"] != "user1@example.com" {
		t.Errorf("exchange %d %v", status, out)
	}
}

func TestPickerPostRejects(t *testing.T) {
	_, srv := newMock(t, Options{Directory: ConfigDirectory([]string{"user1@example.com"})})
	tampered := authQuery()
	tampered.Set("client_id", "other")
	tampered.Set("email", "user1@example.com")
	resp, err := noRedirect.PostForm(srv.URL+"/oauth/authorize", tampered)
	if err != nil {
		t.Fatal(err)
	}
	if _ = body(t, resp); resp.StatusCode != http.StatusBadRequest {
		t.Errorf("tampered: status %d", resp.StatusCode)
	}
	empty := authQuery()
	empty.Set("email", "")
	resp, err = noRedirect.PostForm(srv.URL+"/oauth/authorize", empty)
	if err != nil {
		t.Fatal(err)
	}
	page := body(t, resp)
	if resp.StatusCode != http.StatusOK || !strings.Contains(page, "user1@example.com") || !strings.Contains(page, `class="error"`) {
		t.Errorf("empty email: status %d page %s", resp.StatusCode, page)
	}
}

func TestUsersAPIAfterRestart(t *testing.T) {
	_, srv := newMock(t, Options{Directory: ConfigDirectory(nil)})
	req, _ := http.NewRequest(http.MethodGet, srv.URL+"/api/v4/users/12345", nil)
	req.Header.Set("PRIVATE-TOKEN", "sync")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	if b := body(t, resp); resp.StatusCode != http.StatusOK || !strings.Contains(b, `"state":"active"`) {
		t.Errorf("unseen account: %d %s", resp.StatusCode, b)
	}
}
