// Package version holds build metadata injected with -ldflags -X at build time.
package version

// Version is the git describe output of the build; Commit is its short commit hash.
var (
	Version = "dev"
	Commit  = "unknown"
)

// String formats the build as "<version> (<commit>)" for logs, the CLI and the system API.
func String() string {
	return Version + " (" + Commit + ")"
}
