#!/usr/bin/env bash
# Pull main, rebuild, restart, wait for health. Run ON clinodb:
#   bash ~/devel/projects/whatsapp-with-dashboard/deploy/clinodb/update.sh
set -euo pipefail
repo=${1:-$HOME/devel/projects/whatsapp-with-dashboard}
git -C "$repo" pull --ff-only
bash "$repo/deploy/clinodb/install.sh"
