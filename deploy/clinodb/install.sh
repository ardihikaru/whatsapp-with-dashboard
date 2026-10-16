#!/usr/bin/env bash
# Build the wsm-mcp image and (re)install the systemd USER unit on clinodb
# (sudoless). Image tags: wsm-mcp:<VERSION> (immutable) · wsm-mcp:current
# (what the unit runs) · wsm-mcp:prev (rollback). Same pattern as
# ardi-personal-workspace-ai/apps/hikaru/deploy/install-user-service.sh.
set -euo pipefail
deploy_dir=$(cd "$(dirname "$0")" && pwd)
repo=$(cd "$deploy_dir/../.." && pwd)
version=$(cat "$repo/mcp/VERSION")

install -d -m 700 "$HOME/.config/wsm-mcp"
install -d "$HOME/.config/systemd/user"

if docker image inspect wsm-mcp:current >/dev/null 2>&1; then
  docker tag wsm-mcp:current wsm-mcp:prev
fi
DOCKER_BUILDKIT=1 docker build -f "$repo/Dockerfile.mcp" -t "wsm-mcp:$version" -t wsm-mcp:current "$repo"
docker builder prune -f --filter until=72h >/dev/null || true

cp "$deploy_dir/wsm-mcp.service" "$HOME/.config/systemd/user/"
systemctl --user daemon-reload
systemctl --user enable wsm-mcp.service
systemctl --user restart wsm-mcp.service

for _ in $(seq 1 20); do
  if curl --fail --silent -o /dev/null "http://$(grep -E '^WSM_MCP_HTTP_ADDR=' "$HOME/.config/wsm-mcp/mcp.env" | cut -d= -f2)/healthz"; then
    echo "wsm-mcp ok ($version)"
    exit 0
  fi
  sleep 1
done
echo "wsm-mcp did not answer /healthz within 20s" >&2
exit 1
