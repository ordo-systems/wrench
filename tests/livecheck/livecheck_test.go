package livecheck

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestStreamCollectsSplitToolCalls(t *testing.T) {
	var s stream
	for _, d := range []string{
		`{"choices":[{"delta":{"role":"assistant","content":"Reading"}}]}`,
		`{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_1","function":{"name":"read","arguments":"{\"pa"}}]}}]}`,
		`{"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"th\":\"go.mod\"}"}}]},"finish_reason":"tool_calls"}]}`,
		`{"choices":[],"usage":{"prompt_tokens":1}}`,
		`not json`,
	} {
		s.add(d)
	}
	if s.chunks != 4 || s.toolID != "call_1" || s.toolArgs != `{"path":"go.mod"}` || s.finish != "tool_calls" || !s.usageSeen || s.content != "Reading" {
		t.Fatalf("stream = %+v", s)
	}
	if err := checkToolCall(s); err != nil {
		t.Fatal(err)
	}
}

func TestCheckToolCallRejects(t *testing.T) {
	cases := map[string]stream{
		"no tool call": {finish: "stop", content: "I cannot read files."},
		"broken args":  {toolName: "read", toolArgs: `{"path":`, finish: "tool_calls"},
		"truncated":    {toolName: "read", toolArgs: `{}`},
	}
	for name, s := range cases {
		if err := checkToolCall(s); err == nil {
			t.Errorf("%s accepted", name)
		}
	}
	if err := checkToolCall(stream{content: strings.Repeat("x", 200)}); err == nil || !strings.Contains(err.Error(), "…") {
		t.Errorf("long content not truncated: %v", err)
	}
}

func TestReportOK(t *testing.T) {
	if (Report{}).OK() {
		t.Fatal("an empty run passed")
	}
	if !(Report{Results: []Result{{OK: true}}}).OK() || (Report{Results: []Result{{OK: true}, {OK: false}}}).OK() {
		t.Fatal("OK misreports")
	}
}

// TestCatalogUsesTheAPIClient checks the catalog request a run starts with: the device key and the decoded
// models and default.
func TestCatalogUsesTheAPIClient(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/models" || r.Header.Get("Authorization") != "Bearer wrk_test" {
			http.Error(w, "unexpected request "+r.URL.String(), http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"models":[{"name":"glm-flash","input_token_limit":1,"max_output_tokens":1,"reasoning":false}],"default":"glm-flash"}`))
	}))
	defer srv.Close()
	c, err := newChecker(Options{URL: srv.URL, Token: "wrk_test"})
	if err != nil {
		t.Fatal(err)
	}
	cat, err := c.catalog(context.Background())
	if err != nil || cat.Default == nil || *cat.Default != "glm-flash" || cat.Models[0].Name != "glm-flash" {
		t.Fatalf("catalog = %+v, %v", cat, err)
	}
}
