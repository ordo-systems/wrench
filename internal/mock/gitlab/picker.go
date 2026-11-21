package gitlab

import (
	"html/template"
	"net/http"
	"net/url"
)

// authParams are the authorization request parameters the picker carries from the GET to the POST.
var authParams = []string{"client_id", "response_type", "redirect_uri", "state", "nonce", "scope",
	"code_challenge", "code_challenge_method"}

var pickerPage = template.Must(template.New("picker").Parse(`<!doctype html>
<html lang="en">
<head><meta charset="utf-8"><title>Sign in to GitLab (mock)</title>
<style>body{font-family:sans-serif;max-width:28rem;margin:4rem auto}form{margin:.5rem 0}button{width:100%;padding:.5rem}
.error{color:#b00}</style></head>
<body>
<h1>Sign in to GitLab (mock)</h1>
<p>Demo only: any account below signs in without a password.</p>
{{if .Error}}<p class="error">{{.Error}}</p>{{end}}
{{range .Candidates}}<form method="post" action="/oauth/authorize">
{{range $.Hidden}}<input type="hidden" name="{{.Name}}" value="{{.Value}}">
{{end}}<button name="email" value="{{.}}">{{.}}</button>
</form>
{{end}}<form method="post" action="/oauth/authorize">
{{range .Hidden}}<input type="hidden" name="{{.Name}}" value="{{.Value}}">
{{end}}<label>Other email <input name="email" type="email"></label>
<button>Sign in</button>
</form>
</body>
</html>
`))

type hiddenField struct{ Name, Value string }

// authorize renders the account picker for a valid authorization request.
func (m *Mock) authorize(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	if err := m.checkAuthorization(q); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	m.renderPicker(w, q, "")
}

// approve signs in the chosen email and redirects back to the client with a code.
func (m *Mock) approve(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	q := url.Values{}
	for _, p := range authParams {
		q.Set(p, r.PostForm.Get(p))
	}
	if err := m.checkAuthorization(q); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	id, err := m.o.Directory.SignIn(r.PostForm.Get("email"))
	if err != nil {
		m.renderPicker(w, q, err.Error())
		return
	}
	cb, err := m.IssueCode(id, q)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	http.Redirect(w, r, cb, http.StatusFound)
}

func (m *Mock) renderPicker(w http.ResponseWriter, q url.Values, message string) {
	hidden := make([]hiddenField, 0, len(authParams))
	for _, p := range authParams {
		hidden = append(hidden, hiddenField{p, q.Get(p)})
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	_ = pickerPage.Execute(w, struct {
		Candidates []string
		Hidden     []hiddenField
		Error      string
	}{m.o.Directory.Candidates(), hidden, message})
}
