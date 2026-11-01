package mcp

// Real-host behaviour flags (SWI-21 research R16, contracts/sbx-cli-delta.md).
// Every one is documentation-derived until quickstart Scenario 0 runs its check
// on a real host; they are package variables only so tests can flip them.
var (
	// inspectReportsHeaders: an empty header section from `sbx mcp inspect
	// --json` is a positive "no headers". Scenario 0: register a server with no
	// headers and inspect it. false ⇒ an empty section is *not reported*.
	inspectReportsHeaders = false
	// authStatusReportsScopes: an empty scope list from `sbx mcp auth status
	// --json` is a positive "no default scopes". Scenario 0: register with and
	// without --scope and read auth status. false ⇒ *not reported*.
	authStatusReportsScopes = false
	// inspectReturnsTemplates: inspect shows `${…}` header templates, not the
	// resolved values (FR-423). Scenario 0: register `Authorization: Bearer
	// ${k}` with k set and inspect it.
	inspectReturnsTemplates = true
	// secretListShowsIdentity: `sbx secret ls` lists the runtime's
	// `mcp:<s>:client_secret:identity` record (FR-415). Scenario 0: authorize a
	// pre-registered client, then list secrets.
	secretListShowsIdentity = false
	// killedAddMayComplete: a killed or failed `sbx mcp add` may still finish
	// on the host, so its state is unknown (FR-406). Scenario 0: kill an add
	// mid-run, then list.
	killedAddMayComplete = true
	// mcpLoadWorksInExclusive: `sbx mcp load` attaches a server to a sandbox
	// launched with --static-mcp (FR-425 note). Scenario 0: load into such a
	// sandbox.
	mcpLoadWorksInExclusive = false
)

// LoadWorksInExclusive exposes mcpLoadWorksInExclusive to the sandbox overlay's
// wording (FR-425).
func LoadWorksInExclusive() bool { return mcpLoadWorksInExclusive }
