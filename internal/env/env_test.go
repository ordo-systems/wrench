package env

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

func validVars() map[string]string {
	return map[string]string{
		"WRENCH_LISTEN_ADDR":                   "0.0.0.0:8080",
		"WRENCH_PUBLIC_URL":                    "https://wrench.example.com",
		"WRENCH_WEB_SESSION_TTL":               "12h",
		"WRENCH_CLIENT_KEYS_TTL":               "168h",
		"WRENCH_POSTGRES_DSN":                  "postgres://u:p@db:5432/wrench",
		"WRENCH_GITLAB_URL":                    "https://gitlab.example.com",
		"WRENCH_GITLAB_CLIENT_ID":              "client-id",
		"WRENCH_GITLAB_CLIENT_SECRET":          "client-secret",
		"WRENCH_GITLAB_SYNC_TOKEN":             "sync-token",
		"WRENCH_GITLAB_SYNC_INTERVAL":          "5m",
		"WRENCH_OPENROUTER_URL":                "http://openrouter:8000/api/v1",
		"WRENCH_OPENROUTER_KEY":                "or-key",
		"WRENCH_UPSTREAM_TIMEOUT_CONNECT":      "10s",
		"WRENCH_UPSTREAM_TIMEOUT_FIRST_TOKEN":  "90s",
		"WRENCH_UPSTREAM_TIMEOUT_STREAM_IDLE":  "90s",
		"WRENCH_UPSTREAM_TIMEOUT_TOTAL":        "10m",
		"WRENCH_UPSTREAM_CONCURRENCY_PER_USER": "3",
		"WRENCH_UPSTREAM_CONCURRENCY_GLOBAL":   "60",
		"WRENCH_LOG_LEVEL":                     "info",
		"WRENCH_SESSION_OFFLINE_AFTER":         "60s",
	}
}

func lookup(vars map[string]string) func(string) (string, bool) {
	return func(k string) (string, bool) { v, ok := vars[k]; return v, ok }
}

func TestLoadEnvOrgName(t *testing.T) {
	vars := validVars()
	if e, err := LoadEnv(lookup(vars)); err != nil || e.OrgName != "Wrench" {
		t.Fatalf("default org = %q, %v", e.OrgName, err)
	}
	vars["WRENCH_ORG_NAME"] = "Acme"
	if e, err := LoadEnv(lookup(vars)); err != nil || e.OrgName != "Acme" {
		t.Fatalf("org = %q, %v", e.OrgName, err)
	}
}

func TestLoadEnv(t *testing.T) {
	e, err := LoadEnv(lookup(validVars()))
	if err != nil {
		t.Fatal(err)
	}
	if e.ListenAddr != "0.0.0.0:8080" || e.PublicURL.Host != "wrench.example.com" || e.ClientKeysTTL != 168*time.Hour {
		t.Fatalf("env = %+v", e)
	}
	if e.Upstream.URL.String() != "http://openrouter:8000/api/v1" || e.Upstream.Key != "or-key" || e.Upstream.TimeoutTotal != 10*time.Minute {
		t.Fatalf("upstream = %+v", e.Upstream)
	}
	if e.Upstream.ConcurrencyPerUser != 3 || e.Upstream.ConcurrencyGlobal != 60 || e.GitLab.SyncInterval != 5*time.Minute {
		t.Fatalf("env = %+v", e)
	}
	if e.Sessions.OfflineAfter != time.Minute {
		t.Fatalf("sessions = %+v", e.Sessions)
	}
	if e.AlertWebhookURL != "" {
		t.Fatal("webhook set without WRENCH_ALERT_WEBHOOK_URL")
	}
}

func TestLoadEnvDefaultsOpenRouterURL(t *testing.T) {
	vars := validVars()
	delete(vars, "WRENCH_OPENROUTER_URL")
	e, err := LoadEnv(lookup(vars))
	if err != nil || e.Upstream.URL.String() != "https://openrouter.ai/api/v1" {
		t.Fatalf("url = %v, %v", e.Upstream.URL, err)
	}
}

func TestLoadEnvAlertWebhook(t *testing.T) {
	vars := validVars()
	vars["WRENCH_ALERT_WEBHOOK_URL"] = "https://hooks.example.com/T/secret"
	e, err := LoadEnv(lookup(vars))
	if err != nil || e.AlertWebhookURL.Reveal() != "https://hooks.example.com/T/secret" {
		t.Fatalf("webhook = %v", err)
	}
	vars["WRENCH_ALERT_WEBHOOK_URL"] = "hooks.example.com/secret"
	if _, err := LoadEnv(lookup(vars)); err == nil || !strings.Contains(err.Error(), "WRENCH_ALERT_WEBHOOK_URL") || strings.Contains(err.Error(), "secret") {
		t.Fatalf("err = %v", err)
	}
}

func TestLoadEnvRejects(t *testing.T) {
	cases := map[string]string{
		"WRENCH_LISTEN_ADDR":                   "",
		"WRENCH_PUBLIC_URL":                    "wrench.example.com",
		"WRENCH_WEB_SESSION_TTL":               "12 hours",
		"WRENCH_CLIENT_KEYS_TTL":               "-1h",
		"WRENCH_POSTGRES_DSN":                  "",
		"WRENCH_GITLAB_URL":                    "ftp://gitlab",
		"WRENCH_OPENROUTER_URL":                "openrouter.ai/api/v1",
		"WRENCH_UPSTREAM_TIMEOUT_TOTAL":        "0s",
		"WRENCH_UPSTREAM_CONCURRENCY_PER_USER": "0",
		"WRENCH_UPSTREAM_CONCURRENCY_GLOBAL":   "many",
		"WRENCH_LOG_LEVEL":                     "verbose",
		"WRENCH_SESSION_OFFLINE_AFTER":         "soon",
	}
	for key, bad := range cases {
		t.Run(key, func(t *testing.T) {
			vars := validVars()
			vars[key] = bad
			if _, err := LoadEnv(lookup(vars)); err == nil || !strings.Contains(err.Error(), key) {
				t.Fatalf("err = %v", err)
			}
		})
	}
}

func TestLoadEnvHTTPOnly(t *testing.T) {
	tests := []struct {
		value string
		want  bool
		err   bool
	}{{"", false, false}, {"true", true, false}, {"false", false, false}, {"yes", false, true}}
	for _, tt := range tests {
		t.Run(tt.value, func(t *testing.T) {
			vars := validVars()
			if tt.value != "" {
				vars["WRENCH_HTTP_ONLY"] = tt.value
			}
			e, err := LoadEnv(lookup(vars))
			if tt.err {
				if err == nil || !strings.Contains(err.Error(), "WRENCH_HTTP_ONLY") {
					t.Fatalf("err = %v", err)
				}
				return
			}
			if err != nil || e.HTTPOnly != tt.want {
				t.Fatalf("HTTPOnly = %v, %v", e.HTTPOnly, err)
			}
		})
	}
}

func TestLoadEnvReportsAllMissing(t *testing.T) {
	_, err := LoadEnv(lookup(map[string]string{}))
	if err == nil {
		t.Fatal("want error")
	}
	for key := range validVars() {
		if key == "WRENCH_OPENROUTER_URL" { // optional, has a default
			continue
		}
		if !strings.Contains(err.Error(), key) {
			t.Errorf("missing %s not reported", key)
		}
	}
}

func TestErrorsDoNotLeakSecrets(t *testing.T) {
	vars := validVars()
	vars["WRENCH_OPENROUTER_KEY"] = ""
	vars["WRENCH_POSTGRES_DSN"] = "postgres://u:hunter2@db"
	vars["WRENCH_LOG_LEVEL"] = "bad"
	_, err := LoadEnv(lookup(vars))
	if err == nil || strings.Contains(err.Error(), "hunter2") {
		t.Fatalf("err = %v", err)
	}
}

func TestSecretIsRedacted(t *testing.T) {
	s := Secret("hunter2")
	for _, out := range []string{s.String(), fmt.Sprint(s), fmt.Sprintf("%v %+v %#v %s", s, s, s, s), fmt.Sprintf("%+v", struct{ K Secret }{s})} {
		if strings.Contains(out, "hunter2") {
			t.Fatalf("leaked: %s", out)
		}
	}
	if s.Reveal() != "hunter2" {
		t.Fatal("Reveal lost the value")
	}
}
