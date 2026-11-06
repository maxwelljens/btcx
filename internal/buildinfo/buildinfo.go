// Package buildinfo exposes build-time metadata embedded via -ldflags.
//
// Example:
//
//	go build -ldflags "-X btcx/internal/buildinfo.version=v1.0.0 \
//	  -X btcx/internal/buildinfo.commit=$(git rev-parse --short HEAD) \
//	  -X btcx/internal/buildinfo.date=$(date -u +%Y-%m-%dT%H:%M:%SZ)" ./cmd/btcx
package buildinfo

import "fmt"

// Set at link time. Defaults mark a plain `go build` from source.
var (
	version = "(devel)"
	commit  = "unknown"
	date    = "unknown"
)

// Version returns the release version, e.g. "v1.0.0" or "(devel)".
func Version() string { return version }

// Commit returns the short VCS revision the binary was built from.
func Commit() string { return commit }

// Date returns the UTC build timestamp in RFC 3339 form.
func Date() string { return date }

// String renders "version (commit) built date".
func String() string {
	return fmt.Sprintf("%s (%s) built %s", version, commit, date)
}
