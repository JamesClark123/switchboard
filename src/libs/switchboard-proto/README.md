# @tms/switchboard-proto — shared gRPC contract + domain helpers

The single shared library carrying the Switchboard gRPC/protobuf contract and small
domain-type helpers used by both the daemon and the TUI client. Per the Repository
Structure rule, this `libs` module imports **no other** switchboard category.

## Regenerating the stubs

```bash
./gen.sh   # requires protoc + protoc-gen-go + protoc-gen-go-grpc on PATH
```

`gen/` is generated from `proto/switchboard.proto` (mirrored from
`specs/001-sandbox-session-manager/contracts/switchboard.proto`). The generated
package is excluded from lint and coverage.

## Contents

- `gen/` — generated `switchboard.pb.go` / `switchboard_grpc.pb.go`.
- `types.go` — enum label helpers, seeding-mode mapping, and JSON kit-option
  encode/decode used across modules.
