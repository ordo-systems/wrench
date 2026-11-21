package httpapi

import (
	"context"
	"errors"

	"github.com/ordo-systems/wrench/api"
	"github.com/ordo-systems/wrench/internal/auth"
	"github.com/ordo-systems/wrench/internal/login"
	"github.com/ordo-systems/wrench/internal/store"
)

// StartDeviceLogin starts opencode's device login. Without a login handler the server has no sign-in at all.
func (s *server) StartDeviceLogin(ctx context.Context, req api.StartDeviceLoginRequestObject) (api.StartDeviceLoginResponseObject, error) {
	if s.deps.Login == nil {
		return api.StartDeviceLogin503JSONResponse{UnavailableJSONResponse: api.UnavailableJSONResponse(errorBody("sign-in is not configured"))}, nil
	}
	c, err := s.deps.Login.DeviceCode(ctx, req.Body.ClientId)
	var refused *login.DeviceTokenError
	switch {
	case errors.As(err, &refused):
		return api.StartDeviceLogin400JSONResponse{Error: refused.Code}, nil
	case err != nil:
		return api.StartDeviceLogin503JSONResponse{UnavailableJSONResponse: unavailableBody(ctx, err)}, nil
	}
	return api.StartDeviceLogin200JSONResponse{
		Body: api.DeviceCode{DeviceCode: c.DeviceCode, UserCode: c.UserCode, VerificationUriComplete: c.VerificationURIComplete,
			ExpiresIn: c.ExpiresIn, Interval: c.Interval},
		Headers: api.StartDeviceLogin200ResponseHeaders{CacheControl: "no-store"},
	}, nil
}

// DecideDevice records the dashboard user's approval or denial of a device login. Only a dashboard session may
// decide, so a leaked device key cannot approve new devices; the guard has already checked the Origin.
func (s *server) DecideDevice(ctx context.Context, req api.DecideDeviceRequestObject) (api.DecideDeviceResponseObject, error) {
	id, _ := auth.FromContext(ctx)
	if id.Credential != auth.CredentialSession {
		return api.DecideDevice403JSONResponse{ForbiddenJSONResponse: forbiddenBody("approve devices from the dashboard")}, nil
	}
	if !id.StatusFresh {
		return api.DecideDevice403JSONResponse{ForbiddenJSONResponse: forbiddenBody("your GitLab account status is not confirmed; sign in again")}, nil
	}
	if !req.Body.Decision.Valid() {
		return api.DecideDevice400JSONResponse{BadRequestJSONResponse: badRequestBody("decision must be approve or deny")}, nil
	}
	err := s.deps.Store.DecideDeviceCode(ctx, login.UserCodeHash(req.Body.UserCode), id.Email,
		req.Body.Decision == api.DeviceDecisionDecisionApprove, s.deps.now())
	switch {
	case errors.Is(err, store.ErrNotFound):
		return api.DecideDevice404JSONResponse{NotFoundJSONResponse: notFoundBody("no pending device login has this code; start again in opencode")}, nil
	case err != nil:
		return api.DecideDevice503JSONResponse{UnavailableJSONResponse: unavailableBody(ctx, err)}, nil
	}
	return api.DecideDevice204Response{}, nil
}

// DeviceToken answers a device-login poll or a refresh with a device key, or an RFC 8628 error object.
func (s *server) DeviceToken(ctx context.Context, req api.DeviceTokenRequestObject) (api.DeviceTokenResponseObject, error) {
	if s.deps.Login == nil {
		return api.DeviceToken503JSONResponse{UnavailableJSONResponse: api.UnavailableJSONResponse(errorBody("sign-in is not configured"))}, nil
	}
	out, err := s.deps.Login.DeviceToken(ctx, login.DeviceTokenRequest{GrantType: req.Body.GrantType, ClientID: req.Body.ClientId,
		DeviceCode: deref(req.Body.DeviceCode), RefreshToken: deref(req.Body.RefreshToken)})
	var refused *login.DeviceTokenError
	switch {
	case errors.As(err, &refused):
		return api.DeviceToken400JSONResponse{Error: refused.Code}, nil
	case err != nil:
		return api.DeviceToken503JSONResponse{UnavailableJSONResponse: unavailableBody(ctx, err)}, nil
	}
	return api.DeviceToken200JSONResponse{
		Body: api.DeviceToken{AccessToken: out.AccessToken, RefreshToken: out.RefreshToken, ExpiresIn: out.ExpiresIn,
			OrgId: out.OrgID},
		Headers: api.DeviceToken200ResponseHeaders{CacheControl: "no-store"},
	}, nil
}
