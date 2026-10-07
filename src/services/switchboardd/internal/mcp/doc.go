// Package mcp is the daemon's MCP gateway manager (feature 008). The host sandbox
// runtime keeps MCP server registrations per host and manages them through its
// CLI (`sbx mcp …`); this package drives that CLI with the documented argv only
// (contracts/sbx-mcp-cli.md — every argv is pinned by a test, since `sbx` is not
// installed in the development environment), reads the host's list back on every
// query (the host is the truth), and owns the one thing the runtime has no notion
// of: switchboard's per-daemon attach-by-default marks and default attach mode,
// persisted in the registry's `mcp` bucket.
//
// Authorization is always a separate step from registration (research R2):
// registrations are made with `--skip-auth`, and `sbx mcp auth` runs as a
// bounded, killable child whose authorization URL is streamed to the client.
package mcp
