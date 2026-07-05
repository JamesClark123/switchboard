// Package e2e holds the end-to-end suite for the switchboardd daemon, driving the
// full sandbox lifecycle against a real sandbox runtime (Docker + sbx). The tests
// are guarded by the `e2e` build tag and additionally skip at runtime when sbx /
// Docker are unavailable, so they run only where a real runtime is present
// (CI with Docker, or a developer's host).
package e2e
