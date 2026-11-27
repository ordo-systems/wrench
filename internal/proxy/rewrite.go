package proxy

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/ordo-systems/wrench/internal/catalog"
	"github.com/ordo-systems/wrench/internal/config"
)

// ModelCatalog reports an upstream model's token limits and whether it can be used.
type ModelCatalog interface {
	Lookup(upstreamID string) (catalog.Entry, catalog.Status)
}

// forwardedKeys is the allowlist of client fields sent upstream. Everything else is dropped,
// including routing overrides such as api_base, api_key, fallbacks, models and metadata.
var forwardedKeys = map[string]bool{
	"messages":              true,
	"tools":                 true,
	"tool_choice":           true,
	"parallel_tool_calls":   true,
	"stream":                true,
	"temperature":           true,
	"top_p":                 true,
	"stop":                  true,
	"seed":                  true,
	"response_format":       true,
	"presence_penalty":      true,
	"frequency_penalty":     true,
	"max_tokens":            true,
	"max_completion_tokens": true,
	// Prompt caching: Anthropic-style automatic breakpoint and OpenAI cache controls.
	"cache_control":        true,
	"prompt_cache_key":     true,
	"prompt_cache_options": true,
}

// requestError is a client mistake detected before any attempt is recorded.
type requestError struct {
	status  int
	message string
	// code is a machine-readable reason such as budget_exhausted; empty uses the status.
	code string
}

func (e *requestError) Error() string { return e.message }

func badRequest(format string, args ...any) error {
	return &requestError{status: http.StatusBadRequest, message: fmt.Sprintf(format, args...)}
}

// rewritten is the upstream request derived from a client request.
type rewritten struct {
	body   []byte
	model  config.Model
	stream bool
}

// modelAccess is what decides whether a caller may use a model: the configuration, the caller and the catalog.
type modelAccess struct {
	cfg     *config.Config
	catalog ModelCatalog
	email   string
}

// rewriteRequest maps the client body to the upstream body.
// Models outside the caller's tier or unavailable upstream are rejected before any attempt is recorded.
// session is the authenticated client session; it becomes OpenRouter's sticky-routing key so a
// conversation stays on the provider that holds its prompt cache.
func rewriteRequest(a modelAccess, body []byte, session string) (rewritten, error) {
	in, err := parseClientRequest(a, body)
	if err != nil {
		return rewritten{}, err
	}
	out, err := upstreamBody(in, session)
	return rewritten{body: out, model: in.model, stream: in.stream}, err
}

// clientRequest is a validated client body.
type clientRequest struct {
	fields map[string]json.RawMessage
	model  config.Model
	limits catalog.Entry
	stream bool
}

// parseClientRequest checks the fields Wrench decides on; the caller's tier must allow the model.
func parseClientRequest(a modelAccess, body []byte) (clientRequest, error) {
	var req clientRequest
	if err := json.Unmarshal(body, &req.fields); err != nil {
		return clientRequest{}, badRequest("request body must be a JSON object")
	}
	var err error
	if req.model, req.limits, err = resolveModel(a, req.fields["model"]); err != nil {
		return clientRequest{}, err
	}
	if n, ok := req.fields["n"]; ok && string(n) != "1" {
		return clientRequest{}, badRequest("only n=1 is supported")
	}
	if raw, ok := req.fields["stream"]; ok && json.Unmarshal(raw, &req.stream) != nil {
		return clientRequest{}, badRequest("stream must be a boolean")
	}
	return req, nil
}

func resolveModel(a modelAccess, raw json.RawMessage) (config.Model, catalog.Entry, error) {
	var name string
	if err := json.Unmarshal(raw, &name); err != nil {
		return config.Model{}, catalog.Entry{}, badRequest("model must be a string")
	}
	if _, ok := a.cfg.Model(name); !ok {
		return config.Model{}, catalog.Entry{}, badRequest("model %q is not available", name)
	}
	model, allowed := a.cfg.TierModel(a.email, name)
	if !allowed {
		return config.Model{}, catalog.Entry{}, &requestError{status: http.StatusForbidden,
			message: fmt.Sprintf("model %q is not allowed in your tier", name)}
	}
	limits, status := a.catalog.Lookup(model.UpstreamID)
	if status != catalog.StatusOK {
		return config.Model{}, catalog.Entry{}, &requestError{status: http.StatusServiceUnavailable, code: "model_unavailable",
			message: fmt.Sprintf("model %q is temporarily unavailable (%s)", name, status)}
	}
	return model, limits, nil
}

// upstreamBody keeps allowlisted client fields and adds the server-owned routing and limits.
func upstreamBody(in clientRequest, session string) ([]byte, error) {
	out := map[string]any{}
	for k, v := range in.fields {
		if forwardedKeys[k] {
			out[k] = v
		}
	}
	if err := clampOutputTokens(out, in.limits.MaxCompletionTokens); err != nil {
		return nil, err
	}
	out["model"] = in.model.UpstreamID
	if in.model.ReasoningEffort != "" {
		out["reasoning_effort"] = in.model.ReasoningEffort
	}
	if in.stream {
		out["stream_options"] = map[string]bool{"include_usage": true}
	}
	out["session_id"] = session
	// Anthropic caches only at explicit breakpoints. Other providers cache automatically and,
	// with require_parameters, could be excluded for an unsupported top-level cache_control.
	if _, ok := out["cache_control"]; !ok && strings.HasPrefix(in.model.UpstreamID, "anthropic/") {
		out["cache_control"] = map[string]string{"type": "ephemeral"}
	}
	// OpenRouter: report actual cost, keep the chosen provider's parameters, never fall back.
	out["usage"] = map[string]bool{"include": true}
	out["provider"] = map[string]bool{"allow_fallbacks": false, "require_parameters": true}
	return json.Marshal(out)
}

// clampOutputTokens caps client output limits at the catalog ceiling and sets the ceiling when none is given.
func clampOutputTokens(out map[string]any, limit int) error {
	found := false
	for _, key := range []string{"max_tokens", "max_completion_tokens"} {
		raw, ok := out[key]
		if !ok {
			continue
		}
		found = true
		var n int
		if err := json.Unmarshal(raw.(json.RawMessage), &n); err != nil || n <= 0 {
			return badRequest("%s must be a positive integer", key)
		}
		out[key] = min(n, limit)
	}
	if !found {
		out["max_tokens"] = limit
	}
	return nil
}
