package httpapi

import (
	"fmt"
	"net/http"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/ordo-systems/wrench/internal/auth"
	"github.com/ordo-systems/wrench/internal/config"
)

// maxAPIBody bounds every request body of a documented operation.
const maxAPIBody = 4 << 10

// apiOperation is one operation of the OpenAPI document as far as routing and authorization need it.
type apiOperation struct {
	Method string
	Path   string
	ID     string
	// Secured operations declare a security requirement; the document has no global one.
	Secured bool
	// Admin operations (x-wrench-admin) are refused to non-administrators.
	Admin bool
}

// apiOperations lists the operations of an OpenAPI document.
func apiOperations(doc []byte) ([]apiOperation, error) {
	var d struct {
		Paths map[string]map[string]struct {
			OperationID string                `yaml:"operationId"`
			Security    []map[string][]string `yaml:"security"`
			Admin       bool                  `yaml:"x-wrench-admin"`
		} `yaml:"paths"`
	}
	if err := yaml.Unmarshal(doc, &d); err != nil {
		return nil, fmt.Errorf("parse the OpenAPI document: %w", err)
	}
	var ops []apiOperation
	for path, methods := range d.Paths {
		for method, op := range methods {
			ops = append(ops, apiOperation{Method: strings.ToUpper(method), Path: path, ID: op.OperationID,
				Secured: len(op.Security) > 0, Admin: op.Admin})
		}
	}
	return ops, nil
}

// contract finds the documented operation of a request with the same pattern matching the router uses, before
// the generated router parses parameters and body.
type contract struct {
	mux *http.ServeMux
	ops map[string]apiOperation // by mux pattern
}

func newContract(ops []apiOperation) contract {
	c := contract{mux: http.NewServeMux(), ops: map[string]apiOperation{}}
	for _, op := range ops {
		pattern := op.Method + " " + op.Path
		c.mux.Handle(pattern, http.NotFoundHandler())
		c.ops[pattern] = op
	}
	return c
}

func (c contract) lookup(r *http.Request) (apiOperation, bool) {
	_, pattern := c.mux.Handler(r)
	op, ok := c.ops[pattern]
	return op, ok
}

// guard enforces what the document declares before anything else runs, so an unauthenticated or unauthorized
// request gets 401 or 403 even when its parameters or body are malformed: secured operations need a credential,
// secured mutations authenticated by the session cookie must come from the public origin, and administrator
// operations need the admin role. Request bodies are bounded for every operation.
func guard(deps Deps, c contract, next http.Handler) http.Handler {
	user := auth.Middleware(deps.Auth, sameOrigin(deps.PublicURL, next))
	admin := auth.Middleware(deps.Auth, sameOrigin(deps.PublicURL, adminOnly(next)))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.Body = http.MaxBytesReader(w, r.Body, maxAPIBody)
		op, ok := c.lookup(r)
		switch {
		case ok && op.Admin:
			admin.ServeHTTP(w, r)
		case ok && op.Secured:
			user.ServeHTTP(w, r)
		default:
			next.ServeHTTP(w, r)
		}
	})
}

func adminOnly(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if id, _ := auth.FromContext(r.Context()); id.Role != config.RoleAdmin {
			writeError(w, http.StatusForbidden, "administrator role required")
			return
		}
		next.ServeHTTP(w, r)
	})
}
