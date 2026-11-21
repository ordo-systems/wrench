package proxy

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ordo-systems/wrench/internal/auth"
	"github.com/ordo-systems/wrench/internal/budget"
	"github.com/ordo-systems/wrench/internal/catalog"
	"github.com/ordo-systems/wrench/internal/catalog/catalogtest"
	"github.com/ordo-systems/wrench/internal/config"
	"github.com/ordo-systems/wrench/internal/config/configtest"
	"github.com/ordo-systems/wrench/internal/env"
	"github.com/ordo-systems/wrench/internal/store"
)

// fakeJournal records calls in memory; the PostgreSQL implementation is tested in package store.
type fakeJournal struct {
	mu          sync.Mutex
	started     []store.Attempt
	finished    map[string]store.AttemptResult
	generations map[string]string
	done        chan struct{}
	startErr    error
	touchErr    error
	// keys records the key id of every touched session, keyed by session.
	keys map[string]*int64
}

func newJournal() *fakeJournal {
	return &fakeJournal{finished: map[string]store.AttemptResult{}, generations: map[string]string{}, keys: map[string]*int64{},
		done: make(chan struct{}, 10)}
}

func (j *fakeJournal) RecordGeneration(_ context.Context, id, gen string) error {
	j.mu.Lock()
	defer j.mu.Unlock()
	j.generations[id] = gen
	return nil
}

// fakeAdmission returns a fixed decision.
type fakeAdmission struct {
	decision budget.Decision
	err      error
}

func (a *fakeAdmission) Check(context.Context, string) (budget.Decision, error) {
	return a.decision, a.err
}

func (j *fakeJournal) TouchSession(_ context.Context, id, _ string, keyID *int64, _ string, _ time.Time) error {
	j.mu.Lock()
	defer j.mu.Unlock()
	j.keys[id] = keyID
	return j.touchErr
}

func (j *fakeJournal) StartAttempt(_ context.Context, a store.Attempt) error {
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.startErr != nil {
		return j.startErr
	}
	j.started = append(j.started, a)
	return nil
}

func (j *fakeJournal) FinishAttempt(_ context.Context, id string, r store.AttemptResult) error {
	j.mu.Lock()
	j.finished[id] = r
	j.mu.Unlock()
	j.done <- struct{}{}
	return nil
}

// result waits for the single finished attempt.
func (j *fakeJournal) result(t *testing.T) (store.Attempt, store.AttemptResult) {
	t.Helper()
	select {
	case <-j.done:
	case <-time.After(5 * time.Second):
		t.Fatal("attempt was not finished")
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	if len(j.started) != 1 {
		t.Fatalf("started %d attempts", len(j.started))
	}
	return j.started[0], j.finished[j.started[0].ID]
}

// testAuth accepts "Bearer dev-token" as dev@example.com, a basic-tier user with a fresh GitLab status, and
// "Bearer admin-token" as admin@example.com, an administrator.
type testAuth struct{}

func (testAuth) Authenticate(r *http.Request) (auth.Identity, error) {
	switch r.Header.Get("Authorization") {
	case "Bearer dev-token":
		return auth.Identity{Email: "dev@example.com", Role: config.RoleUser, Credential: auth.CredentialKey, StatusFresh: true, KeyID: 42}, nil
	case "Bearer admin-token":
		return auth.Identity{Email: "admin@example.com", Role: config.RoleAdmin, Credential: auth.CredentialKey, StatusFresh: true}, nil
	}
	return auth.Identity{}, auth.ErrUnauthenticated
}

type fixture struct {
	journal   *fakeJournal
	admission *fakeAdmission
	upstream  *httptest.Server
	proxy     *httptest.Server
	// received is the body the fake OpenRouter got, captured per request.
	received chan map[string]any
	headers  chan http.Header
	// handler is the proxy under test, for tests that tune its limits.
	handler *Handler
	// authed is handler behind testAuth, for tests that bypass the HTTP server.
	authed http.Handler
}

func newFixture(t *testing.T, timeouts env.UpstreamEnv, upstream http.HandlerFunc) *fixture {
	t.Helper()
	cfg := configtest.Load(t, configtest.YAML)
	f := &fixture{journal: newJournal(), admission: &fakeAdmission{decision: budget.Decision{Allowed: true}},
		received: make(chan map[string]any, 16), headers: make(chan http.Header, 16)}
	f.upstream = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		f.received <- body
		f.headers <- r.Header.Clone()
		upstream(w, r)
	}))
	t.Cleanup(f.upstream.Close)
	u, _ := url.Parse(f.upstream.URL)
	if timeouts.URL != nil { // a test-chosen API base path on the fake upstream
		u.Path = timeouts.URL.Path
	}
	timeouts.URL = u
	timeouts.Key = "or-key"
	if timeouts.TimeoutConnect == 0 {
		timeouts.TimeoutConnect = time.Second
	}
	if timeouts.TimeoutFirstToken == 0 {
		timeouts.TimeoutFirstToken = 5 * time.Second
	}
	if timeouts.TimeoutStreamIdle == 0 {
		timeouts.TimeoutStreamIdle = 5 * time.Second
	}
	if timeouts.TimeoutTotal == 0 {
		timeouts.TimeoutTotal = 10 * time.Second
	}
	if timeouts.ConcurrencyPerUser == 0 {
		timeouts.ConcurrencyPerUser = 3
	}
	if timeouts.ConcurrencyGlobal == 0 {
		timeouts.ConcurrencyGlobal = 60
	}
	h := New(config.Fixed(cfg), catalogtest.For(t, cfg, catalog.Entry{UpstreamID: "qwen/qwen3-coder", ContextLength: 64000, MaxCompletionTokens: 4096}),
		timeouts, f.journal, f.admission)
	f.handler = h
	f.authed = auth.Middleware(testAuth{}, h)
	f.proxy = httptest.NewServer(f.authed)
	t.Cleanup(f.proxy.Close)
	return f
}

func (f *fixture) post(ctx context.Context, t *testing.T, body string, header map[string]string) *http.Response {
	t.Helper()
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, f.proxy.URL+"/v1/chat/completions", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer dev-token")
	req.Header.Set(SessionHeader, "session-1")
	for k, v := range header {
		if v == "" {
			req.Header.Del(k)
		} else {
			req.Header.Set(k, v)
		}
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	return resp
}

func sse(w http.ResponseWriter, events ...string) {
	w.Header().Set("Content-Type", "text/event-stream")
	for _, e := range events {
		_, _ = io.WriteString(w, "data: "+e+"\n\n")
		w.(http.Flusher).Flush()
	}
}

var streamEvents = []string{
	`{"id":"gen-123","model":"z-ai/glm-5.3","provider":"Z.AI","choices":[{"index":0,"delta":{"role":"assistant","content":"Hel"}}]}`,
	`{"id":"gen-123","model":"z-ai/glm-5.3","provider":"Z.AI","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_1","type":"function","function":{"name":"read","arguments":"{\"pa"}}]}}]}`,
	`{"id":"gen-123","model":"z-ai/glm-5.3","provider":"Z.AI","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":"th\":\"a.go\"}"}}]},"finish_reason":"tool_calls"}]}`,
	`{"id":"gen-123","model":"z-ai/glm-5.3","provider":"Z.AI","choices":[],"usage":{"prompt_tokens":120,"completion_tokens":30,"prompt_tokens_details":{"cached_tokens":100,"cache_write_tokens":0},"completion_tokens_details":{"reasoning_tokens":7},"cost":0.0004215}}`,
	`[DONE]`,
}

const streamRequest = `{"model":"glm","stream":true,"messages":[{"role":"user","content":"hi"}],
	"tools":[{"type":"function","function":{"name":"read","parameters":{"type":"object"}}}],
	"max_tokens":100000,"api_base":"http://evil","api_key":"x","fallbacks":["opus"],"temperature":0.2}`

func TestStreamRelaysBytesAndRecordsUsage(t *testing.T) {
	f := newFixture(t, env.UpstreamEnv{}, func(w http.ResponseWriter, r *http.Request) { sse(w, streamEvents...) })
	resp := f.post(context.Background(), t, streamRequest, nil)
	got, _ := io.ReadAll(resp.Body)
	resp.Body.Close()

	var want strings.Builder
	for _, e := range streamEvents {
		want.WriteString("data: " + e + "\n\n")
	}
	if resp.StatusCode != http.StatusOK || string(got) != want.String() {
		t.Fatalf("status %d, body:\n%s", resp.StatusCode, got)
	}
	if ct := resp.Header.Get("Content-Type"); ct != "text/event-stream" {
		t.Fatalf("content type %q", ct)
	}

	a, r := f.journal.result(t)
	if a.UserEmail != "dev@example.com" || a.SessionID != "session-1" || a.RequestedModel != "glm" || a.RequestID == "" || a.ID == "" {
		t.Fatalf("attempt = %+v", a)
	}
	if r.Outcome != store.OutcomeSuccess || r.GenerationID != "gen-123" || r.InferenceProvider != "Z.AI" || r.Provider != "openrouter" || r.UpstreamModel != "z-ai/glm-5.3" {
		t.Fatalf("result = %+v", r)
	}
	if r.Cost == nil || r.Cost.String() != "0.0004215" || *r.Tokens.Input != 120 || *r.Tokens.Output != 30 || *r.Tokens.CacheRead != 100 || *r.Tokens.CacheWrite != 0 || *r.Tokens.Reasoning != 7 {
		t.Fatalf("usage = %+v cost %v", r.Tokens, r.Cost)
	}
	if r.FirstTokenAt == nil || r.FirstTokenAt.Before(a.StartedAt) || r.FinishedAt.Before(*r.FirstTokenAt) {
		t.Fatalf("timing: start %v first %v end %v", a.StartedAt, r.FirstTokenAt, r.FinishedAt)
	}
	// The generation id is persisted as soon as the stream shows it, for crash recovery.
	if f.journal.generations[a.ID] != "gen-123" {
		t.Fatalf("generation not recorded early: %v", f.journal.generations)
	}
}

func TestRequestIsRewrittenForUpstream(t *testing.T) {
	f := newFixture(t, env.UpstreamEnv{}, func(w http.ResponseWriter, r *http.Request) { sse(w, streamEvents...) })
	resp := f.post(context.Background(), t, streamRequest, map[string]string{"X-Wrench-Request-Id": "req-7"})
	_, _ = io.Copy(io.Discard, resp.Body)
	resp.Body.Close()

	body, h := <-f.received, <-f.headers
	if h.Get("Authorization") != "Bearer or-key" {
		t.Fatalf("upstream auth %q", h.Get("Authorization"))
	}
	if body["model"] != "z-ai/glm-5.3" || body["max_tokens"] != float64(8192) || body["reasoning_effort"] != "low" || body["temperature"] != 0.2 {
		t.Fatalf("body = %v", body)
	}
	for _, k := range []string{"api_base", "api_key", "fallbacks"} {
		if _, ok := body[k]; ok {
			t.Fatalf("%s forwarded", k)
		}
	}
	if _, ok := body["num_retries"]; ok {
		t.Fatalf("num_retries = %v", body["num_retries"])
	}
	so, _ := body["stream_options"].(map[string]any)
	if so["include_usage"] != true {
		t.Fatalf("stream_options = %v", body["stream_options"])
	}
	usage, _ := body["usage"].(map[string]any)
	prov, _ := body["provider"].(map[string]any)
	if usage["include"] != true || prov["allow_fallbacks"] != false || prov["require_parameters"] != true {
		t.Fatalf("openrouter options: usage %v provider %v", body["usage"], body["provider"])
	}
	if _, ok := body["tools"]; !ok {
		t.Fatal("tools dropped")
	}
	a, _ := f.journal.result(t)
	if a.RequestID != "req-7" {
		t.Fatalf("request id %q", a.RequestID)
	}
}

// OpenRouter's API base may be configured with a trailing slash; requests still reach <base>/chat/completions.
func TestProxySendsTheOpenRouterKey(t *testing.T) {
	paths := make(chan string, 1)
	base, _ := url.Parse("http://x/api/v1/")
	f := newFixture(t, env.UpstreamEnv{URL: base}, func(w http.ResponseWriter, r *http.Request) {
		paths <- r.URL.Path
		sse(w, streamEvents...)
	})
	resp := f.post(context.Background(), t, streamRequest, nil)
	_, _ = io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	if got, h := <-paths, <-f.headers; got != "/api/v1/chat/completions" || h.Get("Authorization") != "Bearer or-key" {
		t.Fatalf("path %q, auth %q", got, h.Get("Authorization"))
	}
}

func TestSmallerClientLimitIsKeptAndReasoningOmitted(t *testing.T) {
	f := newFixture(t, env.UpstreamEnv{}, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"id":"gen-9","model":"qwen/qwen3-coder","choices":[],"usage":{"prompt_tokens":1,"completion_tokens":2,"cost":1e-06}}`)
	})
	f.post(context.Background(), t, `{"model":"glm","max_completion_tokens":1,"reasoning":{"effort":"high"},"messages":[]}`, nil).Body.Close()
	body := <-f.received
	if body["max_completion_tokens"] != float64(1) || body["max_tokens"] != nil || body["reasoning"] != nil || body["reasoning_effort"] != "low" {
		t.Fatalf("body = %v", body)
	}
}

func TestNonStreamResponse(t *testing.T) {
	const payload = `{"id":"gen-9","model":"z-ai/glm-5.3","provider":"Z.AI","choices":[{"message":{"role":"assistant","content":"ok"}}],"usage":{"prompt_tokens":1,"completion_tokens":2,"cost":1e-06}}`
	f := newFixture(t, env.UpstreamEnv{}, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, payload)
	})
	resp := f.post(context.Background(), t, `{"model":"glm","messages":[]}`, nil)
	got, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK || string(got) != payload {
		t.Fatalf("status %d body %s", resp.StatusCode, got)
	}
	body := <-f.received
	if _, ok := body["stream_options"]; ok {
		t.Fatal("stream_options on a non-stream request")
	}
	if body["max_tokens"] != float64(8192) {
		t.Fatalf("default output ceiling = %v", body["max_tokens"])
	}
	_, r := f.journal.result(t)
	if r.Outcome != store.OutcomeSuccess || r.GenerationID != "gen-9" || r.Cost.String() != "0.000001" || *r.Tokens.Output != 2 || r.Tokens.Reasoning != nil {
		t.Fatalf("result = %+v", r)
	}
}

func TestUnavailableModel(t *testing.T) {
	called := false
	f := newFixture(t, env.UpstreamEnv{}, func(w http.ResponseWriter, r *http.Request) { called = true })
	f.handler.catalog = catalogtest.For(t, configtest.Load(t, configtest.YAML), catalog.Entry{UpstreamID: "z-ai/glm-5.3"})
	resp := f.post(context.Background(), t, `{"model":"glm","messages":[]}`, nil)
	msg, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusServiceUnavailable || called || !strings.Contains(string(msg), "unknown_upstream") {
		t.Fatalf("status %d called %v body %s", resp.StatusCode, called, msg)
	}
}

func TestRejectedBeforeForwarding(t *testing.T) {
	cases := map[string]struct {
		body   string
		header map[string]string
		code   int
	}{
		"unknown model":     {`{"model":"gpt","messages":[]}`, nil, http.StatusBadRequest},
		"other tier model":  {`{"model":"big","messages":[]}`, nil, http.StatusForbidden},
		"model in no tier":  {`{"model":"qwen","messages":[]}`, nil, http.StatusForbidden},
		"no model":          {`{"messages":[]}`, nil, http.StatusBadRequest},
		"empty model":       {`{"model":"","messages":[]}`, nil, http.StatusBadRequest},
		"zero max tokens":   {`{"model":"glm","max_tokens":0,"messages":[]}`, nil, http.StatusBadRequest},
		"text max tokens":   {`{"model":"glm","max_completion_tokens":"10","messages":[]}`, nil, http.StatusBadRequest},
		"invalid json":      {`{`, nil, http.StatusBadRequest},
		"multiple choices":  {`{"model":"glm","n":2,"messages":[]}`, nil, http.StatusBadRequest},
		"no session":        {`{"model":"glm","messages":[]}`, map[string]string{SessionHeader: ""}, http.StatusBadRequest},
		"bad session":       {`{"model":"glm","messages":[]}`, map[string]string{SessionHeader: "a b"}, http.StatusBadRequest},
		"wrong token":       {`{"model":"glm","messages":[]}`, map[string]string{"Authorization": "Bearer nope"}, http.StatusUnauthorized},
		"bad request id":    {`{"model":"glm","messages":[]}`, map[string]string{"X-Wrench-Request-Id": strings.Repeat("x", 200)}, http.StatusBadRequest},
		"stream not a bool": {`{"model":"glm","stream":"yes","messages":[]}`, nil, http.StatusBadRequest},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			called := false
			f := newFixture(t, env.UpstreamEnv{}, func(w http.ResponseWriter, r *http.Request) { called = true })
			resp := f.post(context.Background(), t, c.body, c.header)
			msg, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			if resp.StatusCode != c.code || called || len(f.journal.started) != 0 {
				t.Fatalf("status %d (want %d), upstream called %v, started %d: %s", resp.StatusCode, c.code, called, len(f.journal.started), msg)
			}
			var e struct {
				Error struct{ Message string } `json:"error"`
			}
			if json.Unmarshal(msg, &e) != nil || e.Error.Message == "" {
				t.Fatalf("not an OpenAI-style error: %s", msg)
			}
		})
	}
}

func TestJournalFailureFailsClosed(t *testing.T) {
	called := false
	f := newFixture(t, env.UpstreamEnv{}, func(w http.ResponseWriter, r *http.Request) { called = true })
	f.journal.startErr = errors.New("db down")
	resp := f.post(context.Background(), t, `{"model":"glm","messages":[]}`, nil)
	resp.Body.Close()
	if resp.StatusCode != http.StatusServiceUnavailable || called {
		t.Fatalf("status %d, upstream called %v", resp.StatusCode, called)
	}
}

func TestSessionOfAnotherUserIsForbidden(t *testing.T) {
	f := newFixture(t, env.UpstreamEnv{}, func(w http.ResponseWriter, r *http.Request) {})
	f.journal.touchErr = store.ErrSessionOwner
	resp := f.post(context.Background(), t, `{"model":"glm","messages":[]}`, nil)
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("status %d", resp.StatusCode)
	}
}

func TestUpstreamErrorIsRelayed(t *testing.T) {
	f := newFixture(t, env.UpstreamEnv{}, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = io.WriteString(w, `{"error":{"message":"rate limited"}}`)
	})
	resp := f.post(context.Background(), t, `{"model":"glm","stream":true,"messages":[]}`, nil)
	got, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusTooManyRequests || !strings.Contains(string(got), "rate limited") {
		t.Fatalf("status %d body %s", resp.StatusCode, got)
	}
	_, r := f.journal.result(t)
	if r.Outcome != store.OutcomeError || r.ErrorReason != "upstream status 429" || r.Cost != nil || r.NotSent {
		t.Fatalf("result = %+v", r)
	}
}

func TestUpstreamUnavailable(t *testing.T) {
	f := newFixture(t, env.UpstreamEnv{}, func(w http.ResponseWriter, r *http.Request) {})
	f.upstream.Close()
	resp := f.post(context.Background(), t, `{"model":"glm","messages":[]}`, nil)
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadGateway {
		t.Fatalf("status %d", resp.StatusCode)
	}
	_, r := f.journal.result(t)
	if r.Outcome != store.OutcomeError || !strings.Contains(r.ErrorReason, "upstream request failed") || !r.NotSent {
		t.Fatalf("result = %+v", r)
	}
}

func TestOversizedBodyIsNotRelayedAsSuccess(t *testing.T) {
	const payload = `{"id":"gen-big","choices":[],"usage":{"prompt_tokens":1,"completion_tokens":2,"cost":0.001}}`
	f := newFixture(t, env.UpstreamEnv{}, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, payload)
	})
	f.handler.maxResponse = int64(len(payload)) - 1
	resp := f.post(context.Background(), t, `{"model":"glm","messages":[]}`, nil)
	got, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadGateway || strings.Contains(string(got), "gen-big") {
		t.Fatalf("status %d body %s", resp.StatusCode, got)
	}
	_, r := f.journal.result(t)
	if r.Outcome != store.OutcomeError || r.ErrorReason != "upstream body is too large" || r.Cost != nil {
		t.Fatalf("result = %+v", r)
	}
}

// goneWriter is a client connection that broke before the first byte of the body.
type goneWriter struct{ header http.Header }

func (g *goneWriter) Header() http.Header       { return g.header }
func (g *goneWriter) WriteHeader(int)           {}
func (g *goneWriter) Write([]byte) (int, error) { return 0, errors.New("broken pipe") }

func TestFailedClientWriteIsCancelled(t *testing.T) {
	f := newFixture(t, env.UpstreamEnv{}, func(w http.ResponseWriter, r *http.Request) { sse(w, streamEvents...) })
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"glm","stream":true,"messages":[]}`))
	req.Header.Set("Authorization", "Bearer dev-token")
	req.Header.Set(SessionHeader, "session-1")
	f.authed.ServeHTTP(&goneWriter{header: http.Header{}}, req)
	_, r := f.journal.result(t)
	if r.Outcome != store.OutcomeCancelled || r.ErrorReason != "client disconnected" {
		t.Fatalf("result = %+v", r)
	}
}

func TestBrokenStreamIsAnError(t *testing.T) {
	f := newFixture(t, env.UpstreamEnv{}, func(w http.ResponseWriter, r *http.Request) { sse(w, streamEvents[0]) })
	resp := f.post(context.Background(), t, `{"model":"glm","stream":true,"messages":[]}`, nil)
	got, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if !strings.Contains(string(got), "Hel") {
		t.Fatalf("partial content lost: %s", got)
	}
	_, r := f.journal.result(t)
	if r.Outcome != store.OutcomeError || r.ErrorReason != "upstream stream ended without [DONE]" || r.GenerationID != "gen-123" || r.Cost != nil {
		t.Fatalf("result = %+v", r)
	}
}

func TestClientCancelStopsUpstream(t *testing.T) {
	upstreamGone := make(chan struct{})
	f := newFixture(t, env.UpstreamEnv{}, func(w http.ResponseWriter, r *http.Request) {
		sse(w, streamEvents[0])
		<-r.Context().Done()
		close(upstreamGone)
	})
	ctx, cancel := context.WithCancel(context.Background())
	resp := f.post(ctx, t, `{"model":"glm","stream":true,"messages":[]}`, nil)
	line, _ := bufio.NewReader(resp.Body).ReadString('\n')
	if !strings.Contains(line, "Hel") {
		t.Fatalf("first line %q", line)
	}
	cancel()
	resp.Body.Close()
	select {
	case <-upstreamGone:
	case <-time.After(5 * time.Second):
		t.Fatal("upstream call not cancelled")
	}
	_, r := f.journal.result(t)
	if r.Outcome != store.OutcomeCancelled || r.GenerationID != "gen-123" || r.FirstTokenAt == nil {
		t.Fatalf("result = %+v", r)
	}
}

func TestStreamTimeouts(t *testing.T) {
	cases := map[string]struct {
		timeouts env.UpstreamEnv
		events   []string
		reason   string
	}{
		"first token": {env.UpstreamEnv{TimeoutFirstToken: 100 * time.Millisecond}, nil, "upstream first-token timeout"},
		// SSE comments are keep-alives, not progress, so they must not reset the first-token timer.
		"first token despite keep-alives": {env.UpstreamEnv{TimeoutFirstToken: 300 * time.Millisecond}, nil, "upstream first-token timeout"},
		"idle":                            {env.UpstreamEnv{TimeoutStreamIdle: 100 * time.Millisecond}, streamEvents[:1], "upstream stream idle timeout"},
		"total":                           {env.UpstreamEnv{TimeoutTotal: 150 * time.Millisecond}, streamEvents[:1], "upstream total timeout"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			keepAlive := strings.Contains(name, "keep-alives")
			f := newFixture(t, c.timeouts, func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "text/event-stream")
				w.WriteHeader(http.StatusOK)
				w.(http.Flusher).Flush()
				sse(w, c.events...)
				for keepAlive {
					select {
					case <-r.Context().Done():
						return
					case <-time.After(50 * time.Millisecond):
						_, _ = io.WriteString(w, ": OPENROUTER PROCESSING\n\n")
						w.(http.Flusher).Flush()
					}
				}
				<-r.Context().Done()
			})
			resp := f.post(context.Background(), t, `{"model":"glm","stream":true,"messages":[]}`, nil)
			_, _ = io.Copy(io.Discard, resp.Body)
			resp.Body.Close()
			_, r := f.journal.result(t)
			if r.Outcome != store.OutcomeError || r.ErrorReason != c.reason {
				t.Fatalf("result = %+v", r)
			}
		})
	}
}

func TestBudgetRefusalsHappenBeforeForwarding(t *testing.T) {
	cases := map[string]struct {
		decision budget.Decision
		err      error
		status   int
		code     string
	}{
		"exhausted":       {budget.Decision{Code: budget.CodeBudgetExhausted, Reason: "user day budget exhausted"}, nil, http.StatusPaymentRequired, budget.CodeBudgetExhausted},
		"accounting down": {budget.Decision{}, errors.New("db down"), http.StatusServiceUnavailable, ""},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			called := false
			f := newFixture(t, env.UpstreamEnv{}, func(w http.ResponseWriter, r *http.Request) { called = true })
			f.admission.decision, f.admission.err = c.decision, c.err
			resp := f.post(context.Background(), t, `{"model":"glm","messages":[]}`, nil)
			var body struct {
				Error struct {
					Message string `json:"message"`
					Code    any    `json:"code"`
				} `json:"error"`
			}
			_ = json.NewDecoder(resp.Body).Decode(&body)
			resp.Body.Close()
			if resp.StatusCode != c.status || called || len(f.journal.started) != 0 {
				t.Fatalf("status %d, called %v, started %d", resp.StatusCode, called, len(f.journal.started))
			}
			if c.code != "" && (body.Error.Code != c.code || body.Error.Message != c.decision.Reason) {
				t.Fatalf("error = %+v", body.Error)
			}
		})
	}
}

func TestBudgetWarningHeader(t *testing.T) {
	f := newFixture(t, env.UpstreamEnv{}, func(w http.ResponseWriter, r *http.Request) { sse(w, streamEvents...) })
	f.admission.decision = budget.Decision{Allowed: true, Warnings: []budget.Usage{
		{Scope: budget.ScopeUser, Window: store.WindowDay, Spent: 820_000_000, Limit: 1_000_000_000, Percent: 82, Threshold: 80},
	}}
	resp := f.post(context.Background(), t, streamRequest, nil)
	_, _ = io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	if got := resp.Header.Get(BudgetWarningHeader); got != "user day 82% ($0.82 of $1)" {
		t.Fatalf("warning header %q", got)
	}
}

// TestBudgetWarningHeaderByRole checks that warnings about global windows carry amounts for administrators only.
func TestBudgetWarningHeaderByRole(t *testing.T) {
	for _, c := range []struct {
		name, token string
		scope       budget.Scope
		want        string
	}{
		{"user window, user", "dev-token", budget.ScopeUser, "user day 82% ($0.82 of $1)"},
		{"user window, admin", "admin-token", budget.ScopeUser, "user day 82% ($0.82 of $1)"},
		{"global window, user", "dev-token", budget.ScopeGlobal, "global day 82%"},
		{"global window, admin", "admin-token", budget.ScopeGlobal, "global day 82% ($0.82 of $1)"},
	} {
		t.Run(c.name, func(t *testing.T) {
			f := newFixture(t, env.UpstreamEnv{}, func(w http.ResponseWriter, r *http.Request) { sse(w, streamEvents...) })
			f.admission.decision = budget.Decision{Allowed: true, Warnings: []budget.Usage{
				{Scope: c.scope, Window: store.WindowDay, Spent: 820_000_000, Limit: 1_000_000_000, Percent: 82, Threshold: 80},
			}}
			resp := f.post(context.Background(), t, streamRequest, map[string]string{"Authorization": "Bearer " + c.token})
			_, _ = io.Copy(io.Discard, resp.Body)
			resp.Body.Close()
			if got := resp.Header.Get(BudgetWarningHeader); got != c.want {
				t.Fatalf("warning header %q, want %q", got, c.want)
			}
		})
	}
}

// TestGlobalBudgetRefusalByRole checks that a refusal by a global window names no amounts for non-administrators.
func TestGlobalBudgetRefusalByRole(t *testing.T) {
	resets := time.Date(2026, 11, 14, 0, 0, 0, 0, time.UTC)
	reason := "global day budget exhausted: spent $500 of $500; resets at 2026-11-14T00:00:00Z"
	for _, c := range []struct {
		name, token string
		want        string
	}{
		{"user", "dev-token", "shared day budget exhausted; resets at 2026-11-14T00:00:00Z"},
		{"admin", "admin-token", reason},
	} {
		t.Run(c.name, func(t *testing.T) {
			f := newFixture(t, env.UpstreamEnv{}, func(w http.ResponseWriter, r *http.Request) {})
			f.admission.decision = budget.Decision{Code: budget.CodeBudgetExhausted, Reason: reason,
				Refused: &budget.Usage{Scope: budget.ScopeGlobal, Window: store.WindowDay, Spent: 500_000_000_000,
					Limit: 500_000_000_000, Percent: 100, ResetsAt: resets}}
			resp := f.post(context.Background(), t, `{"model":"glm","messages":[]}`, map[string]string{"Authorization": "Bearer " + c.token})
			var body struct {
				Error struct {
					Message string `json:"message"`
					Code    any    `json:"code"`
				} `json:"error"`
			}
			_ = json.NewDecoder(resp.Body).Decode(&body)
			resp.Body.Close()
			if resp.StatusCode != http.StatusPaymentRequired || body.Error.Code != budget.CodeBudgetExhausted || body.Error.Message != c.want {
				t.Fatalf("status %d, error %+v, want %q", resp.StatusCode, body.Error, c.want)
			}
		})
	}
}

func TestConcurrencyLimit(t *testing.T) {
	release := make(chan struct{})
	f := newFixture(t, env.UpstreamEnv{ConcurrencyPerUser: 1, ConcurrencyGlobal: 5}, func(w http.ResponseWriter, r *http.Request) {
		sse(w, streamEvents[0])
		select {
		case <-release:
		case <-r.Context().Done():
		}
		sse(w, streamEvents[3:]...)
	})
	first := f.post(context.Background(), t, `{"model":"glm","stream":true,"messages":[]}`, nil)
	_, _ = bufio.NewReader(first.Body).ReadString('\n')

	second := f.post(context.Background(), t, `{"model":"glm","stream":true,"messages":[]}`, nil)
	second.Body.Close()
	if second.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("second concurrent request: %d", second.StatusCode)
	}
	close(release)
	_, _ = io.Copy(io.Discard, first.Body)
	first.Body.Close()
	f.journal.result(t)

	// The slot is free again once the first request finished.
	third := f.post(context.Background(), t, `{"model":"glm","messages":[]}`, nil)
	third.Body.Close()
	if third.StatusCode == http.StatusTooManyRequests {
		t.Fatal("slot leaked")
	}
}

func TestLimiter(t *testing.T) {
	l := newLimiter(2, 3)
	for i, step := range []struct {
		user string
		want bool
	}{{"a", true}, {"a", true}, {"a", false}, {"b", true}, {"c", false}} {
		if got := l.acquire(step.user); got != step.want {
			t.Fatalf("step %d: acquire(%s) = %v", i, step.user, got)
		}
	}
	l.release("a")
	if !l.acquire("c") {
		t.Fatal("released slot not reusable")
	}
	l.release("a")
	l.release("b")
	l.release("c")
	if len(l.users) != 0 || l.total != 0 {
		t.Fatalf("limiter not empty: %+v", l)
	}
}

func TestAgentHeaderIgnored(t *testing.T) {
	f := newFixture(t, env.UpstreamEnv{}, func(w http.ResponseWriter, r *http.Request) { sse(w, streamEvents...) })
	resp := f.post(context.Background(), t, streamRequest, map[string]string{"X-Wrench-Agent-Id": "sub-1"})
	_, _ = io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status %d", resp.StatusCode)
	}
	if a, _ := f.journal.result(t); a.SessionID != "session-1" || a.RequestedModel != "glm" {
		t.Fatalf("attempt = %+v", a)
	}
}

func TestModelOutsideTierIsForbidden(t *testing.T) {
	f := newFixture(t, env.UpstreamEnv{}, func(w http.ResponseWriter, r *http.Request) { sse(w, streamEvents...) })
	resp := f.post(context.Background(), t, `{"model":"big","messages":[{"role":"user","content":"hi"}]}`, nil)
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden || !strings.Contains(string(body), `model \"big\" is not allowed in your tier`) {
		t.Fatalf("status %d, body %s", resp.StatusCode, body)
	}
}

func TestOpencodeSessionHeader(t *testing.T) {
	const ses = "ses_e2067994affelpQh9Yv9RwjXGP"
	tests := []struct {
		name    string
		header  map[string]string
		code    int
		session string
	}{
		{"opencode only", map[string]string{SessionHeader: "", OpencodeSessionHeader: ses}, http.StatusOK, ses},
		{"wrench header wins", map[string]string{OpencodeSessionHeader: ses}, http.StatusOK, "session-1"},
		{"neither", map[string]string{SessionHeader: ""}, http.StatusBadRequest, ""},
		{"bad opencode id", map[string]string{SessionHeader: "", OpencodeSessionHeader: "ses bad"}, http.StatusBadRequest, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newFixture(t, env.UpstreamEnv{}, func(w http.ResponseWriter, r *http.Request) { sse(w, streamEvents...) })
			resp := f.post(context.Background(), t, streamRequest, tt.header)
			_, _ = io.Copy(io.Discard, resp.Body)
			resp.Body.Close()
			if resp.StatusCode != tt.code {
				t.Fatalf("status %d, want %d", resp.StatusCode, tt.code)
			}
			if tt.code != http.StatusOK {
				return
			}
			if a, _ := f.journal.result(t); a.SessionID != tt.session {
				t.Fatalf("session = %q, want %q", a.SessionID, tt.session)
			}
			f.journal.mu.Lock()
			defer f.journal.mu.Unlock()
			if k := f.journal.keys[tt.session]; k == nil || *k != 42 {
				t.Fatalf("touched key = %v, want 42", k)
			}
		})
	}
}

func TestAgentFromUserAgent(t *testing.T) {
	tests := []struct{ name, in, want string }{
		{"product token", "opencode/2.3.4 (darwin; arm64)", "opencode/2.3.4"},
		{"empty", "", ""},
		{"long token is cut", strings.Repeat("a", 80), strings.Repeat("a", 64)},
		{"leading space", "  x", ""},
		{"multibyte stays valid", strings.Repeat("\u00e9", 80), strings.Repeat("\u00e9", 64)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := agentFromUserAgent(tt.in); got != tt.want {
				t.Errorf("agentFromUserAgent(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}
