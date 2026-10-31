package mcp

import "testing"

// The defaults are documentation-derived (contracts/sbx-cli-delta.md); a change
// is a Scenario 0 outcome and must update the contract too.
func TestHostFlagDefaults(t *testing.T) {
	if inspectReportsHeaders || authStatusReportsScopes || !inspectReturnsTemplates ||
		secretListShowsIdentity || !killedAddMayComplete || mcpLoadWorksInExclusive || LoadWorksInExclusive() {
		t.Fatal("host flag defaults changed without a Scenario 0 record")
	}
}
