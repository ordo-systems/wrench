// Package livecheck exercises a running Wrench end to end, as the pilot requires: for every tier model a
// streamed tool call, the answer to its tool result and a cancellation mid-stream. It then waits until the
// journal holds a confirmed cost for every attempt. Requests use a small output ceiling so a run against live
// providers costs little.
//
// A cancelled stream has no final usage, so its cost stays unknown until reconciliation, and Wrench pauses
// all inference meanwhile. Cancellations therefore run last, one at a time, and each step reports how long
// that pause lasted: a pilot metric for the unknown-cost rule.
package livecheck

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/ordo-systems/wrench/tests/internal/wrenchapi"
)

// Options configure one run. Token is a device key (wrk_...).
type Options struct {
	URL   string
	Token string
	// Models limits the checked models; empty means the caller's whole tier catalog.
	Models []string
	// CancelPrompt must produce a long answer, so cancellation happens mid-stream.
	CancelPrompt string
	// Settle is how long to wait for every attempt's cost to be confirmed.
	Settle time.Duration
	// Session names the run's session; the dashboard shows the run under it.
	Session string
	Client  *http.Client
}

// Result is the outcome of one step.
type Result struct {
	Model  string `json:"model"`
	Step   string `json:"step"`
	OK     bool   `json:"ok"`
	Detail string `json:"detail"`
}

// Attempt is the journal's record of one call, as listed by GET /api/requests.
type Attempt struct {
	ID                string  `json:"id"`
	RequestedModel    string  `json:"requested_model"`
	InferenceProvider string  `json:"inference_provider"`
	GenerationID      string  `json:"generation_id"`
	Outcome           string  `json:"outcome"`
	ErrorReason       string  `json:"error_reason"`
	Cost              *string `json:"cost"`
	CostState         string  `json:"cost_state"`
}

// Report collects a run.
type Report struct {
	Session  string    `json:"session"`
	Results  []Result  `json:"results"`
	Attempts []Attempt `json:"attempts"`
}

// OK reports whether every step passed.
func (r Report) OK() bool {
	for _, res := range r.Results {
		if !res.OK {
			return false
		}
	}
	return len(r.Results) > 0
}

const maxTokens = 256

var readTool = []map[string]any{{"type": "function", "function": map[string]any{
	"name": "read", "description": "Read a file from the repository",
	"parameters": map[string]any{"type": "object", "properties": map[string]any{"path": map[string]any{"type": "string"}},
		"required": []string{"path"}},
}}}

type checker struct {
	o Options
	// api calls the documented Wrench API; inference streams use o.Client directly.
	api    *wrenchapi.ClientWithResponses
	report Report
}

func newChecker(o Options) (*checker, error) {
	if o.Client == nil {
		o.Client = &http.Client{Timeout: 3 * time.Minute}
	}
	if o.Session == "" {
		o.Session = "livecheck-" + time.Now().UTC().Format("20060102T150405")
	}
	api, err := wrenchapi.NewClientWithResponses(o.URL, wrenchapi.WithHTTPClient(o.Client), wrenchapi.Bearer(o.Token))
	if err != nil {
		return nil, fmt.Errorf("livecheck: %w", err)
	}
	return &checker{o: o, api: api, report: Report{Session: o.Session}}, nil
}

// Run performs the checks. An error means the run could not start, for example a rejected token;
// failed steps are reported in the Report instead.
func Run(ctx context.Context, o Options) (Report, error) {
	c, err := newChecker(o)
	if err != nil {
		return Report{}, err
	}
	o = c.o
	catalog, err := c.catalog(ctx)
	if err != nil {
		return c.report, err
	}
	models := o.Models
	if len(models) == 0 {
		for _, m := range catalog.Models {
			models = append(models, m.Name)
		}
	}
	for _, m := range models {
		c.toolRound(ctx, m)
	}
	for _, m := range models {
		c.cancel(ctx, m)
	}
	c.settle(ctx)
	return c.report, nil
}

// catalog reads the models of the caller's tier.
func (c *checker) catalog(ctx context.Context) (wrenchapi.ModelList, error) {
	resp, err := c.api.ListModelsWithResponse(ctx)
	if err != nil {
		return wrenchapi.ModelList{}, err
	}
	if resp.JSON200 == nil {
		return wrenchapi.ModelList{}, fmt.Errorf("livecheck: model catalog status %d", resp.StatusCode())
	}
	return *resp.JSON200, nil
}

func (c *checker) do(ctx context.Context, method, path string, body any, header map[string]string) (*http.Response, error) {
	var r io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		r = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.o.URL+path, r)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+c.o.Token)
	req.Header.Set("Content-Type", "application/json")
	for k, v := range header {
		req.Header.Set(k, v)
	}
	return c.o.Client.Do(req)
}

func (c *checker) add(model, step string, err error, detail string) {
	r := Result{Model: model, Step: step, OK: err == nil, Detail: detail}
	if err != nil {
		r.Detail = err.Error()
	}
	c.report.Results = append(c.report.Results, r)
}

// stream is what a streamed chat completion delivered.
type stream struct {
	chunks    int
	content   string
	toolID    string
	toolName  string
	toolArgs  string
	finish    string
	done      bool
	usageSeen bool
}

// chat sends a streamed request; stopAfterFirst cancels after the first data chunk.
func (c *checker) chat(ctx context.Context, model string, messages []map[string]any, tools bool, stopAfterFirst bool) (stream, error) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	body := map[string]any{"model": model, "stream": true, "max_tokens": maxTokens, "messages": messages}
	if tools {
		body["tools"] = readTool
	}
	resp, err := c.do(ctx, http.MethodPost, "/v1/chat/completions", body,
		map[string]string{"X-Wrench-Session-Id": c.o.Session})
	if err != nil {
		return stream{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return stream{}, fmt.Errorf("status %d: %s", resp.StatusCode, strings.TrimSpace(string(msg)))
	}
	var s stream
	sc := bufio.NewScanner(resp.Body)
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	for sc.Scan() {
		data, ok := strings.CutPrefix(sc.Text(), "data: ")
		if !ok {
			continue
		}
		if data == "[DONE]" {
			s.done = true
			break
		}
		s.add(data)
		if stopAfterFirst && s.chunks > 0 {
			cancel()
			return s, nil
		}
	}
	return s, sc.Err()
}

func (s *stream) add(data string) {
	var chunk struct {
		Choices []struct {
			Delta struct {
				Content   string `json:"content"`
				ToolCalls []struct {
					ID       string `json:"id"`
					Function struct {
						Name      string `json:"name"`
						Arguments string `json:"arguments"`
					} `json:"function"`
				} `json:"tool_calls"`
			} `json:"delta"`
			FinishReason *string `json:"finish_reason"`
		} `json:"choices"`
		Usage json.RawMessage `json:"usage"`
	}
	if json.Unmarshal([]byte(data), &chunk) != nil {
		return
	}
	s.chunks++
	if len(chunk.Usage) > 0 && string(chunk.Usage) != "null" {
		s.usageSeen = true
	}
	for _, ch := range chunk.Choices {
		s.content += ch.Delta.Content
		for _, tc := range ch.Delta.ToolCalls {
			if tc.ID != "" {
				s.toolID = tc.ID
			}
			if tc.Function.Name != "" {
				s.toolName = tc.Function.Name
			}
			s.toolArgs += tc.Function.Arguments
		}
		if ch.FinishReason != nil {
			s.finish = *ch.FinishReason
		}
	}
}

// toolRound checks that a tool call streams intact and that the model answers its result.
func (c *checker) toolRound(ctx context.Context, model string) {
	user := map[string]any{"role": "user", "content": "Read the file go.mod with the read tool. Do not answer without calling it."}
	first, err := c.chat(ctx, model, []map[string]any{user}, true, false)
	if err == nil {
		err = checkToolCall(first)
	}
	c.add(model, "tool call", err, fmt.Sprintf("%s(%s)", first.toolName, first.toolArgs))
	if err != nil {
		return
	}
	call := map[string]any{"role": "assistant", "content": nil, "tool_calls": []map[string]any{{"id": first.toolID, "type": "function",
		"function": map[string]any{"name": first.toolName, "arguments": first.toolArgs}}}}
	result := map[string]any{"role": "tool", "tool_call_id": first.toolID, "content": "module example.com/demo\n\ngo 1.27\n"}
	second, err := c.chat(ctx, model, []map[string]any{user, call, result}, true, false)
	if err == nil && !second.done && second.finish == "" {
		err = errors.New("stream ended without finish_reason or [DONE]")
	}
	c.add(model, "tool result", err, fmt.Sprintf("%d chunks, finish %q, usage %t", second.chunks, second.finish, second.usageSeen))
}

func checkToolCall(s stream) error {
	switch {
	case s.toolName != "read":
		return fmt.Errorf("no read tool call (finish %q, content %q)", s.finish, truncate(s.content))
	case !json.Valid([]byte(s.toolArgs)):
		return fmt.Errorf("tool arguments are not valid JSON: %q", s.toolArgs)
	case !s.done && s.finish == "":
		return errors.New("stream ended without finish_reason or [DONE]")
	}
	return nil
}

func truncate(s string) string {
	if len(s) > 80 {
		return s[:80] + "…"
	}
	return s
}

// cancel abandons a stream after its first chunk; the journal must record the attempt as cancelled.
func (c *checker) cancel(ctx context.Context, model string) {
	prompt := c.o.CancelPrompt
	if prompt == "" {
		prompt = "Write the numbers from 1 to 300, one per line, and nothing else."
	}
	s, err := c.chat(ctx, model, []map[string]any{{"role": "user", "content": prompt}}, false, true)
	if err == nil && s.chunks == 0 {
		err = errors.New("no chunk before cancelling")
	}
	c.add(model, "cancel", err, fmt.Sprintf("cancelled after %d chunk(s)", s.chunks))
	if err != nil {
		return
	}
	closed, err := c.waitClosed(ctx)
	c.add(model, "close after cancel", err, fmt.Sprintf("the cancelled attempt was closed after %s", closed.Round(time.Second)))
}

// waitClosed waits until the cancelled attempt is recorded as finished (the proxy closes it after it notices
// the disconnect) and returns how long that took.
func (c *checker) waitClosed(ctx context.Context) (time.Duration, error) {
	start := time.Now()
	for {
		attempts, err := c.attempts(ctx)
		if err != nil {
			return time.Since(start), err
		}
		running := false
		for _, a := range attempts {
			running = running || a.Outcome == ""
		}
		if !running {
			return time.Since(start), nil
		}
		if time.Since(start) > c.o.Settle {
			return time.Since(start), errors.New("the cancelled attempt was never closed")
		}
		select {
		case <-ctx.Done():
			return time.Since(start), ctx.Err()
		case <-time.After(200 * time.Millisecond):
		}
	}
}

// settle waits until every attempt of the session finished with a confirmed cost.
func (c *checker) settle(ctx context.Context) {
	deadline := time.Now().Add(c.o.Settle)
	for {
		attempts, err := c.attempts(ctx)
		if err != nil {
			c.add("", "accounting", err, "")
			return
		}
		c.report.Attempts = attempts
		pending := 0
		for _, a := range attempts {
			if a.Outcome == "" || a.CostState != "confirmed" {
				pending++
			}
		}
		if pending == 0 || time.Now().After(deadline) {
			var err error
			if pending > 0 {
				err = fmt.Errorf("%d of %d attempts have no confirmed cost after %s", pending, len(attempts), c.o.Settle)
			}
			c.add("", "accounting", err, fmt.Sprintf("%d attempts", len(attempts)))
			return
		}
		select {
		case <-ctx.Done():
			c.add("", "accounting", ctx.Err(), "")
			return
		case <-time.After(2 * time.Second):
		}
	}
}

func (c *checker) attempts(ctx context.Context) ([]Attempt, error) {
	limit, session := "1000", c.o.Session
	resp, err := c.api.ListRequestsWithResponse(ctx, &wrenchapi.ListRequestsParams{Limit: &limit, Session: &session})
	if err != nil {
		return nil, err
	}
	if resp.JSON200 == nil {
		return nil, fmt.Errorf("requests status %d", resp.StatusCode())
	}
	out := make([]Attempt, 0, len(resp.JSON200.Attempts))
	for _, a := range resp.JSON200.Attempts {
		out = append(out, Attempt{ID: a.Id, RequestedModel: a.RequestedModel,
			InferenceProvider: a.InferenceProvider, GenerationID: a.GenerationId, Outcome: string(a.Outcome),
			ErrorReason: a.ErrorReason, Cost: a.Cost, CostState: string(a.CostState)})
	}
	return out, nil
}
