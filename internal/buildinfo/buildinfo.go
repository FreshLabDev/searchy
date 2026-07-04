// Package buildinfo holds build-time version metadata, stamped at link time via
// -ldflags "-X searchy/internal/buildinfo.Version=...". Defaults
// keep a plain `go build`/`go run` identifiable, while the current product
// version is 0.1.0 (no git tag cut yet). Mirrors the branchy bot's approach.
package buildinfo

var (
	Version = "0.1.0"
	Commit  = "none"
	Date    = "unknown"
)
