# Proto delta: SWI-21 (additive to `src/libs/switchboard-proto/proto/switchboard.proto`)

Every change is additive. Older clients keep working: they never send the new fields, and the old RPCs keep
their behaviour (FR-422). No `NotificationKind` is added.

## RPCs (service `Switchboard`, in the MCP gateway block)

```proto
// SWI-21 (FR-402): the edit form's pre-fill — switchboard's record merged with what
// the runtime reports, credential literals redacted, plus the fingerprint to echo back.
rpc GetMcpServerDefinition(GetMcpServerDefinitionRequest) returns (McpDefinitionView);
// SWI-21 (FR-404): every save from the MCP form (add, edit, secrets-only, Retry).
// Runs to completion on the daemon even if the caller goes away.
rpc SaveMcpServer(SaveMcpServerRequest) returns (McpSaveOutcome);
// SWI-21 (FR-406, FR-426): drop a pending replace.
rpc CancelMcpPendingReplace(CancelMcpPendingReplaceRequest) returns (CancelMcpPendingReplaceResponse);
// SWI-21 (FR-413): remove secrets a save stopped using, under the per-name lock.
rpc RemoveMcpServerSecrets(RemoveMcpServerSecretsRequest) returns (RemoveMcpServerSecretsResponse);
```

## Messages

```proto
message McpServerDefinition {
  McpServerKind kind = 1;
  string url = 2;
  AddMcpServerRequest.LocalCommand command = 3;
  repeated string headers = 4;            // "Name: value"; never a credential literal (FR-423)
  string client_id = 5;
  string oauth_authorization_server = 6;
  repeated string scopes = 7;
  bool rebind_pending = 8;                // FR-415
  repeated string literal_on_host = 9;    // header names held redacted (FR-423)
}

message McpPendingReplace {
  string name = 1;
  bool attach_by_default = 2;
  McpServerDefinition definition = 3;
  McpServerDefinition previous = 4;
  repeated string removed_secrets = 5;
  repeated string removal_failed_secrets = 6;
  bool add_started = 7;
  string error = 8;
  google.protobuf.Timestamp failed_at = 9;
  uint64 revision = 10;
}

// for_add: only the secret status (stored_secrets, secrets_available) for a name that
// need not be registered (add mode, R11).
message GetMcpServerDefinitionRequest { string name = 1; bool for_add = 2; }

message McpFieldFlag {
  enum Kind {
    KIND_UNSPECIFIED = 0;
    CHANGED_ON_HOST = 1;
    NOT_RECOVERABLE = 2;
    LITERAL_ON_HOST = 3;
    RESOLVED_UNCHECKABLE = 4;           // inspect returns resolved values (FR-423 flag)
  }
  string field = 1;                     // "url", "headers", "headers/<Name>", "client_id", "oauth_authorization_server", "scopes", "command"
  Kind kind = 2;
  string host_value = 3;                // redacted where FR-423 applies
  string recorded_value = 4;
}

message McpSecretStatus {
  enum Status { STATUS_UNSPECIFIED = 0; SET = 1; MISSING = 2; REMOVAL_FAILED = 3; }
  string name = 1;                      // full name mcp:<server>:<placeholder|client_secret>
  Status status = 2;
}

message McpDefinitionView {
  string name = 1;
  McpServerDefinition definition = 2;   // merged pre-fill (redacted)
  repeated McpFieldFlag flags = 3;
  repeated string unsupported_settings = 4;   // e.g. "resource", "skip-ssrf-check", "registry image"
  repeated McpSecretStatus secrets = 5;
  bool secrets_available = 6;
  string secrets_unavailable_reason = 7;
  McpAuthState auth_state = 8;
  bool has_record = 9;
  bool attach_by_default = 10;
  McpPendingReplace pending_replace = 11;
  string fingerprint = 12;              // opaque; echo in SaveMcpServerRequest
  // FR-406 Cancel confirmation: removal-failed names still in the global list;
  // unknown when the list cannot be read.
  repeated string pending_removal_failed_listed = 13;
  bool pending_removal_failed_list_unknown = 14;
  // Every global-scope mcp:<name>:* secret name (never values); the form derives
  // set/missing for placeholders typed after it opened (FR-409, FR-411).
  repeated string stored_secrets = 15;
}

message SaveMcpServerRequest {
  string name = 1;
  McpServerDefinition definition = 2;   // rebind_pending / literal_on_host ignored (daemon-owned)
  bool attach_by_default = 3;
  bool is_new = 4;                      // add from the form
  string fingerprint = 5;               // "" only with is_new
  uint64 retry_of_revision = 6;         // Retry of a pending replace
  map<string, string> secret_values = 7;// full secret name -> value; never logged (FR-104)
  bool acknowledge_local_execution = 8; // FR-076/FR-407
  bool confirm_drop_unsupported = 9;    // FR-402
  bool confirm_discard_authorization = 10; // FR-404
}

message McpSaveOutcome {
  enum State { STATE_UNSPECIFIED = 0; REGISTERED = 1; UNCHANGED_REGISTRATION = 2; STILL_REGISTERED = 3; NOT_REGISTERED = 4; }
  enum Identity { IDENTITY_UNSPECIFIED = 0; NOT_TOUCHED = 1; LEFT_IN_PLACE = 2; CLEARED = 3; NOT_CLEARED = 4; UNCONFIRMED = 5; }
  string name = 1;
  State state = 2;
  bool replaced = 3;
  bool add_started = 4;
  repeated string created = 5;
  repeated string overwritten = 6;
  repeated string rolled_back = 7;
  repeated string rollback_failed = 8;
  Identity identity = 9;
  bool rebind_pending = 10;
  repeated string host_errors = 11;
  repeated McpAttachedSandbox attached_sandboxes = 12;
  repeated string unused_secrets = 13;
  McpPendingReplace pending_replace = 14;
  McpServer server = 15;                // decorated row when registered
}

message McpAttachedSandbox {
  string sandbox_id = 1;
  string display_name = 2;
  bool skipped_at_launch = 3;           // FR-425 entry
  bool exclusive = 4;                   // launched with --static-mcp
}

message CancelMcpPendingReplaceRequest { string name = 1; uint64 revision = 2; }
message CancelMcpPendingReplaceResponse { repeated string left_on_host = 1; repeated string unconfirmed = 2; }

message RemoveMcpServerSecretsRequest { string name = 1; repeated string secrets = 2; }
message RemoveMcpServerSecretsResponse { repeated string removed = 1; repeated string failed = 2; bool rebind_pending = 3; repeated string host_errors = 4; }

enum McpSkipState { MCP_SKIP_STATE_UNSPECIFIED = 0; MCP_SKIP_STATE_SKIPPED_BEING_REPLACED = 1; MCP_SKIP_STATE_NOT_ATTACHED_AT_LAUNCH = 2; }
message McpSkippedServer { string name = 1; McpSkipState state = 2; }

message McpSaveRefusal {                // carried as a status detail on FAILED_PRECONDITION
  enum Reason { REASON_UNSPECIFIED = 0; STALE_CHANGED = 1; STALE_RESTARTED = 2; PENDING_ENDED = 3; PENDING_REMOVAL_FAILED = 4; AUTH_IN_PROGRESS = 5; BEING_REPLACED = 6; PENDING_EXISTS = 7; GONE = 8; }
  Reason reason = 1;
  string message = 2;
}
```

## Field additions on existing messages

| Message | Field | Number |
|---------|-------|--------|
| `AddMcpServerRequest` | `repeated string scopes` | 9 |
| `AuthorizeMcpServerRequest` | `repeated string scopes`, `bool no_scope` | 2, 3 |
| `McpServer` | `bool rebind_pending` | 9 |
| `McpGatewaySettings` | `map<string, McpServerDefinition> definitions`, `map<string, McpPendingReplace> pending_replaces` | 3, 4 |
| `ListMcpServersResponse` | `repeated McpPendingReplace pending_replaces` | 3 |
| `Event.McpGatewayChanged` | `McpSaveOutcome outcome`, `repeated McpPendingReplace pending_replaces` | 2, 3 |
| `Sandbox` | `repeated McpSkippedServer mcp_skipped` | 25 |

`ListMcpServersResponse.settings` is sent with `definitions` and `pending_replaces` **cleared** (the list
carries pending replaces in field 3; definitions only travel through `GetMcpServerDefinition`).

`DaemonInfo.capabilities` gains `"mcp-edit"`.

## Status codes

| Condition | Code | Detail |
|-----------|------|--------|
| Rule violation (FR-408/417/420/423) | `INVALID_ARGUMENT` | message names the field/placeholder |
| Fingerprint differs | `FAILED_PRECONDITION` | `McpSaveRefusal{STALE_CHANGED \| STALE_RESTARTED}` |
| Retry of an ended pending replace | `FAILED_PRECONDITION` | `PENDING_ENDED` |
| Retry pre-step removal still listed | `FAILED_PRECONDITION` | `PENDING_REMOVAL_FAILED` |
| Auth in progress (FR-424) | `FAILED_PRECONDITION` | `AUTH_IN_PROGRESS` |
| Authorize while saving / pending | `FAILED_PRECONDITION` | `BEING_REPLACED` |
| Add of a name with a pending replace | `FAILED_PRECONDITION` | `PENDING_EXISTS` |
| Edit of a name no longer listed (not `for_add`) | `NOT_FOUND` | `GONE` |
| Re-registering save with an unconverted `LITERAL_ON_HOST` / `RESOLVED_UNCHECKABLE` header | `INVALID_ARGUMENT` | message names the header, never its value |
| Local not acknowledged | `FAILED_PRECONDITION` | existing |
| Host step failed | `OK` with `McpSaveOutcome` (state + `host_errors`) | the save ran; the outcome describes it |
| Gateway/secrets unavailable | `FAILED_PRECONDITION` | existing FR-079 reason |

A header in `SaveMcpServerRequest.definition` that the view flagged `LITERAL_ON_HOST` / `RESOLVED_UNCHECKABLE` and
that still holds the view's redacted value counts as unchanged (research R2) and is accepted only on a save that does
not re-register.

Error messages never quote a credential header's value (research R1).

Secret values: `SaveMcpServerRequest.secret_values` is excluded from every log line; the gRPC logging
interceptor (if any) must not print the request; tests assert no value reaches argv, logs or errors.
