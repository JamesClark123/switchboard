// Package mcprules holds every rule for an MCP server definition that both the
// daemon and the TUI apply (SWI-21 FR-420, research R1): header syntax,
// `${placeholder}` names and their secret names, the credential-header rule
// with its one-step conversion and redaction, OAuth scopes, the OAuth client
// ID and authorization server, the URL/argument credential warning, and the
// normalised comparison of two definitions.
//
// It imports only the standard library and the same module's gen package, so
// the libs dependency rule holds. Error messages never quote a credential
// header's value: they name the header and the position of the literal, or
// quote Redact(value).
package mcprules
