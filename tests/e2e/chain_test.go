//go:build integration

// Package e2e drives the phase-1 chain through real containers:
// client -> Wrench -> fake OpenRouter, then reads the journal through the dashboard API.
// Run it with `make e2e`.
package e2e

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/ordo-systems/wrench/tests/internal/wrenchapi"
)

// token is user1@example.com's device key, obtained once per run in TestMain.
var token string

func baseURL(t *testing.T, name string) string {
	t.Helper()
	v := os.Getenv(name)
	if v == "" {
		t.Skip(name + " is not set; run make e2e")
	}
	return v
}

func chat(ctx context.Context, t *testing.T, session, body string) *http.Response {
	t.Helper()
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, baseURL(t, "WRENCH_E2E_URL")+"/v1/chat/completions", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Wrench-Session-Id", session)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	return resp
}

func streamBody(content string) string {
	return `{"model":"glm-5.3","stream":true,"messages":[{"role":"user","content":"` + content + `"}],
		"tools":[{"type":"function","function":{"name":"read","parameters":{"type":"object","properties":{"path":{"type":"string"}}}}}]}`
}

// attempts polls the dashboard API until the session has a finished attempt.
func attempts(t *testing.T, session string) []attempt {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for {
		resp, err := client(t).ListRequestsWithResponse(context.Background(), &wrenchapi.ListRequestsParams{Session: &session})
		if err != nil {
			t.Fatal(err)
		}
		if resp.JSON200 == nil {
			t.Fatalf("requests: %d %s", resp.StatusCode(), resp.Body)
		}
		out := resp.JSON200
		if len(out.Attempts) > 0 && out.Attempts[0].Outcome != "" {
			return out.Attempts
		}
		if time.Now().After(deadline) {
			t.Fatalf("no finished attempt for session %s: %+v", session, out.Attempts)
		}
		time.Sleep(200 * time.Millisecond)
	}
}

func fake(t *testing.T, path string) []byte {
	t.Helper()
	resp, err := http.Get(baseURL(t, "WRENCH_E2E_OPENROUTER_URL") + path)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return b
}

// api sends a JSON request with the e2e key and returns the status and body.
func api(t *testing.T, method, path, body string, header map[string]string) (int, []byte) {
	t.Helper()
	return sendJSON(t, token, method, path, body, header)
}

// apiAs is api with another device key.
func apiAs(t *testing.T, key, method, path, body string) (int, []byte) {
	t.Helper()
	return sendJSON(t, key, method, path, body, nil)
}

func sendJSON(t *testing.T, key, method, path, body string, header map[string]string) (int, []byte) {
	t.Helper()
	req, _ := http.NewRequest(method, baseURL(t, "WRENCH_E2E_URL")+path, strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+key)
	req.Header.Set("Content-Type", "application/json")
	for k, v := range header {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, b
}

func session(t *testing.T) string {
	return "e2e-" + strings.ReplaceAll(t.Name(), "/", "-") + "-" + time.Now().Format("150405.000000")
}

func TestStreamWithToolCallIsAccounted(t *testing.T) {
	s := session(t)
	resp := chat(context.Background(), t, s, streamBody("read go.mod"))
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		t.Fatalf("status %d: %s", resp.StatusCode, b)
	}
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		t.Fatalf("status %d: %s", resp.StatusCode, b)
	}

	// Reassemble the tool call the way a client does: concatenate argument deltas by index.
	var args, name string
	done := false
	sc := bufio.NewScanner(resp.Body)
	for sc.Scan() {
		data, ok := strings.CutPrefix(sc.Text(), "data: ")
		if !ok {
			continue
		}
		if data == "[DONE]" {
			done = true
			continue
		}
		var c struct {
			Choices []struct {
				Delta struct {
					ToolCalls []struct {
						Function struct{ Name, Arguments string }
					} `json:"tool_calls"`
				}
			}
		}
		if err := json.Unmarshal([]byte(data), &c); err != nil {
			t.Fatalf("bad chunk %q: %v", data, err)
		}
		for _, ch := range c.Choices {
			for _, tc := range ch.Delta.ToolCalls {
				name += tc.Function.Name
				args += tc.Function.Arguments
			}
		}
	}
	if !done || name != "read" || args != `{"path":"go.mod"}` {
		t.Fatalf("done=%v tool=%q args=%q", done, name, args)
	}

	// What Wrench actually sent to "OpenRouter".
	var sent map[string]any
	if err := json.Unmarshal(fake(t, "/_last"), &sent); err != nil {
		t.Fatal(err)
	}
	t.Logf("upstream body keys: %v", keys(sent))
	if sent["model"] != "z-ai/glm-5.3" {
		t.Errorf("upstream model = %v", sent["model"])
	}
	if _, ok := sent["num_retries"]; ok {
		t.Error("num_retries sent to OpenRouter")
	}
	if _, ok := sent["tools"]; !ok {
		t.Error("tools not forwarded")
	}
	if u, _ := sent["usage"].(map[string]any); u["include"] != true {
		t.Errorf("usage accounting not forwarded: %v", sent["usage"])
	}
	if p, _ := sent["provider"].(map[string]any); p["allow_fallbacks"] != false {
		t.Errorf("provider routing not forwarded: %v", sent["provider"])
	}

	a := attempts(t, s)[0]
	t.Logf("attempt: %+v", a)
	// OpenRouter's bytes reach Wrench unchanged, so the chunk-level provider is recorded with the attempt.
	if a.InferenceProvider != "FakeProvider" {
		t.Errorf("provider = %q", a.InferenceProvider)
	}
	if a.Outcome != outcomeSuccess || a.CostState != costConfirmed || a.Cost == nil || cost(a) != "0.0004215" {
		t.Errorf("cost not traced to provider: outcome %s state %s cost %v", a.Outcome, a.CostState, cost(a))
	}
	if !strings.HasPrefix(a.GenerationId, "gen-fake-") {
		t.Errorf("generation id = %q", a.GenerationId)
	}
	if a.Tokens.Input == nil || *a.Tokens.Input != 120 || a.Tokens.Output == nil || *a.Tokens.Output != 30 {
		t.Errorf("tokens = %+v", a.Tokens)
	}
	if a.Tokens.CacheRead == nil || *a.Tokens.CacheRead != 100 || a.Tokens.Reasoning == nil || *a.Tokens.Reasoning != 7 {
		t.Errorf("cache/reasoning tokens = %+v", a.Tokens)
	}
}

// reconciled polls until the attempt's cost is no longer pending. The provider cannot signal it: OpenRouter's
// chunk-level provider is recorded before reconciliation runs.
func reconciled(t *testing.T, session string) attempt {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for {
		a := attempts(t, session)[0]
		if a.CostState != costPending || time.Now().After(deadline) {
			return a
		}
		time.Sleep(500 * time.Millisecond)
	}
}

// TestUnknownCostIsSettledFromGenerationMetadata covers a response without usage.cost: the attempt
// stays pending until Wrench reads the generation from OpenRouter.
func TestUnknownCostIsSettledFromGenerationMetadata(t *testing.T) {
	s := session(t)
	resp := chat(context.Background(), t, s, streamBody("nocost"))
	_, _ = io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	if a := attempts(t, s)[0]; a.CostState != costPending {
		t.Fatalf("cost known without metadata: %+v", a)
	}
	a := reconciled(t, s)
	if a.CostState != costConfirmed || a.Cost == nil || cost(a) != "0.0004215" || a.InferenceProvider != "FakeProvider" {
		t.Fatalf("not settled from generation metadata: %+v", a)
	}
	// Wrench reads metadata with the OpenRouter key.
	if got := string(fake(t, "/_generation_auth")); got != "Bearer demo-openrouter-key" {
		t.Fatalf("upstream metadata auth = %q", got)
	}
}

func TestNonStreamIsAccounted(t *testing.T) {
	s := session(t)
	resp := chat(context.Background(), t, s, `{"model":"glm-5.3","messages":[{"role":"user","content":"hi"}]}`)
	b, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK || !strings.Contains(string(b), `"done"`) {
		t.Fatalf("status %d: %s", resp.StatusCode, b)
	}
	a := attempts(t, s)[0]
	if a.Outcome != outcomeSuccess || a.Cost == nil || cost(a) != "0.0004215" || !strings.HasPrefix(a.GenerationId, "gen-fake-") {
		t.Errorf("attempt = %+v", a)
	}
}

func TestBrokenStreamIsRecordedAsError(t *testing.T) {
	s := session(t)
	resp := chat(context.Background(), t, s, streamBody("break"))
	_, _ = io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	a := attempts(t, s)[0]
	t.Logf("attempt: %+v", a)
	if a.Outcome != outcomeError || a.CostState != costPending {
		t.Errorf("outcome %s (%s), cost state %s", a.Outcome, a.ErrorReason, a.CostState)
	}
	// The generation id was recorded from the first chunk, so reconciliation settles the cost
	// and lifts the global pause that the unknown cost imposed.
	if a := reconciled(t, s); a.CostState != costConfirmed {
		t.Fatalf("broken stream not reconciled: %+v", a)
	}
}

func TestClientCancelReachesProvider(t *testing.T) {
	s := session(t)
	before := string(fake(t, "/_cancelled"))
	ctx, cancel := context.WithCancel(context.Background())
	resp := chat(ctx, t, s, streamBody("slow"))
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		t.Fatalf("status %d: %s", resp.StatusCode, b)
	}
	br := bufio.NewReader(resp.Body)
	for {
		line, err := br.ReadString('\n')
		if err != nil {
			t.Fatalf("stream ended before the first chunk: %v", err)
		}
		if strings.HasPrefix(line, "data:") {
			break
		}
	}
	cancel()
	resp.Body.Close()

	a := attempts(t, s)[0]
	if a.Outcome != outcomeCancelled {
		t.Errorf("outcome %s (%s)", a.Outcome, a.ErrorReason)
	}
	deadline := time.Now().Add(10 * time.Second)
	for string(fake(t, "/_cancelled")) == before {
		if time.Now().After(deadline) {
			t.Fatal("cancellation did not propagate to the provider")
		}
		time.Sleep(200 * time.Millisecond)
	}
	if a := reconciled(t, s); a.CostState != costConfirmed {
		t.Fatalf("cancelled attempt not reconciled: %+v", a)
	}
}

func keys(m map[string]any) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
