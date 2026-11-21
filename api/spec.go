// Package api is the Wrench HTTP API contract: the OpenAPI document and the server interface and models generated
// from it. Only this file is written by hand; change openapi.yaml and run make generate.
package api

import _ "embed"

// Document is openapi.yaml, the source of truth for every documented route. The server reads it to decide which
// operations require a credential.
//
//go:embed openapi.yaml
var Document []byte
