// Package wrenchapi is the Wrench API client generated from api/openapi.yaml. Only this file
// is written by hand; run make generate after the document changes.
package wrenchapi

import (
	"context"
	"net/http"
)

// Bearer authenticates every request with a device key.
func Bearer(token string) ClientOption {
	return WithRequestEditorFn(func(_ context.Context, req *http.Request) error {
		req.Header.Set("Authorization", "Bearer "+token)
		return nil
	})
}
