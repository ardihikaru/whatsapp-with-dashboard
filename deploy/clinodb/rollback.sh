#!/usr/bin/env bash
# Run the previous image (wsm-mcp:prev) again and restart.
set -euo pipefail
docker image inspect wsm-mcp:prev >/dev/null   # fails loudly if there is nothing to roll back to
docker tag wsm-mcp:prev wsm-mcp:current
systemctl --user restart wsm-mcp.service
