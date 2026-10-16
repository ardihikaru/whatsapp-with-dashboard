#!/usr/bin/env bash
# Regenerate wsm-mcp's gRPC stubs from proto/wsmmcp/v1/mcp.proto with a
# containerized protoc (no host install). Generated *.pb.go files are
# committed so builds skip codegen. Same pattern as hikaru-mcp's own
# scripts/gen-proto.sh.
set -euo pipefail

REPO_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
IMAGE="${PROTOC_IMAGE:-rvolosatovs/protoc:4.1.0}"

docker run --rm \
  -v "${REPO_DIR}:/repo" \
  -w "/repo/proto/wsmmcp/v1" \
  --user "$(id -u):$(id -g)" \
  "${IMAGE}" \
  --proto_path=. \
  --go_out=paths=source_relative:. \
  --go-grpc_out=paths=source_relative:. \
  mcp.proto

ls -la "${REPO_DIR}"/proto/wsmmcp/v1/*.pb.go
