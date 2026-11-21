package httpapi

import (
	"log/slog"
	"net/http"

	"github.com/ordo-systems/wrench/api"
)

// server implements the generated strict interface: each method takes a parsed request and returns one of the
// responses the document lists for the operation.
type server struct {
	deps Deps
}

var _ api.StrictServerInterface = (*server)(nil)

// strictOptions writes parse failures as 400 and handler failures as 500, both in the API's error format.
var strictOptions = api.StrictHTTPServerOptions{RequestErrorHandlerFunc: badRequest, ResponseErrorHandlerFunc: internalError}

func errorBody(message string) api.Error {
	return api.Error{Error: api.ErrorDetail{Message: message}}
}

// internalError reports an error a handler returned instead of a documented response.
func internalError(w http.ResponseWriter, r *http.Request, err error) {
	slog.ErrorContext(r.Context(), "API handler failed", "path", r.URL.Path, "err", err)
	writeError(w, http.StatusInternalServerError, "internal error")
}

// badRequest reports a parameter or body the generated code could not parse, in the API's error format.
func badRequest(w http.ResponseWriter, _ *http.Request, err error) {
	writeError(w, http.StatusBadRequest, err.Error())
}
