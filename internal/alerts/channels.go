package alerts

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"time"

	"github.com/ordo-systems/wrench/internal/money"
)

// Webhook posts {"text": ...}, the payload Slack and Mattermost incoming webhooks accept.
type Webhook struct {
	url    string
	client *http.Client
}

// NewWebhook returns a notifier for the URL; the URL may embed a token and is never logged.
func NewWebhook(u string) *Webhook {
	return &Webhook{url: u, client: &http.Client{Timeout: 10 * time.Second}}
}

func (w *Webhook) Notify(ctx context.Context, text string) error {
	body, _ := json.Marshal(map[string]string{"text": text})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, w.url, bytes.NewReader(body))
	if err != nil {
		return errors.New("alerts: invalid webhook URL")
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := w.client.Do(req)
	if err != nil {
		// The error text contains the URL; report only that delivery failed.
		return errors.New("alerts: webhook unreachable")
	}
	resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		return fmt.Errorf("alerts: webhook status %d", resp.StatusCode)
	}
	return nil
}

// LogNotifier writes alerts to the log when no webhook is configured; incidents still reach the dashboard.
type LogNotifier struct{ Log *slog.Logger }

func (l LogNotifier) Notify(_ context.Context, text string) error {
	l.Log.Warn("alert", "text", text)
	return nil
}

// OpenRouterKey checks Wrench's key against OpenRouter's <base>/key, which accepts a regular API key (/credits
// needs a management key). A 2xx answer proves both the network path and the key.
type OpenRouterKey struct {
	endpoint *url.URL
	key      string
	client   *http.Client
}

func NewOpenRouterKey(base *url.URL, key string) *OpenRouterKey {
	// Redirects are refused so the key is never replayed to another URL.
	return &OpenRouterKey{endpoint: base.JoinPath("key"), key: key, client: &http.Client{Timeout: 15 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}
}

// Check returns nil when OpenRouter accepts the key.
func (k *OpenRouterKey) Check(ctx context.Context) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, k.endpoint.String(), nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+k.key)
	resp, err := k.client.Do(req)
	if err != nil {
		return fmt.Errorf("openrouter key: %w", err)
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return fmt.Errorf("openrouter key: status %d", resp.StatusCode)
	}
	return nil
}

// OpenRouterBalance reads the remaining credit limit of Wrench's key from OpenRouter's <base>/key. The account
// balance (/credits) needs a management key, so the alert watches the key's limit instead; a key without a limit
// has no balance to report.
type OpenRouterBalance struct {
	endpoint *url.URL
	key      string
	client   *http.Client
}

func NewOpenRouterBalance(base *url.URL, key string) *OpenRouterBalance {
	// Redirects are refused so the key is never replayed to another URL.
	return &OpenRouterBalance{endpoint: base.JoinPath("key"), key: key, client: &http.Client{Timeout: 15 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}
}

func (b *OpenRouterBalance) Balance(ctx context.Context) (money.Amount, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, b.endpoint.String(), nil)
	if err != nil {
		return 0, err
	}
	req.Header.Set("Authorization", "Bearer "+b.key)
	resp, err := b.client.Do(req)
	if err != nil {
		return 0, fmt.Errorf("alerts: key: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return 0, fmt.Errorf("alerts: key status %d", resp.StatusCode)
	}
	var body struct {
		Data struct {
			LimitRemaining *json.Number `json:"limit_remaining"`
		} `json:"data"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&body); err != nil {
		return 0, fmt.Errorf("alerts: key body: %w", err)
	}
	if body.Data.LimitRemaining == nil {
		return 0, errors.New("alerts: the OpenRouter key has no credit limit; set one in OpenRouter to enable low_balance")
	}
	return money.ParseCost(*body.Data.LimitRemaining)
}
