# Proto delta: SWI-21 (additive to `src/libs/switchboard-proto/proto/switchboard.proto`)

Every change is additive. Older clients keep working: they never send the new fields, and the old RPCs keep
their behaviour (FR-422), with one deliberate exception (*Compatibility* below). No `NotificationKind` is added. `AddMcpServer` in particular keeps today's header
validation: the broader `mcprules.CheckCredentialValue` rule applies only on `SaveMcpServer` (research R2).
The definition `AddMcpServer` records holds any literal it flags redacted, with the name in `literal_on_host`.

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

// Crash safety (research R2): written by a registration change under the per-name lock before its
// first host mutation, deleted by its persist. Leftovers (from an earlier daemon run)
// are resolved only by the manager's recovery routine.
// Never sent to clients. Kept apart from McpPendingReplace so a Retry's pending replace is untouched.
message McpSaveInProgress {
  string name = 1;
  bool attach_by_default = 2;                 // the save's mark (copy)
  McpServerDefinition definition = 3;
  McpServerDefinition previous = 4;           // replace: live; Retry: pending's previous; new server: unset
  bool retry = 5;
  repeated string planned_created_secrets = 6;     // absent in the snapshot
  repeated string planned_overwritten_secrets = 7; // present in the snapshot
  // The save's `mcp rm` failed and its re-list failed too (recoverLater): nothing was stored and
  // `mcp add` never ran, so recovery records only whether the remove landed. remove_error holds
  // the host errors. The planned_* lists are cleared when this is set.
  bool remove_only = 8;
  string remove_error = 9;
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
  string field = 1;                     // "url", "headers", "headers/<Name>", "client_id", "oauth_authorization_server", "scopes", "command", "kind"
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
  // A live save of the name holds an in-progress record (research R2); when the name is not listed
  // this is returned instead of NOT_FOUND.
  bool save_in_progress = 16;
  // A leftover in-progress record is waiting for recovery (research R2); same NOT_FOUND rule.
  bool recovering = 17;
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
  enum Reason { REASON_UNSPECIFIED = 0; STALE_CHANGED = 1; STALE_RESTARTED = 2; PENDING_ENDED = 3; PENDING_REMOVAL_FAILED = 4; AUTH_IN_PROGRESS = 5; BEING_REPLACED = 6; PENDING_EXISTS = 7; GONE = 8; UNVERIFIED = 9; }
  Reason reason = 1;
  string message = 2;
}
```

## Field additions on existing messages

| Message | Field | Number |
|---------|-------|--------|
| `AddMcpServerRequest` | `repeated string scopes` | 9 |
| `AuthorizeMcpServerRequest` | `repeated string scopes`, `bool no_scope` | 2, 3 |
| `McpServer` | `bool rebind_pending`, `bool save_in_progress` (a live save's in-progress record, research R2), `bool recovering` (a leftover in-progress record waiting for recovery) | 9, 10, 11 |
| `McpGatewaySettings` | `map<string, McpServerDefinition> definitions`, `map<string, McpPendingReplace> pending_replaces`, `map<string, McpSaveInProgress> saves_in_progress` | 3, 4, 5 |
| `ListMcpServersResponse` | `repeated McpPendingReplace pending_replaces` | 3 |
| `Event.McpGatewayChanged` | `McpSaveOutcome outcome`, `repeated McpPendingReplace pending_replaces` | 2, 3 |
| `Sandbox` | `repeated McpSkippedServer mcp_skipped` | 25 |

`ListMcpServersResponse.settings` is sent with `definitions`, `pending_replaces` and `saves_in_progress` **cleared** (the list
carries pending replaces in field 3; definitions only travel through `GetMcpServerDefinition`).

`DaemonInfo.capabilities` gains `"mcp-edit"`.

## Status codes

| Condition | Code | Detail |
|-----------|------|--------|
| Rule violation (FR-408/417/420/423) | `INVALID_ARGUMENT` | message names the field/placeholder |
| Fingerprint differs | `FAILED_PRECONDITION` | `McpSaveRefusal{STALE_CHANGED \| STALE_RESTARTED}` |
| Fingerprint re-read failed (`ls`/`inspect`/`auth status`) | `FAILED_PRECONDITION` | `UNVERIFIED` (retryable; the form is not reloaded) |
| Leftover in-progress record not yet recovered (research R2), on `SaveMcpServer`, Cancel, `RemoveMcpServer`, `RemoveMcpServerSecrets`, `AddMcpServer` or `SetMcpServerDefault` | `FAILED_PRECONDITION` | `UNVERIFIED` ("recovering an interrupted save; try again") |
| `SetMcpServerDefault`, `RemoveMcpServer`, `RemoveMcpServerSecrets`, Cancel, `SaveMcpServer` or `AddMcpServer` while a live save holds an in-progress record or retries its `persist` (`saving`/`recording`), including a caller already queued on the lock | `FAILED_PRECONDITION` | `BEING_REPLACED` ("a save of this server is still running; try again later") |
| Runtime `persist` failed after 3 attempts (the daemon keeps retrying the same write) | `UNAVAILABLE` | `google.rpc.ErrorInfo{reason: "MCP_SAVE_NOT_RECORDED", domain: "switchboard"}` (no `McpSaveRefusal`); registration change: "the host was changed but the result could not be recorded yet; retrying"; switchboard-only save: "the save ran but its result (mark, re-bind state) could not be recorded yet; retrying" plus the stored secrets. A client tells this from a transport `UNAVAILABLE` (where the save may never have reached the daemon) only by the `ErrorInfo` |
| Replace whose `mcp rm` failed and whose re-list failed too (research R7) | `FAILED_PRECONDITION` | `UNVERIFIED` (names the host errors; the in-progress record stays and recovery resolves it) |
| Empty `secret_values` entry, or one containing `••••` | `INVALID_ARGUMENT` | message names the secret, never the value |
| Retry of an ended pending replace | `FAILED_PRECONDITION` | `PENDING_ENDED` |
| Retry pre-step removal still listed | `FAILED_PRECONDITION` | `PENDING_REMOVAL_FAILED` |
| Auth in progress (FR-424) | `FAILED_PRECONDITION` | `AUTH_IN_PROGRESS` |
| Authorize while saving / pending | `FAILED_PRECONDITION` | `BEING_REPLACED` |
| Add of a name with a pending replace | `FAILED_PRECONDITION` | `PENDING_EXISTS` |
| Edit of a name no longer listed (not `for_add`) | `NOT_FOUND` | `GONE` |
| Re-registering save with an unconverted `LITERAL_ON_HOST` header | `INVALID_ARGUMENT` | message names the header, never its value |
| Local not acknowledged | `FAILED_PRECONDITION` | existing |
| Host step failed | `OK` with `McpSaveOutcome` (state + `host_errors`) | the save ran; the outcome describes it |
| Gateway/secrets unavailable | `FAILED_PRECONDITION` | existing FR-079 reason |

A header in `SaveMcpServerRequest.definition` that the view flagged `LITERAL_ON_HOST` and that still holds the view's
redacted value counts as unchanged (research R2) and is accepted only on a save that does not re-register. A
`RESOLVED_UNCHECKABLE` header carries the record's template and is accepted on any save (FR-423).

Error messages never quote a credential header's value (research R1).

## Compatibility

The one behaviour change to an existing RPC: `SetMcpServerDefault` on a name that another client is saving
(`saving`, or `recording` for a switchboard-only save retrying its `persist`) or that has a leftover in-progress record (`recovering`) is refused with `FAILED_PRECONDITION` and
changes nothing (research R2 *Leftover records*), until the save's result is recorded (normally within
`saveTimeout`; longer only while a failed `persist` is retried, research R2 *Failed `persist`*). `AddMcpServer`
and `RemoveMcpServer` of a `saving`/`recording` name are likewise refused at once with `BEING_REPLACED` instead
of waiting for the lock, also when they were already queued on it; a name is `saving`/`recording` only while a
new client's `SaveMcpServer` runs. An older TUI has
no refusal handling, so its `m`/`space` toggle shows the status message on its usual error line: "a save of
this server is still running; try again later" or "recovering an interrupted save; try again". This is deliberate: a mark
changed under an in-progress record would be overwritten by that save's `persist` or by recovery.

Secret values: `SaveMcpServerRequest.secret_values` is excluded from every log line; the gRPC logging
interceptor (if any) must not print the request; tests assert no value reaches argv, logs or errors.
