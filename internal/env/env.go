// Package env parses runtime settings and secrets from the environment (a deployment's .env).
package env

import (
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"time"
)

// Env holds runtime settings and secrets from the environment. Secret values must never reach logs or diagnostics.
// Backup settings belong to the backup container and are not read by the Wrench process.
type Env struct {
	ListenAddr    string
	PublicURL     *url.URL
	WebSessionTTL time.Duration
	ClientKeysTTL time.Duration
	PostgresDSN   Secret
	GitLab        GitLabEnv
	Upstream      UpstreamEnv
	Sessions      SessionsEnv
	LogLevel      string
	// BackupStatusFile is the status file written by the backup container; empty when backup is not deployed.
	BackupStatusFile string
	// OrgName names the organization in opencode (WRENCH_ORG_NAME, default Wrench).
	OrgName string
	// AlertWebhookURL is empty when no webhook is configured; alerts are then logged and shown on the dashboard.
	AlertWebhookURL Secret
	// HTTPOnly drops Secure and the __Host- prefix from cookies, for deployments without TLS (WRENCH_HTTP_ONLY).
	HTTPOnly bool
}

// GitLabEnv configures OIDC login and the periodic account status check.
type GitLabEnv struct {
	URL          *url.URL
	ClientID     string
	ClientSecret Secret
	SyncToken    Secret
	SyncInterval time.Duration
}

// UpstreamEnv configures OpenRouter and the limits on requests to it. URL is OpenRouter's API base; Key is sent
// only to that base and never forwarded to clients.
type UpstreamEnv struct {
	URL                *url.URL
	Key                Secret
	TimeoutConnect     time.Duration
	TimeoutFirstToken  time.Duration
	TimeoutStreamIdle  time.Duration
	TimeoutTotal       time.Duration
	ConcurrencyPerUser int
	ConcurrencyGlobal  int
}

// SessionsEnv sets when a session without requests for OfflineAfter is shown as offline; that is not evidence
// that the client has stopped.
type SessionsEnv struct {
	OfflineAfter time.Duration
}

// Secret is a string whose formatting is redacted to keep values out of logs and errors.
type Secret string

const redacted = "[redacted]"

// String returns a fixed placeholder instead of the value.
func (Secret) String() string { return redacted }

// GoString keeps %#v from printing the value.
func (Secret) GoString() string { return redacted }

// Reveal returns the value for the one place that must send it, such as an Authorization header.
func (s Secret) Reveal() string { return string(s) }

// defaultOpenRouterURL is OpenRouter's API base, used when WRENCH_OPENROUTER_URL is unset.
const defaultOpenRouterURL = "https://openrouter.ai/api/v1"

var logLevels = map[string]bool{"debug": true, "info": true, "warn": true, "error": true}

// LoadEnv parses and validates WRENCH_* variables and reports every invalid or missing value at once.
// Error messages name variables, never their values.
func LoadEnv(lookupEnv func(string) (string, bool)) (Env, error) {
	p := parser{lookup: lookupEnv}
	e := Env{
		ListenAddr:    p.str("WRENCH_LISTEN_ADDR"),
		PublicURL:     p.url("WRENCH_PUBLIC_URL"),
		WebSessionTTL: p.duration("WRENCH_WEB_SESSION_TTL"),
		ClientKeysTTL: p.duration("WRENCH_CLIENT_KEYS_TTL"),
		PostgresDSN:   Secret(p.str("WRENCH_POSTGRES_DSN")),
		GitLab: GitLabEnv{
			URL:          p.url("WRENCH_GITLAB_URL"),
			ClientID:     p.str("WRENCH_GITLAB_CLIENT_ID"),
			ClientSecret: Secret(p.str("WRENCH_GITLAB_CLIENT_SECRET")),
			SyncToken:    Secret(p.str("WRENCH_GITLAB_SYNC_TOKEN")),
			SyncInterval: p.duration("WRENCH_GITLAB_SYNC_INTERVAL"),
		},
		Upstream: UpstreamEnv{
			URL:                p.urlOr("WRENCH_OPENROUTER_URL", defaultOpenRouterURL),
			Key:                Secret(p.str("WRENCH_OPENROUTER_KEY")),
			TimeoutConnect:     p.duration("WRENCH_UPSTREAM_TIMEOUT_CONNECT"),
			TimeoutFirstToken:  p.duration("WRENCH_UPSTREAM_TIMEOUT_FIRST_TOKEN"),
			TimeoutStreamIdle:  p.duration("WRENCH_UPSTREAM_TIMEOUT_STREAM_IDLE"),
			TimeoutTotal:       p.duration("WRENCH_UPSTREAM_TIMEOUT_TOTAL"),
			ConcurrencyPerUser: p.positiveInt("WRENCH_UPSTREAM_CONCURRENCY_PER_USER"),
			ConcurrencyGlobal:  p.positiveInt("WRENCH_UPSTREAM_CONCURRENCY_GLOBAL"),
		},
		Sessions: SessionsEnv{
			OfflineAfter: p.duration("WRENCH_SESSION_OFFLINE_AFTER"),
		},
		LogLevel: p.str("WRENCH_LOG_LEVEL"),
		HTTPOnly: p.optionalBool("WRENCH_HTTP_ONLY"),
	}
	if e.LogLevel != "" && !logLevels[e.LogLevel] {
		p.fail("WRENCH_LOG_LEVEL", "must be debug, info, warn or error")
	}
	e.BackupStatusFile, _ = lookupEnv("WRENCH_BACKUP_STATUS_FILE")
	if e.OrgName, _ = lookupEnv("WRENCH_ORG_NAME"); e.OrgName == "" {
		e.OrgName = "Wrench"
	}
	if hook, _ := lookupEnv("WRENCH_ALERT_WEBHOOK_URL"); hook != "" {
		if u, err := url.Parse(hook); err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
			p.fail("WRENCH_ALERT_WEBHOOK_URL", "must be an absolute http(s) URL")
		}
		e.AlertWebhookURL = Secret(hook)
	}
	return e, errors.Join(p.errs...)
}

// parser collects errors so one run reports every problem in the candidate environment.
type parser struct {
	lookup func(string) (string, bool)
	errs   []error
}

func (p *parser) fail(key, reason string) {
	p.errs = append(p.errs, fmt.Errorf("%s %s", key, reason))
}

func (p *parser) str(key string) string {
	v, _ := p.lookup(key)
	if v == "" {
		p.fail(key, "is required")
	}
	return v
}

func (p *parser) url(key string) *url.URL {
	v := p.str(key)
	if v == "" {
		return nil
	}
	u, err := url.Parse(v)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		p.fail(key, "must be an absolute http(s) URL")
		return nil
	}
	return u
}

// urlOr parses key like url, using def when the variable is unset or empty.
func (p *parser) urlOr(key, def string) *url.URL {
	if v, _ := p.lookup(key); v == "" {
		u, _ := url.Parse(def)
		return u
	}
	return p.url(key)
}

func (p *parser) duration(key string) time.Duration {
	v := p.str(key)
	if v == "" {
		return 0
	}
	d, err := time.ParseDuration(v)
	if err != nil || d <= 0 {
		p.fail(key, "must be a positive Go duration such as 90s or 168h")
	}
	return d
}

// optionalBool accepts true or false; unset or empty means false.
func (p *parser) optionalBool(key string) bool {
	switch v, _ := p.lookup(key); v {
	case "", "false":
		return false
	case "true":
		return true
	default:
		p.fail(key, "must be true or false")
		return false
	}
}

func (p *parser) positiveInt(key string) int {
	v := p.str(key)
	if v == "" {
		return 0
	}
	n, err := strconv.Atoi(v)
	if err != nil || n <= 0 {
		p.fail(key, "must be a positive integer")
	}
	return n
}
