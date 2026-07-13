#!/usr/bin/env bash
# Regenerate the Go gRPC stubs from proto/switchboard.proto into gen/.
# Requires: protoc, protoc-gen-go, protoc-gen-go-grpc on PATH.
set -euo pipefail
here="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
cd "$here"
protoc \
  --proto_path=proto \
  --go_out=gen --go_opt=paths=source_relative \
  --go-grpc_out=gen --go-grpc_opt=paths=source_relative \
  proto/switchboard.proto
echo "generated gen/switchboard.pb.go gen/switchboard_grpc.pb.go"
