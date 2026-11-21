package proxy

import (
	"bytes"
	"encoding/json"
	"time"

	"github.com/ordo-systems/wrench/internal/money"
	"github.com/ordo-systems/wrench/internal/store"
)

// payload is the accounting-relevant subset of an OpenAI-compatible response or stream chunk.
// Content fields are deliberately absent so prompts and completions are never retained.
type payload struct {
	ID       string        `json:"id"`
	Model    string        `json:"model"`
	Provider string        `json:"provider"`
	Usage    *usagePayload `json:"usage"`
}

// usagePayload covers OpenAI/OpenRouter usage, including Anthropic-style cache fields some providers report.
type usagePayload struct {
	PromptTokens     *int64 `json:"prompt_tokens"`
	CompletionTokens *int64 `json:"completion_tokens"`
	PromptDetails    *struct {
		CachedTokens     *int64 `json:"cached_tokens"`
		CacheWriteTokens *int64 `json:"cache_write_tokens"`
	} `json:"prompt_tokens_details"`
	CompletionDetails *struct {
		ReasoningTokens *int64 `json:"reasoning_tokens"`
	} `json:"completion_tokens_details"`
	CacheReadInputTokens     *int64       `json:"cache_read_input_tokens"`
	CacheCreationInputTokens *int64       `json:"cache_creation_input_tokens"`
	Cost                     *json.Number `json:"cost"`
}

// usageTap accumulates what the response reports about the attempt.
// It is used by one request goroutine only.
type usageTap struct {
	result store.AttemptResult
	done   bool
	// onGeneration is called once, when the generation id first appears.
	onGeneration func(string)
}

var (
	dataPrefix = []byte("data:")
	doneMarker = []byte("[DONE]")
)

// line inspects one SSE line and reports whether it carried a data event.
// Comments and blank lines are not activity: keep-alives must not hide a stalled model.
func (t *usageTap) line(l []byte, now time.Time) bool {
	data, ok := bytes.CutPrefix(bytes.TrimRight(l, "\r\n"), dataPrefix)
	if !ok {
		return false
	}
	data = bytes.TrimSpace(data)
	switch {
	case bytes.Equal(data, doneMarker):
		t.done = true
	default:
		if t.result.FirstTokenAt == nil {
			t.result.FirstTokenAt = &now
		}
		t.observe(data)
	}
	return true
}

// observe merges one JSON chunk or body. Unparseable chunks are relayed but not accounted.
func (t *usageTap) observe(data []byte) {
	var p payload
	if json.Unmarshal(data, &p) != nil {
		return
	}
	if t.result.GenerationID == "" && p.ID != "" {
		t.result.GenerationID = p.ID
		if t.onGeneration != nil {
			t.onGeneration(p.ID)
		}
	}
	if p.Model != "" {
		t.result.UpstreamModel = p.Model
	}
	if p.Provider != "" {
		t.result.InferenceProvider = p.Provider
	}
	if p.Usage != nil {
		t.mergeUsage(p.Usage)
	}
}

func (t *usageTap) mergeUsage(u *usagePayload) {
	tok := &t.result.Tokens
	set(&tok.Input, u.PromptTokens)
	set(&tok.Output, u.CompletionTokens)
	set(&tok.CacheRead, u.CacheReadInputTokens)
	set(&tok.CacheWrite, u.CacheCreationInputTokens)
	if u.PromptDetails != nil {
		set(&tok.CacheRead, u.PromptDetails.CachedTokens)
		set(&tok.CacheWrite, u.PromptDetails.CacheWriteTokens)
	}
	if u.CompletionDetails != nil {
		set(&tok.Reasoning, u.CompletionDetails.ReasoningTokens)
	}
	if u.Cost != nil {
		if c, err := money.ParseCost(*u.Cost); err == nil {
			t.result.Cost = &c
		}
	}
}

func set(dst **int64, v *int64) {
	if v != nil {
		*dst = v
	}
}
