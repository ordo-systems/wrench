package web

import (
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"testing/fstest"
)

func serve(t *testing.T, files fstest.MapFS, path string) (int, string) {
	t.Helper()
	rec := httptest.NewRecorder()
	handler(files).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
	body, _ := io.ReadAll(rec.Body)
	return rec.Code, string(body)
}

func TestSPA(t *testing.T) {
	files := fstest.MapFS{
		"index.html":     {Data: []byte("<app>")},
		"assets/app.js":  {Data: []byte("js")},
		"assets/app.css": {Data: []byte("css")},
	}
	cases := []struct {
		path string
		code int
		body string
	}{
		{"/", 200, "<app>"},
		{"/assets/app.js", 200, "js"},
		{"/sessions/abc", 200, "<app>"},
		{"/assets/missing.js", 404, ""},
	}
	for _, c := range cases {
		code, body := serve(t, files, c.path)
		if code != c.code || (c.body != "" && body != c.body) {
			t.Errorf("%s: %d %q", c.path, code, body)
		}
	}
}

func TestUnbuiltDashboard(t *testing.T) {
	if code, _ := serve(t, fstest.MapFS{}, "/requests"); code != http.StatusServiceUnavailable {
		t.Fatalf("code %d", code)
	}
}
