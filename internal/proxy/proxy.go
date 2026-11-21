// Package proxy forwards OpenAI-compatible inference requests from clients to OpenRouter
// and records each upstream attempt in the journal.
package proxy

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/ordo-systems/wrench/internal/auth"
	"github.com/ordo-systems/wrench/internal/budget"
	"github.com/ordo-systems/wrench/internal/config"
	"github.com/ordo-systems/wrench/internal/env"
	"github.com/ordo-systems/wrench/internal/store"
)

// Client headers. Requests without a session are rejected; the request ID is optional. opencode sends its own
// session id, which is used when X-Wrench-Session-Id is absent.
const (
	SessionHeader         = "X-Wrench-Session-Id"
	OpencodeSessionHeader = "X-Opencode-Session-Id"
	RequestHeader         = "X-Wrench-Request-Id"
	// BudgetWarningHeader lists reached budget thresholds on admitted requests.
	BudgetWarningHeader = "X-Wrench-Budget-Warning"
)

const (
	maxRequestBody  = 32 << 20
	maxErrorBody    = 1 << 20
	maxResponseBody = 64 << 20
	providerName    = "openrouter"
)

// clientID admits opencode's ids such as ses_e2067994affelpQh9Yv9RwjXGP.
var clientID = regexp.MustCompile(`^[A-Za-z0-9_.:-]{1,128}$`)

// ValidClientID reports whether s is acceptable as a client-chosen session or request ID.
func ValidClientID(s string) bool { return clientID.MatchString(s) }

var (
	errFirstToken = errors.New("upstream first-token timeout")
	errIdle       = errors.New("upstream stream idle timeout")
	errTotal      = errors.New("upstream total timeout")
)

// Journal is the subset of the store the proxy needs.
type Journal interface {
	TouchSession(ctx context.Context, id, userEmail string, keyID *int64, agent string, at time.Time) error
	StartAttempt(ctx context.Context, a store.Attempt) error
	RecordGeneration(ctx context.Context, attemptID, generationID string) error
	FinishAttempt(ctx context.Context, id string, r store.AttemptResult) error
}

// Admission decides from recorded spend whether a request may start; an error means accounting is down.
type Admission interface {
	Check(ctx context.Context, email string) (budget.Decision, error)
}

// Handler serves POST /v1/chat/completions behind auth.Middleware.
// It resolves the catalog model, rewrites it to the upstream ID with catalog limits and reasoning,
// disables retries and fallbacks, records a pending attempt and relays the response unchanged.
type Handler struct {
	cfg       *config.Holder
	catalog   ModelCatalog
	upstream  env.UpstreamEnv
	journal   Journal
	admission Admission
	limiter   *limiter
	client    *http.Client
	// maxResponse caps a non-streaming response body; larger bodies fail instead of being truncated.
	maxResponse int64
	now         func() time.Time
	log         *slog.Logger
}

// New builds the proxy handler. Its client neither follows redirects nor retries.
func New(cfg *config.Holder, catalog ModelCatalog, upstream env.UpstreamEnv, journal Journal, admission Admission) *Handler {
	transport := &http.Transport{
		DialContext:         (&net.Dialer{Timeout: upstream.TimeoutConnect}).DialContext,
		TLSHandshakeTimeout: upstream.TimeoutConnect,
		MaxIdleConnsPerHost: 64,
	}
	return &Handler{
		cfg:         cfg,
		catalog:     catalog,
		upstream:    upstream,
		journal:     journal,
		admission:   admission,
		limiter:     newLimiter(upstream.ConcurrencyPerUser, upstream.ConcurrencyGlobal),
		maxResponse: maxResponseBody,
		client: &http.Client{
			Transport:     transport,
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		},
		now: func() time.Time { return time.Now().UTC() },
		log: slog.Default(),
	}
}

// ServeHTTP proxies one request as exactly one upstream call.
// Client disconnect cancels the upstream call; the attempt is finished as cancelled with whatever usage was reported.
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	id, ok := auth.FromContext(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthenticated")
		return
	}
	attempt, req, err := h.admit(w, r, id)
	if err != nil {
		var re *requestError
		if errors.As(err, &re) {
			writeCodedError(w, re.status, re.message, re.code)
			return
		}
		h.log.Error("request denied: accounting unavailable", "err", err)
		writeError(w, http.StatusServiceUnavailable, "accounting is unavailable")
		return
	}
	defer h.limiter.release(id.Email)
	res := h.forward(w, r, req, attempt.ID)
	res.Provider = providerName
	ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), 10*time.Second)
	defer cancel()
	if err := h.journal.FinishAttempt(ctx, attempt.ID, res); err != nil {
		h.log.Error("attempt result not recorded; it stays pending", "attempt", attempt.ID, "err", err)
	}
}

// admit validates the request, applies budgets and concurrency limits, and records the pending
// attempt; nothing is forwarded on error. On success the caller owns one limiter slot.
func (h *Handler) admit(w http.ResponseWriter, r *http.Request, id auth.Identity) (store.Attempt, rewritten, error) {
	ids, err := parseClientIDs(r)
	if err != nil {
		return store.Attempt{}, rewritten{}, err
	}
	body, err := io.ReadAll(http.MaxBytesReader(nil, r.Body, maxRequestBody))
	if err != nil {
		return store.Attempt{}, rewritten{}, &requestError{status: http.StatusRequestEntityTooLarge, message: "request body is too large"}
	}
	req, err := rewriteRequest(modelAccess{cfg: config.For(r.Context(), h.cfg), catalog: h.catalog, email: id.Email}, body)
	if err != nil {
		return store.Attempt{}, rewritten{}, err
	}
	if err := h.checkBudget(r.Context(), w, id.Email); err != nil {
		return store.Attempt{}, rewritten{}, err
	}
	if !h.limiter.acquire(id.Email) {
		return store.Attempt{}, rewritten{}, &requestError{status: http.StatusTooManyRequests, code: "concurrency_limit",
			message: "too many concurrent requests; wait for running requests to finish"}
	}
	a, err := h.record(r, id, ids, req)
	if err != nil {
		h.limiter.release(id.Email)
	}
	return a, req, err
}

// checkBudget refuses exhausted budgets and unsettled charges, and announces reached thresholds.
// Refusals are 402 so clients show the reason instead of retrying.
func (h *Handler) checkBudget(ctx context.Context, w http.ResponseWriter, email string) error {
	d, err := h.admission.Check(ctx, email)
	if err != nil {
		return err
	}
	id, _ := auth.FromContext(ctx)
	admin := id.Role == config.RoleAdmin
	if !d.Allowed {
		message := d.Reason
		if !admin && d.Refused != nil && d.Refused.Scope == budget.ScopeGlobal {
			message = fmt.Sprintf("shared %s budget exhausted; resets at %s", d.Refused.Window, d.Refused.ResetsAt.Format(time.RFC3339))
		}
		return &requestError{status: http.StatusPaymentRequired, code: d.Code, message: message}
	}
	var warnings []string
	for _, u := range d.Warnings {
		if !admin && u.Scope == budget.ScopeGlobal {
			// Global amounts reveal everyone's spend; administrators only.
			warnings = append(warnings, fmt.Sprintf("%s %s %d%%", u.Scope, u.Window, u.Percent))
			continue
		}
		warnings = append(warnings, fmt.Sprintf("%s %s %d%% ($%s of $%s)", u.Scope, u.Window, u.Percent, u.Spent, u.Limit))
	}
	if len(warnings) > 0 {
		w.Header().Set(BudgetWarningHeader, strings.Join(warnings, "; "))
	}
	return nil
}

// record touches the session and persists the pending attempt before anything is forwarded.
func (h *Handler) record(r *http.Request, id auth.Identity, ids clientIDs, req rewritten) (store.Attempt, error) {
	attemptID := newID()
	if ids.request == "" {
		ids.request = attemptID
	}
	now := h.now()
	if err := h.journal.TouchSession(r.Context(), ids.session, id.Email, keyOf(id), agentFromUserAgent(r.Header.Get("User-Agent")), now); err != nil {
		if errors.Is(err, store.ErrSessionOwner) {
			return store.Attempt{}, &requestError{status: http.StatusForbidden, message: "session belongs to another user"}
		}
		return store.Attempt{}, err
	}
	a := store.Attempt{
		ID: attemptID, RequestID: ids.request, UserEmail: id.Email, SessionID: ids.session,
		RequestedModel: req.model.Name, StartedAt: now,
	}
	return a, h.journal.StartAttempt(r.Context(), a)
}

// clientIDs are the attribution headers sent by the client.
type clientIDs struct{ session, request string }

func parseClientIDs(r *http.Request) (clientIDs, error) {
	header := SessionHeader
	if r.Header.Get(SessionHeader) == "" && r.Header.Get(OpencodeSessionHeader) != "" {
		header = OpencodeSessionHeader
	}
	ids := clientIDs{session: r.Header.Get(header)}
	if !clientID.MatchString(ids.session) {
		return ids, badRequest("%s must match %s", header, clientID)
	}
	var err error
	if ids.request, err = optionalID(r, RequestHeader); err != nil {
		return ids, err
	}
	return ids, nil
}

func optionalID(r *http.Request, header string) (string, error) {
	v := r.Header.Get(header)
	if v != "" && !clientID.MatchString(v) {
		return "", badRequest("%s must match %s", header, clientID)
	}
	return v, nil
}

// forward performs the single upstream call and relays its response.
func (h *Handler) forward(w http.ResponseWriter, r *http.Request, req rewritten, attemptID string) store.AttemptResult {
	ctx, cancel := context.WithCancelCause(r.Context())
	defer cancel(nil)
	ctx, stop := context.WithTimeoutCause(ctx, h.upstream.TimeoutTotal, errTotal)
	defer stop()
	var act *activity
	if req.stream {
		act = newActivity(cancel, h.upstream.TimeoutFirstToken, h.upstream.TimeoutStreamIdle)
		defer act.stop()
	}

	up, err := http.NewRequestWithContext(ctx, http.MethodPost, h.upstream.URL.JoinPath("chat", "completions").String(), bytes.NewReader(req.body))
	if err != nil {
		return h.failed(ctx, w, r, err)
	}
	up.Header.Set("Content-Type", "application/json")
	up.Header.Set("Authorization", "Bearer "+h.upstream.Key.Reveal())
	resp, err := h.client.Do(up)
	if err != nil {
		return h.failed(ctx, w, r, err)
	}
	defer resp.Body.Close()

	tap := &usageTap{onGeneration: func(gen string) { h.recordGeneration(r.Context(), attemptID, gen) }}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		relayError(w, resp)
		return h.finish(ctx, r, tap, "upstream status "+strconv.Itoa(resp.StatusCode))
	}
	if req.stream {
		return h.relayStream(ctx, w, r, resp, tap, act)
	}
	return h.relayBody(ctx, w, r, resp, tap)
}

// recordGeneration persists the generation id early; failure only costs crash recovery, not the request.
func (h *Handler) recordGeneration(ctx context.Context, attemptID, gen string) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
	defer cancel()
	if err := h.journal.RecordGeneration(ctx, attemptID, gen); err != nil {
		h.log.Warn("generation id not recorded early", "attempt", attemptID, "err", err)
	}
}

// failed reports an upstream call that produced no response. A failed dial proves the request
// never reached OpenRouter, so it is recorded as not sent instead of blocking everyone as unsettled.
func (h *Handler) failed(ctx context.Context, w http.ResponseWriter, r *http.Request, err error) store.AttemptResult {
	res := h.finish(ctx, r, &usageTap{}, "upstream request failed")
	var op *net.OpError
	res.NotSent = errors.As(err, &op) && op.Op == "dial"
	if res.Outcome == store.OutcomeError {
		status := http.StatusBadGateway
		if errors.Is(context.Cause(ctx), errTotal) || errors.Is(context.Cause(ctx), errFirstToken) {
			status = http.StatusGatewayTimeout
		}
		h.log.Warn("upstream request failed", "err", err)
		writeError(w, status, res.ErrorReason)
	}
	return res
}

// finish classifies the attempt: client cancellation wins, then timeouts, then the given failure.
// An empty failure means success.
func (h *Handler) finish(ctx context.Context, r *http.Request, tap *usageTap, failure string) store.AttemptResult {
	res := tap.result
	res.FinishedAt = h.now()
	cause := context.Cause(ctx)
	switch {
	case r.Context().Err() != nil:
		res.Outcome, res.ErrorReason = store.OutcomeCancelled, "client disconnected"
	case errors.Is(cause, errTotal), errors.Is(cause, errFirstToken), errors.Is(cause, errIdle):
		res.Outcome, res.ErrorReason = store.OutcomeError, cause.Error()
	case failure != "":
		res.Outcome, res.ErrorReason = store.OutcomeError, failure
	default:
		res.Outcome = store.OutcomeSuccess
	}
	return res
}

// relayStream copies SSE lines to the client as they arrive, flushing at event boundaries.
// Bytes are forwarded unmodified so tool calls and reasoning state stay intact.
func (h *Handler) relayStream(ctx context.Context, w http.ResponseWriter, r *http.Request, resp *http.Response, tap *usageTap, act *activity) store.AttemptResult {
	w.Header().Set("Content-Type", resp.Header.Get("Content-Type"))
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(resp.StatusCode)
	flusher, _ := w.(http.Flusher)
	flush := func() {
		if flusher != nil {
			flusher.Flush()
		}
	}
	flush()

	br := bufio.NewReaderSize(resp.Body, 64<<10)
	clientGone := false
	for {
		line, err := br.ReadBytes('\n')
		if len(line) > 0 {
			if tap.line(line, h.now()) {
				act.touch()
			}
			if _, werr := w.Write(line); werr != nil {
				clientGone = true
				break
			}
			if len(bytes.TrimSpace(line)) == 0 {
				flush()
			}
		}
		if err != nil {
			break
		}
	}
	flush()
	if clientGone {
		// A failed write is a disconnect even before the server notices it and cancels the request context.
		res := h.finish(ctx, r, tap, "")
		res.Outcome, res.ErrorReason = store.OutcomeCancelled, "client disconnected"
		return res
	}
	failure := ""
	if !tap.done {
		failure = "upstream stream ended without [DONE]"
	}
	return h.finish(ctx, r, tap, failure)
}

// relayBody copies a non-streaming JSON response and extracts its usage.
func (h *Handler) relayBody(ctx context.Context, w http.ResponseWriter, r *http.Request, resp *http.Response, tap *usageTap) store.AttemptResult {
	body, err := io.ReadAll(io.LimitReader(resp.Body, h.maxResponse+1))
	failure := ""
	switch {
	case err != nil:
		failure = "upstream body read failed"
	case int64(len(body)) > h.maxResponse:
		failure = "upstream body is too large"
	}
	if failure != "" {
		res := h.finish(ctx, r, tap, failure)
		writeError(w, http.StatusBadGateway, res.ErrorReason)
		return res
	}
	tap.observe(body)
	w.Header().Set("Content-Type", resp.Header.Get("Content-Type"))
	w.WriteHeader(resp.StatusCode)
	_, _ = w.Write(body)
	return h.finish(ctx, r, tap, "")
}

// relayError passes the upstream error status and body through; error bodies carry no completion content.
func relayError(w http.ResponseWriter, resp *http.Response) {
	body, _ := io.ReadAll(io.LimitReader(resp.Body, maxErrorBody))
	if ct := resp.Header.Get("Content-Type"); ct != "" {
		w.Header().Set("Content-Type", ct)
	}
	w.WriteHeader(resp.StatusCode)
	_, _ = w.Write(body)
}

// writeError emits an OpenAI-style error body so clients surface the message instead of retrying blindly.
func writeError(w http.ResponseWriter, status int, message string) {
	writeCodedError(w, status, message, "")
}

// writeCodedError adds a machine-readable code such as budget_exhausted; without one the code is the status.
func writeCodedError(w http.ResponseWriter, status int, message, code string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	var c any = status
	if code != "" {
		c = code
	}
	_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]any{"message": message, "type": "wrench_error", "code": c}})
}

func newID() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// activity cancels a stream that shows no data events: first within the first-token timeout,
// then within the idle timeout after each event.
type activity struct {
	timer *time.Timer
	idle  time.Duration
	cause atomic.Pointer[error]
}

func newActivity(cancel context.CancelCauseFunc, first, idle time.Duration) *activity {
	a := &activity{idle: idle}
	a.cause.Store(&errFirstToken)
	a.timer = time.AfterFunc(first, func() { cancel(*a.cause.Load()) })
	return a
}

func (a *activity) touch() {
	if a == nil {
		return
	}
	a.cause.Store(&errIdle)
	a.timer.Reset(a.idle)
}

func (a *activity) stop() { a.timer.Stop() }

// keyOf is the device key that authenticated the request, nil for other credentials.
func keyOf(id auth.Identity) *int64 {
	if id.KeyID == 0 {
		return nil
	}
	return &id.KeyID
}

// maxAgentLen bounds the stored client agent.
const maxAgentLen = 64

// agentFromUserAgent returns the leading product token of a User-Agent header ("opencode/2.3.4"), cut to
// maxAgentLen runes. It is client-controlled display text, never an identity.
func agentFromUserAgent(ua string) string {
	token, _, _ := strings.Cut(ua, " ")
	if r := []rune(token); len(r) > maxAgentLen {
		token = string(r[:maxAgentLen])
	}
	return strings.ToValidUTF8(token, "")
}
