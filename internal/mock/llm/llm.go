// Package llm is the OpenRouter mock run by `wrench mock llm`: it imitates the OpenRouter chat completions
// API, so the demo and the end-to-end tests exercise Wrench's upstream path without credentials or spend.
//
// The last message selects the scenario: "break" drops the stream mid-way,
// "slow" stalls after the first chunk until the caller disconnects, a tool result gets a final
// text answer, and anything else asks for the read tool on go.mod.
// "nocost" completes without usage.cost, so only generation metadata can settle it.
// GET /api/v1/generation?id= serves metadata for issued generations.
// GET /api/v1/models serves a catalog with the demo models (one without max_completion_tokens).
// GET /_last returns the last request body; GET /_cancelled counts calls the caller abandoned;
// GET /_generation_auth returns the Authorization header of the last metadata lookup.
package llm

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sync"
	"sync/atomic"
	"time"
)

// modelCatalog covers the models of the repository config.yaml; qwen3-coder has no completion cap, as some
// OpenRouter entries do.
const modelCatalog = `{"data":[
{"id":"anthropic/claude-opus-5.5","context_length":200000,"top_provider":{"max_completion_tokens":32000},"pricing":{"prompt":"0.000015","completion":"0.000075"}},
{"id":"z-ai/glm-5.3","context_length":128000,"top_provider":{"max_completion_tokens":16384},"pricing":{"prompt":"0.0000006","completion":"0.0000022"}},
{"id":"z-ai/glm-5.3-flash","context_length":128000,"top_provider":{"max_completion_tokens":16384},"pricing":{"prompt":"0.0000001","completion":"0.0000004"}},
{"id":"qwen/qwen3-coder","context_length":262144,"top_provider":{"max_completion_tokens":null},"pricing":{"prompt":"0.0000002","completion":"0.0000008"}},
{"id":"deepseek/deepseek-v4-flash","context_length":64000,"top_provider":{"max_completion_tokens":8192},"pricing":{"prompt":"0.00000027","completion":"0.0000011"}}]}`

// Cost is reported in exponent form on purpose: Wrench must parse it exactly.
const cost = "4.215e-04"

type server struct {
	mu        sync.Mutex
	last      []byte
	issued    map[string]bool
	genAuth   string
	calls     atomic.Int64
	cancelled atomic.Int64
}

// Handler returns a fresh mock; each call has its own request log and counters.
func Handler() http.Handler {
	s := &server{issued: map[string]bool{}}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/v1/chat/completions", s.complete)
	mux.HandleFunc("GET /_last", func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		defer s.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(s.last)
	})
	mux.HandleFunc("GET /api/v1/generation", s.generation)
	mux.HandleFunc("GET /api/v1/models", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, modelCatalog)
	})
	// GET /api/v1/key describes the caller's key, as OpenRouter does for any regular key: the readiness probe reads
	// it, and its fixed $2.25 limit_remaining drives the low-balance alert.
	mux.HandleFunc("GET /api/v1/key", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"data":{"label":"sk-or-v1-fake","limit":12.5,"limit_remaining":2.25,"usage":10.25}}`)
	})
	mux.HandleFunc("GET /_generation_auth", func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		defer s.mu.Unlock()
		_, _ = io.WriteString(w, s.genAuth)
	})
	mux.HandleFunc("GET /_cancelled", func(w http.ResponseWriter, r *http.Request) {
		_, _ = fmt.Fprint(w, s.cancelled.Load())
	})
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {})
	return mux
}

type request struct {
	Model    string `json:"model"`
	Stream   bool   `json:"stream"`
	Messages []struct {
		Role    string `json:"role"`
		Content any    `json:"content"`
	} `json:"messages"`
}

func (s *server) complete(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	s.mu.Lock()
	s.last = body
	s.mu.Unlock()
	var req request
	if err := json.Unmarshal(body, &req); err != nil {
		http.Error(w, `{"error":{"message":"bad json"}}`, http.StatusBadRequest)
		return
	}
	scenario := ""
	if n := len(req.Messages); n > 0 {
		scenario, _ = req.Messages[n-1].Content.(string)
		if req.Messages[n-1].Role == "tool" {
			scenario = "answer"
		}
	}
	gen := fmt.Sprintf("gen-fake-%d", s.calls.Add(1))
	s.mu.Lock()
	s.issued[gen] = true
	s.mu.Unlock()
	usage := `{"prompt_tokens":120,"completion_tokens":30,"total_tokens":150,"prompt_tokens_details":{"cached_tokens":100},"completion_tokens_details":{"reasoning_tokens":7},"cost":` + cost + `}`
	if scenario == "nocost" {
		usage = `{"prompt_tokens":120,"completion_tokens":30,"total_tokens":150}`
	}

	if !req.Stream {
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprintf(w, `{"id":%q,"object":"chat.completion","created":%d,"model":%q,"provider":"FakeProvider","choices":[{"index":0,"message":{"role":"assistant","content":"done"},"finish_reason":"stop"}],"usage":%s}`,
			gen, time.Now().Unix(), req.Model, usage)
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	flusher := w.(http.Flusher)
	chunk := func(delta, finish string, extra string) {
		f := "null"
		if finish != "" {
			f = `"` + finish + `"`
		}
		_, _ = fmt.Fprintf(w, "data: {\"id\":%q,\"object\":\"chat.completion.chunk\",\"created\":%d,\"model\":%q,\"provider\":\"FakeProvider\",\"choices\":[{\"index\":0,\"delta\":%s,\"finish_reason\":%s}]%s}\n\n",
			gen, time.Now().Unix(), req.Model, delta, f, extra)
		flusher.Flush()
	}
	_, _ = io.WriteString(w, ": OPENROUTER PROCESSING\n\n")
	chunk(`{"role":"assistant","content":"Reading"}`, "", "")

	switch scenario {
	case "break":
		if hj, ok := w.(http.Hijacker); ok {
			conn, _, _ := hj.Hijack()
			_ = conn.Close()
		}
		return
	case "answer", "nocost":
		chunk(`{"content":" done."}`, "stop", "")
		_, _ = fmt.Fprintf(w, "data: {\"id\":%q,\"object\":\"chat.completion.chunk\",\"created\":%d,\"model\":%q,\"choices\":[],\"usage\":%s}\n\n", gen, time.Now().Unix(), req.Model, usage)
		_, _ = io.WriteString(w, "data: [DONE]\n\n")
		flusher.Flush()
		return
	case "slow":
		select {
		case <-r.Context().Done():
			s.cancelled.Add(1)
		case <-time.After(60 * time.Second):
		}
		return
	}

	chunk(`{"tool_calls":[{"index":0,"id":"call_1","type":"function","function":{"name":"read","arguments":"{\"pa"}}]}`, "", "")
	chunk(`{"tool_calls":[{"index":0,"function":{"arguments":"th\":\"go.mod\"}"}}]}`, "tool_calls", "")
	_, _ = fmt.Fprintf(w, "data: {\"id\":%q,\"object\":\"chat.completion.chunk\",\"created\":%d,\"model\":%q,\"provider\":\"FakeProvider\",\"choices\":[],\"usage\":%s}\n\n", gen, time.Now().Unix(), req.Model, usage)
	_, _ = io.WriteString(w, "data: [DONE]\n\n")
	flusher.Flush()
}

// generation imitates OpenRouter's generation metadata endpoint.
func (s *server) generation(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	s.genAuth = r.Header.Get("Authorization")
	known := s.issued[r.URL.Query().Get("id")]
	s.mu.Unlock()
	if !known {
		http.Error(w, `{"error":{"code":404,"message":"Generation not found"}}`, http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = fmt.Fprintf(w, `{"data":{"id":%q,"total_cost":%s,"provider_name":"FakeProvider"}}`, r.URL.Query().Get("id"), cost)
}
