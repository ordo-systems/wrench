package api

import (
	"maps"
	"slices"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// document is the part of the OpenAPI document the contract tests rely on.
type document struct {
	OpenAPI    string                          `yaml:"openapi"`
	Paths      map[string]map[string]operation `yaml:"paths"`
	Components struct {
		Responses map[string]struct {
			Content map[string]struct {
				Schema struct {
					Ref string `yaml:"$ref"`
				} `yaml:"schema"`
			} `yaml:"content"`
		} `yaml:"responses"`
	} `yaml:"components"`
}

type operation struct {
	OperationID string                `yaml:"operationId"`
	Security    []map[string][]string `yaml:"security"`
	Admin       bool                  `yaml:"x-wrench-admin"`
	Responses   map[string]struct {
		Ref string `yaml:"$ref"`
	} `yaml:"responses"`
}

func parse(t *testing.T) document {
	t.Helper()
	var d document
	if err := yaml.Unmarshal(Document, &d); err != nil {
		t.Fatalf("document does not parse: %v", err)
	}
	return d
}

func TestDocumentOperations(t *testing.T) {
	d := parse(t)
	if d.OpenAPI != "3.0.3" {
		t.Errorf("openapi = %q, want 3.0.3", d.OpenAPI)
	}
	want := []string{
		"getHealthz", "getReadyz", "startDeviceLogin", "deviceToken", "decideDevice", "getConsoleUser", "listConsoleOrgs", "getConsoleConfig", "listConsoleInstructions", "listConsoleSkills", "listConsoleBudgets", "getMe", "listModels", "listRequests", "listSessions",
		"getUsage", "listKeys", "revokeKey", "revokeKeys", "revokeUser",
		"getStats",
		"listUsers", "getSystem", "listAlerts", "getBudget", "getStatus", "streamEvents",
	}
	ids := map[string]bool{}
	public := map[string]bool{}
	admin := map[string]bool{}
	for path, methods := range d.Paths {
		for method, op := range methods {
			if op.OperationID == "" {
				t.Errorf("%s %s has no operationId", method, path)
			}
			ids[op.OperationID] = true
			if len(op.Security) == 0 {
				public[op.OperationID] = true
			}
			if op.Admin {
				admin[op.OperationID] = true
			}
			for code, r := range op.Responses {
				// Readiness reports which dependency failed in the same body as success; the device login answers 400
				// with the RFC 8628 error object opencode reads.
				device := (op.OperationID == "startDeviceLogin" || op.OperationID == "deviceToken") && code == "400"
				if code[0] == '2' || code[0] == '3' || (op.OperationID == "getReadyz" && code == "503") || device {
					continue
				}
				name := strings.TrimPrefix(r.Ref, "#/components/responses/")
				if r.Ref == "" || !errorBodied(d, name) {
					t.Errorf("%s %s response %s is not an Error response", method, path, code)
				}
			}
		}
	}
	if got := slices.Sorted(maps.Keys(ids)); !slices.Equal(got, slices.Sorted(slices.Values(want))) {
		t.Errorf("operations = %v, want %v", got, slices.Sorted(slices.Values(want)))
	}
	if got := slices.Sorted(maps.Keys(admin)); !slices.Equal(got, []string{"getSystem", "listAlerts", "listUsers", "revokeUser"}) {
		t.Errorf("administrator operations = %v", got)
	}
	if got := slices.Sorted(maps.Keys(public)); !slices.Equal(got, []string{"deviceToken", "getHealthz", "getReadyz", "startDeviceLogin"}) {
		t.Errorf("public operations = %v", got)
	}
}

func errorBodied(d document, name string) bool {
	r, ok := d.Components.Responses[name]
	return ok && r.Content["application/json"].Schema.Ref == "#/components/schemas/Error"
}
