// Package buildinfo holds build-time version metadata, stamped at link time via
// -ldflags "-X searchy/internal/buildinfo.Version=...". Defaults
// keep a plain `go build`/`go run` identifiable, while the current product
// version is the current pre-release line. Mirrors the branchy bot's approach.
package buildinfo

var (
	Version = "0.1.0-beta.1"
	Commit  = "none"
	Date    = "unknown"
)
