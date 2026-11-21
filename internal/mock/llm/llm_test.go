package llm

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func start(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(Handler())
	t.Cleanup(srv.Close)
	return srv
}

func get(t *testing.T, url string, header ...string) (int, string) {
	t.Helper()
	req, _ := http.NewRequest(http.MethodGet, url, nil)
	if len(header) == 2 {
		req.Header.Set(header[0], header[1])
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b)
}

func post(ctx context.Context, t *testing.T, srv *httptest.Server, content string, stream bool) *http.Response {
	t.Helper()
	body := `{"model":"m","stream":` + map[bool]string{true: "true", false: "false"}[stream] +
		`,"messages":[{"role":"user","content":"` + content + `"}]}`
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, srv.URL+"/api/v1/chat/completions", strings.NewReader(body))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	return resp
}

func readAll(t *testing.T, resp *http.Response) string {
	t.Helper()
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return string(b)
}

func TestNonStreamCompletion(t *testing.T) {
	srv := start(t)
	resp := post(context.Background(), t, srv, "hello", false)
	body := readAll(t, resp)
	if resp.StatusCode != http.StatusOK || !strings.Contains(body, `"content":"done"`) || !strings.Contains(body, `"cost":4.215e-04`) {
		t.Fatalf("status %d body %s", resp.StatusCode, body)
	}
	if _, last := get(t, srv.URL+"/_last"); !strings.Contains(last, `"content":"hello"`) {
		t.Errorf("/_last = %s", last)
	}
}

func TestStreamScenarios(t *testing.T) {
	tests := []struct {
		content  string
		contains []string
		done     bool
	}{
		{content: "hello", contains: []string{`"name":"read"`, `"finish_reason":"tool_calls"`}, done: true},
		{content: "nocost", contains: []string{`" done."`}, done: true},
		{content: "break", contains: []string{`"Reading"`}, done: false},
	}
	for _, tt := range tests {
		t.Run(tt.content, func(t *testing.T) {
			srv := start(t)
			body := readAll(t, post(context.Background(), t, srv, tt.content, true))
			for _, s := range tt.contains {
				if !strings.Contains(body, s) {
					t.Errorf("body lacks %s: %s", s, body)
				}
			}
			if strings.Contains(body, "data: [DONE]") != tt.done {
				t.Errorf("[DONE] present = %v, want %v", !tt.done, tt.done)
			}
			if tt.content == "nocost" && strings.Contains(body, `"cost"`) {
				t.Errorf("nocost reported a cost: %s", body)
			}
		})
	}
}

func TestToolResultGetsAnswer(t *testing.T) {
	srv := start(t)
	body := `{"model":"m","stream":true,"messages":[{"role":"user","content":"x"},{"role":"tool","content":"module x"}]}`
	resp, err := http.Post(srv.URL+"/api/v1/chat/completions", "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	if out := readAll(t, resp); !strings.Contains(out, `" done."`) || strings.Contains(out, "tool_calls") {
		t.Errorf("body %s", out)
	}
}

func TestGeneration(t *testing.T) {
	srv := start(t)
	var out struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal([]byte(readAll(t, post(context.Background(), t, srv, "nocost", false))), &out); err != nil {
		t.Fatal(err)
	}
	code, body := get(t, srv.URL+"/api/v1/generation?id="+out.ID, "Authorization", "Bearer k")
	if code != http.StatusOK || !strings.Contains(body, `"total_cost":4.215e-04`) {
		t.Fatalf("generation %d %s", code, body)
	}
	if _, auth := get(t, srv.URL+"/_generation_auth"); auth != "Bearer k" {
		t.Errorf("/_generation_auth = %q", auth)
	}
	if code, _ := get(t, srv.URL+"/api/v1/generation?id=nope"); code != http.StatusNotFound {
		t.Errorf("unknown generation status %d", code)
	}
}

func TestKeyAndHealth(t *testing.T) {
	srv := start(t)
	if code, body := get(t, srv.URL+"/api/v1/key"); code != http.StatusOK || !strings.Contains(body, `"limit_remaining":2.25`) {
		t.Errorf("key %d %s", code, body)
	}
	if code, _ := get(t, srv.URL+"/api/v1/credits"); code != http.StatusNotFound {
		t.Errorf("credits %d: Wrench must not depend on the management-key endpoint", code)
	}
	if code, _ := get(t, srv.URL+"/healthz"); code != http.StatusOK {
		t.Errorf("healthz %d", code)
	}
}

func TestSlowCancelCounts(t *testing.T) {
	srv := start(t)
	ctx, cancel := context.WithCancel(context.Background())
	resp := post(ctx, t, srv, "slow", true)
	line, err := bufio.NewReader(resp.Body).ReadString('\n')
	if err != nil || !strings.HasPrefix(line, ":") {
		t.Fatalf("first line %q, %v", line, err)
	}
	cancel()
	_ = resp.Body.Close()
	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, n := get(t, srv.URL+"/_cancelled"); n == "1" {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("cancellation was not counted")
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestModelCatalog(t *testing.T) {
	srv := start(t)
	code, body := get(t, srv.URL+"/api/v1/models")
	var out struct {
		Data []struct {
			ID            string `json:"id"`
			ContextLength int    `json:"context_length"`
			TopProvider   struct {
				MaxCompletionTokens *int `json:"max_completion_tokens"`
			} `json:"top_provider"`
		} `json:"data"`
	}
	if err := json.Unmarshal([]byte(body), &out); err != nil || code != http.StatusOK {
		t.Fatalf("models %d %s", code, body)
	}
	ids, nullCap := map[string]bool{}, 0
	for _, m := range out.Data {
		ids[m.ID] = m.ContextLength > 0
		if m.TopProvider.MaxCompletionTokens == nil {
			nullCap++
		}
	}
	for _, id := range []string{"anthropic/claude-opus-5.5", "z-ai/glm-5.3", "z-ai/glm-5.3-flash", "qwen/qwen3-coder", "deepseek/deepseek-v4-flash"} {
		if !ids[id] {
			t.Errorf("catalog lacks %s", id)
		}
	}
	if nullCap != 1 {
		t.Errorf("entries without max_completion_tokens: %d, want 1", nullCap)
	}
}

func TestModelCatalogCarriesPricing(t *testing.T) {
	_, body := get(t, start(t).URL+"/api/v1/models")
	var cat struct {
		Data []struct {
			ID      string `json:"id"`
			Pricing struct {
				Prompt     string `json:"prompt"`
				Completion string `json:"completion"`
			} `json:"pricing"`
		} `json:"data"`
	}
	if err := json.Unmarshal([]byte(body), &cat); err != nil || len(cat.Data) != 5 {
		t.Fatalf("catalog = %s, %v", body, err)
	}
	for _, m := range cat.Data {
		if m.Pricing.Prompt == "" || m.Pricing.Completion == "" {
			t.Errorf("%s has no pricing", m.ID)
		}
	}
}
