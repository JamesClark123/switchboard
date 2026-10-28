# Contract: proto delta (SWI-18)

All changes are additive to `src/libs/switchboard-proto/proto/switchboard.proto`, apart from `Action.worker_tag`,
which is deprecated but kept (research R14). Field numbers avoid the ones SWI-17 (PR #21) claims: `Action` 9,
`Run` 20–30, `WorkerView` 12–14, `WorkerRun` 4–10, `UpdateWorkerRequest` 8, `NotificationKind` 9–10.

## New messages and enum

```proto
// A named group of workers on one orchestrator (SWI-18 FR-301..FR-305). Referenced by id everywhere.
message Lane {
  string id = 1;                          // "ln-" + 12 hex; minted by the daemon; never changes
  string name = 2;                        // FR-303 rule; unique per orchestrator; "open" reserved
  string description = 3;                 // one line, <= 200 runes; never sent to agents
  google.protobuf.Timestamp created_at = 4;
}

enum LaneMode {
  LANE_MODE_UNSPECIFIED = 0;              // no lanes
  LANE_MODE_EXCLUSIVE = 1;                // only workers in the lanes (FR-311)
  LANE_MODE_INCLUSIVE = 2;                // workers in the lanes, else open-lane workers (FR-312)
}

// Lanes picked at launch or attach, waiting for adoption (FR-306b).
message AttachTicket {
  string id = 1;                          // 16 hex
  repeated string lane_ids = 2;
  string host_id = 3;
  string sandbox_id = 4;                  // attach only
  google.protobuf.Timestamp issued_at = 5; // expires 24 h later
}

// Stops stale records re-adopting a dropped sandbox (FR-308c).
message HighWaterMark {
  uint64 revision = 1;
  bool removed = 2;                       // terminal
  uint64 set_seq = 3;                     // Orchestrator.roster_seq when set
  google.protobuf.Timestamp removed_at = 4;
  string host_id = 5;                     // host the sandbox lived on; scopes listing-based pruning
}

message DroppedWorkerTag {                // FR-318b
  string action = 1;
  string tag = 2;
  google.protobuf.Timestamp dropped_at = 3;
}

// A deleted action's lane setting, copied onto its queued runs (FR-314b).
message KeptLaneSetting {
  repeated string lane_ids = 1;           // empty => un-laned
  LaneMode mode = 2;
}

// Display copy pushed to a worker's owning daemon (FR-309a). Presence = set.
message WorkerLaneDisplay {
  repeated string names = 1;
  bool pending = 2;
}

// The open-lane guard's list as the developer saw it (FR-308a).
message LaneGuardConfirm {
  repeated string unlaned_actions = 1;
  repeated string unlaned_runs = 2;       // run ids of queued un-laned runs of deleted actions
}

// What a guarded change would affect (FR-305, FR-308a). Also the FailedPrecondition status detail of a refusal.
message LaneChangeImpact {
  repeated string unlaned_actions = 1;    // guard list; empty => guard does not fire
  repeated string unlaned_runs = 2;
  // lane delete only (FR-305):
  repeated string workers = 3;            // display names of member workers
  repeated LaneActionUse actions = 4;     // actions using the lane, with their mode
  repeated string actions_left_unlaned = 5;
  uint32 rerouted_runs = 6;               // queued runs whose routing changes, incl. kept copies
}
message LaneActionUse { string action = 1; LaneMode mode = 2; }

message EventsDropped {}                  // the hub dropped events for this subscriber (research R6)
```

## Extended messages

```proto
message Action {
  // ...1..5, 7, 8 unchanged; 9 = SWI-17 sequence
  string worker_tag = 6 [deprecated = true]; // SWI-18: read only on load to drop it (FR-318b); never written or reused
  repeated string lane_ids = 10;          // prompt only (FR-316)
  LaneMode lane_mode = 11;
}

message Worker {
  // 1..4 unchanged
  repeated string lane_ids = 5;
  bool lanes_pending = 6;                 // adopted through an invalid ticket (FR-306b)
  uint64 revision = 7;                    // sandbox revision this entry was written from (FR-308c)
  uint64 written_seq = 8;                 // Orchestrator.roster_seq at the write
}

message Orchestrator {
  // 1..10 unchanged
  repeated Lane lanes = 11;
  repeated AttachTicket attach_tickets = 12;
  map<string, HighWaterMark> high_water_marks = 13;  // cleared in summaries sent to clients
  repeated DroppedWorkerTag dropped_worker_tags = 14;
  uint64 roster_seq = 15;
}

message WorkerView {
  // 1..11 unchanged; 12..14 = SWI-17
  repeated string lane_ids = 15;
  bool lanes_pending = 16;
  bool detaching = 17;
}

message Run {
  // 1..19 unchanged; 20..30 = SWI-17
  KeptLaneSetting kept_lanes = 31;
  string waiting_reason = 32;             // derived on read, never stored (FR-315)
}

message Sandbox {
  // 1..23 unchanged
  uint64 revision = 24;                   // stamped by the registry on every write (FR-308c)
}

message SandboxController {
  // 1..6 unchanged
  repeated string lane_names = 7;         // display only (FR-309a/b)
  bool lanes_pending = 8;                 // display only
  string attach_ticket = 9;               // FR-306b; the orchestrator's token, not lane data
}

message UpdateWorkerRequest {
  // 1..7 unchanged; 8 = SWI-17 expect_run_id
  WorkerLaneDisplay lanes = 9;
}

message DaemonInfo {
  // 1..9 unchanged
  repeated string capabilities = 10;      // SWI-18 adds "lanes" (FR-318a)
}

message Event {
  oneof event {
    // 1..8 unchanged
    EventsDropped events_dropped = 9;
  }
}

message PutActionRequest {
  string orchestrator_id = 1;
  Action action = 2;
  bool sets_lanes = 3;                    // FR-318a: a lanes-aware client always sets it
  LaneGuardConfirm guard = 4;             // FR-308a
}
message GetWorkerBundleRequest {
  string orchestrator_id = 1;
  string host_id = 2;                     // target host; required when lane_ids is set
  repeated string lane_ids = 3;           // FR-306a; non-empty => an attach ticket is issued
}
message AttachWorkerRequest {
  string orchestrator_id = 1; string host_id = 2; string sandbox_id = 3;
  repeated string lane_ids = 4;           // FR-306a
}
message DetachWorkerRequest {
  string orchestrator_id = 1; string sandbox_id = 2;
  LaneGuardConfirm guard = 3;             // FR-308a
}
```

## New RPCs (appended to the service after `OrchestratorLink`)

```proto
rpc PutLane(PutLaneRequest) returns (Lane);
rpc DeleteLane(DeleteLaneRequest) returns (DeleteLaneResponse);
rpc SetWorkerLanes(SetWorkerLanesRequest) returns (WorkerView);
rpc SetLaneMembers(SetLaneMembersRequest) returns (Lane);
rpc CheckLaneChange(CheckLaneChangeRequest) returns (LaneChangeImpact);
rpc DismissDroppedWorkerTags(OrchestratorIdRequest) returns (Orchestrator);

message PutLaneRequest {
  string orchestrator_id = 1;
  string lane_id = 2;                     // empty => create
  string name = 3;
  string description = 4;
}
message DeleteLaneRequest {
  string orchestrator_id = 1;
  string lane_id = 2;
  LaneGuardConfirm guard = 3;
}
message DeleteLaneResponse { LaneChangeImpact applied = 1; }

// FR-306 / FR-307a: refused unless the worker's lanes and pending flag equal the expected ones.
message SetWorkerLanesRequest {
  string orchestrator_id = 1;
  string sandbox_id = 2;
  repeated string lane_ids = 3;           // empty => open lane (also clears lanes_pending)
  repeated string expected_lane_ids = 4;
  bool expected_lanes_pending = 5;
  LaneGuardConfirm guard = 6;
}
// FR-307 / FR-307a: refused unless the lane's members equal the expected ones.
message SetLaneMembersRequest {
  string orchestrator_id = 1;
  string lane_id = 2;
  repeated string sandbox_ids = 3;
  repeated string expected_sandbox_ids = 4;
  LaneGuardConfirm guard = 5;
}
message CheckLaneChangeRequest {
  string orchestrator_id = 1;
  oneof change {
    SetWorkerLanesRequest set_worker_lanes = 2;
    SetLaneMembersRequest set_lane_members = 3;
    PutActionRequest put_action = 4;
    DeleteLaneRequest delete_lane = 5;
    DetachWorkerRequest detach_worker = 6;
  }
}
```

## Status codes

| RPC | Code | When |
|-----|------|------|
| any lane write | `InvalidArgument` | name/description rule, cap, reason-with-lanes, missing mode |
| `PutLane` (create/rename) | `AlreadyExists` | name taken |
| `PutAction`, `PutLane`, `DeleteLane` | `NotFound` | a named lane or action no longer exists (FR-316a); the action form reloads its lane options |
| `SetWorkerLanes`, `SetLaneMembers` | `Aborted` | every FR-307a case: the worker's lanes or pending flag changed, the lane's members changed, the edited lane or a ticked lane was deleted, or the worker left the roster; the TUI reloads the picker |
| guarded writes | `FailedPrecondition` + `LaneChangeImpact` detail | guard fires and the confirm does not cover the current list |
| `PutAction` | `FailedPrecondition` | `sets_lanes` false or `worker_tag` set (old client) |
| `SetWorkerLanes` on the reasoning sandbox | `InvalidArgument` | FR-310 |

`Action.worker_tag` is deprecated, so the generated getter is too. The one load-time read (and its tests) carries
`//nolint:staticcheck // SA1019: read only to drop the retired worker tag (FR-318b)`.

`orchestrator_changed` is emitted after every lane, membership, ticket and dismissal write, as for other record
changes.
