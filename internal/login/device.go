package login

import (
	"context"
	"crypto/rand"
	"errors"
	"math/big"
	"strings"
	"time"

	"github.com/ordo-systems/wrench/internal/auth"
	"github.com/ordo-systems/wrench/internal/config"
	"github.com/ordo-systems/wrench/internal/events"
	"github.com/ordo-systems/wrench/internal/store"
)

// Device login (RFC 8628) as the OpenCode Console contract uses it: opencode asks for a code pair, the user approves
// the short code on the dashboard's /device page, and opencode polls until it receives a device key and a refresh
// token.
const (
	// ClientID is the only client allowed to start a device login: opencode's CLI.
	ClientID        = "opencode-cli"
	DeviceCodeGrant = "urn:ietf:params:oauth:grant-type:device_code"
	RefreshGrant    = "refresh_token"
	// OrgID is the single organization the Console contract reports.
	OrgID = "wrench"

	deviceCodeTTL = 10 * time.Minute
	devicePoll    = 5 * time.Second
	deviceKeyName = "opencode"
	// maxLiveDeviceCodes bounds unapproved logins; anyone can start one, so a flood gets 503, not a growing table.
	maxLiveDeviceCodes = 1000
	userCodeLength     = 8
)

// userCodeAlphabet has no vowels, so a code never spells a word, and no look-alike digits.
const userCodeAlphabet = "BCDFGHJKLMNPQRSTVWXZ"

// DeviceCodeResponse starts a device login.
type DeviceCodeResponse struct {
	DeviceCode              string
	UserCode                string
	VerificationURIComplete string
	ExpiresIn               int
	Interval                int
}

// DeviceTokenRequest is one token poll or refresh.
type DeviceTokenRequest struct {
	GrantType    string
	ClientID     string
	DeviceCode   string
	RefreshToken string
}

// DeviceTokenResponse carries a new device key (access token) and its refresh token.
type DeviceTokenResponse struct {
	AccessToken  string
	RefreshToken string
	ExpiresIn    int
	OrgID        string
}

// DeviceTokenError is an RFC 8628 / OAuth error code such as authorization_pending.
type DeviceTokenError struct{ Code string }

func (e *DeviceTokenError) Error() string { return e.Code }

func refuse(code string) error { return &DeviceTokenError{Code: code} }

// UserCodeHash hashes a user code as the user typed it: case, dashes and spaces do not matter.
func UserCodeHash(code string) []byte {
	return auth.Hash(strings.NewReplacer("-", "", " ", "").Replace(strings.ToUpper(code)))
}

// DeviceCode starts a device login for opencode.
func (h *Handler) DeviceCode(ctx context.Context, clientID string) (DeviceCodeResponse, error) {
	if clientID != ClientID {
		return DeviceCodeResponse{}, refuse("invalid_client")
	}
	userCode, err := newUserCode()
	if err != nil {
		return DeviceCodeResponse{}, err
	}
	deviceCode, deviceHash := auth.NewToken("")
	if err := h.store.CreateDeviceCode(ctx, deviceHash, UserCodeHash(userCode), h.now().Add(deviceCodeTTL),
		maxLiveDeviceCodes); err != nil {
		return DeviceCodeResponse{}, err
	}
	return DeviceCodeResponse{DeviceCode: deviceCode, UserCode: userCode,
		VerificationURIComplete: h.publicURL + "/device?code=" + userCode,
		ExpiresIn:               int(deviceCodeTTL.Seconds()), Interval: int(devicePoll.Seconds())}, nil
}

// DeviceToken answers a poll of a device code or a refresh. Refusals are *DeviceTokenError.
func (h *Handler) DeviceToken(ctx context.Context, req DeviceTokenRequest) (DeviceTokenResponse, error) {
	if req.ClientID != ClientID {
		return DeviceTokenResponse{}, refuse("invalid_client")
	}
	switch req.GrantType {
	case DeviceCodeGrant:
		return h.pollDevice(ctx, req.DeviceCode)
	case RefreshGrant:
		return h.refresh(ctx, req.RefreshToken)
	}
	return DeviceTokenResponse{}, refuse("unsupported_grant_type")
}

func (h *Handler) pollDevice(ctx context.Context, deviceCode string) (DeviceTokenResponse, error) {
	now := h.now()
	c, err := h.store.PollDeviceCode(ctx, auth.Hash(deviceCode), now, devicePoll)
	switch {
	case errors.Is(err, store.ErrNotFound):
		return DeviceTokenResponse{}, refuse("expired_token")
	case errors.Is(err, store.ErrSlowDown):
		return DeviceTokenResponse{}, refuse("slow_down")
	case err != nil:
		return DeviceTokenResponse{}, err
	}
	switch c.Status {
	case store.DevicePending:
		return DeviceTokenResponse{}, refuse("authorization_pending")
	case store.DeviceDenied:
		return DeviceTokenResponse{}, refuse("access_denied")
	}
	user, err := h.store.UserByID(ctx, c.UserID)
	if err != nil {
		return DeviceTokenResponse{}, err
	}
	if !h.allowed(ctx, user) {
		return DeviceTokenResponse{}, refuse("access_denied")
	}
	key, keyHash := auth.NewToken(auth.KeyPrefix)
	refresh, refreshHash := auth.NewToken("")
	issued, err := h.store.IssueDeviceKey(ctx, user.ID, deviceKeyName+"-"+rand.Text(), deviceKeyName, keyHash, refreshHash,
		now, now.Add(h.keyTTL))
	if err != nil {
		return DeviceTokenResponse{}, err
	}
	h.publishKeys(user.Email)
	h.log.Info("device key issued", "email", user.Email, "device", issued.DeviceID, "key_id", issued.ID)
	return h.tokens(key, refresh), nil
}

func (h *Handler) refresh(ctx context.Context, refreshToken string) (DeviceTokenResponse, error) {
	now := h.now()
	key, keyHash := auth.NewToken(auth.KeyPrefix)
	refresh, refreshHash := auth.NewToken("")
	issued, user, err := h.store.RefreshClientKey(ctx, auth.Hash(refreshToken), keyHash, refreshHash, now, now.Add(h.keyTTL))
	if errors.Is(err, store.ErrNotFound) {
		return DeviceTokenResponse{}, refuse("invalid_grant")
	}
	if err != nil {
		return DeviceTokenResponse{}, err
	}
	if !h.allowed(ctx, user) {
		// The refresh already rotated the key; take the new one back before refusing.
		if _, err := h.store.RevokeClientKey(ctx, issued.ID, user.Email, now); err != nil {
			return DeviceTokenResponse{}, err
		}
		return DeviceTokenResponse{}, refuse("invalid_grant")
	}
	h.publishKeys(user.Email)
	return h.tokens(key, refresh), nil
}

// publishKeys tells dashboards that the user's key list changed.
func (h *Handler) publishKeys(email string) {
	if h.events != nil {
		h.events.Publish(events.Keys, email)
	}
}

// allowed is true for an allowlisted user whose GitLab account is not blocked.
func (h *Handler) allowed(ctx context.Context, u store.User) bool {
	_, ok := config.For(ctx, h.cfg).User(u.Email)
	return ok && u.Status != store.UserBlocked
}

func (h *Handler) tokens(key, refresh string) DeviceTokenResponse {
	return DeviceTokenResponse{AccessToken: key, RefreshToken: refresh, ExpiresIn: int(h.keyTTL.Seconds()), OrgID: OrgID}
}

// newUserCode returns XXXX-XXXX from userCodeAlphabet.
func newUserCode() (string, error) {
	var b strings.Builder
	for i := range userCodeLength {
		if i == userCodeLength/2 {
			b.WriteByte('-')
		}
		n, err := rand.Int(rand.Reader, big.NewInt(int64(len(userCodeAlphabet))))
		if err != nil {
			return "", err
		}
		b.WriteByte(userCodeAlphabet[n.Int64()])
	}
	return b.String(), nil
}

// safeReturn keeps a same-origin path for the web sign-in to return to; anything else returns to the dashboard root.
func safeReturn(p string) string {
	if !strings.HasPrefix(p, "/") || strings.HasPrefix(p, "//") || strings.Contains(p, `\`) {
		return "/"
	}
	// Browsers strip tabs and newlines from a Location, so "/\t/host" would become "//host".
	if strings.ContainsFunc(p, func(r rune) bool { return r < 0x20 || r == 0x7f }) {
		return "/"
	}
	return p
}
