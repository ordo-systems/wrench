package httpapi

import (
	"context"
	"errors"
	"net/http"

	"github.com/ordo-systems/wrench/api"
	"github.com/ordo-systems/wrench/internal/auth"
	"github.com/ordo-systems/wrench/internal/config"
	"github.com/ordo-systems/wrench/internal/events"
	"github.com/ordo-systems/wrench/internal/store"
)

// ListKeys returns device key metadata; tokens are never stored, so they cannot leak here.
func (s *server) ListKeys(ctx context.Context, req api.ListKeysRequestObject) (api.ListKeysResponseObject, error) {
	user, err := scopeOf(ctx, req.Params.User)
	if err != nil {
		return api.ListKeys403JSONResponse{ForbiddenJSONResponse: forbiddenBody(err.Error())}, nil
	}
	keys, err := s.deps.Store.ListClientKeys(ctx, user)
	if err != nil {
		return api.ListKeys503JSONResponse{UnavailableJSONResponse: unavailableBody(ctx, err)}, nil
	}
	return api.ListKeys200JSONResponse{Keys: views(keys, clientKeyView)}, nil
}

// publish tells dashboards what changed; servers without an event bus stay silent.
func (s *server) publish(t events.Topic, user string) {
	if s.deps.Events != nil {
		s.deps.Events.Publish(t, user)
	}
}

// RevokeKey revokes one key; non-admins can only reach their own, and others look absent.
func (s *server) RevokeKey(ctx context.Context, req api.RevokeKeyRequestObject) (api.RevokeKeyResponseObject, error) {
	owner := ""
	if caller, _ := auth.FromContext(ctx); caller.Role != config.RoleAdmin {
		owner = caller.Email
	}
	ownerEmail, err := s.deps.Store.RevokeClientKey(ctx, req.Id, owner, s.deps.now())
	switch {
	case errors.Is(err, store.ErrNotFound):
		return api.RevokeKey404JSONResponse{NotFoundJSONResponse: notFoundBody("no such active key")}, nil
	case err != nil:
		return api.RevokeKey503JSONResponse{UnavailableJSONResponse: unavailableBody(ctx, err)}, nil
	}
	s.publish(events.Keys, ownerEmail)
	return api.RevokeKey204Response{}, nil
}

// RevokeKeys revokes all active keys of one user: the caller by default, another user for administrators.
// "Everyone" is refused, so a stray `*` cannot sign out the whole company.
func (s *server) RevokeKeys(ctx context.Context, req api.RevokeKeysRequestObject) (api.RevokeKeysResponseObject, error) {
	user, err := scopeOf(ctx, req.Params.User)
	if err != nil {
		return api.RevokeKeys403JSONResponse{ForbiddenJSONResponse: forbiddenBody(err.Error())}, nil
	}
	if user == "" {
		return api.RevokeKeys400JSONResponse{BadRequestJSONResponse: badRequestBody("user must name one user, not *")}, nil
	}
	n, err := s.deps.Store.RevokeClientKeys(ctx, user, s.deps.now())
	if err != nil {
		return api.RevokeKeys503JSONResponse{UnavailableJSONResponse: unavailableBody(ctx, err)}, nil
	}
	if n > 0 {
		s.publish(events.Keys, user)
	}
	return api.RevokeKeys200JSONResponse{Revoked: n}, nil
}

// RevokeUser is the administrator's emergency stop for one user: every key and session. The role is checked by guard.
func (s *server) RevokeUser(ctx context.Context, req api.RevokeUserRequestObject) (api.RevokeUserResponseObject, error) {
	n, err := s.deps.Store.RevokeUserCredentials(ctx, config.NormalizeEmail(req.Email), s.deps.now())
	switch {
	case errors.Is(err, store.ErrNotFound):
		return api.RevokeUser404JSONResponse{NotFoundJSONResponse: notFoundBody("no such user")}, nil
	case err != nil:
		return api.RevokeUser503JSONResponse{UnavailableJSONResponse: unavailableBody(ctx, err)}, nil
	}
	s.publish(events.Users, "")
	s.publish(events.Keys, config.NormalizeEmail(req.Email))
	return api.RevokeUser200JSONResponse{Revoked: n}, nil
}

// sameOrigin blocks cross-site mutations that ride on the session cookie. Bearer keys are not
// sent automatically by browsers, so they need no Origin; reads change nothing, so they need none either.
func sameOrigin(public string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id, _ := auth.FromContext(r.Context())
		mutation := r.Method != http.MethodGet && r.Method != http.MethodHead
		if mutation && id.Credential == auth.CredentialSession && (public == "" || r.Header.Get("Origin") != public) {
			writeError(w, http.StatusForbidden, "cross-origin request rejected")
			return
		}
		next.ServeHTTP(w, r)
	})
}
