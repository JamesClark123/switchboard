// Package e2e holds the PTY-driven end-to-end suite for the switchboard TUI
// (Rule VI's justified Playwright substitute for a terminal UI). The actual
// tests are guarded by the `e2e` build tag so they run only in CI / on demand
// (`go test -tags e2e ./...`), keeping the fast unit/integration layers free of
// the binary-build + PTY overhead.
package e2e
