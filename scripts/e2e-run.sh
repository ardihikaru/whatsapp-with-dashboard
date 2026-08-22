#!/usr/bin/env bash
# Run the Playwright suite. Thin wrapper whose only job is the browser's
# shared-library path.
#
# `npx playwright install-deps chromium` is the correct fix and needs root. On a
# host where that is not available (no sudo, or the dpkg lock is held), the same
# 71 packages can be unpacked into a private prefix with no system change:
#
#   mkdir -p ~/.cache/ms-playwright-libs && cd "$(mktemp -d)"
#   apt-get download $(npx playwright install-deps --dry-run chromium | tail -n +2)
#   for d in *.deb; do dpkg -x "$d" ~/.cache/ms-playwright-libs; done
#
# If that prefix exists this script puts it on LD_LIBRARY_PATH; otherwise it
# changes nothing and the system libraries are used.
set -euo pipefail

repo="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$repo"

libs="${WSM_E2E_BROWSER_LIBS:-$HOME/.cache/ms-playwright-libs/usr/lib/x86_64-linux-gnu}"
if [ -d "$libs" ]; then
  export LD_LIBRARY_PATH="$libs${LD_LIBRARY_PATH:+:$LD_LIBRARY_PATH}"
fi

exec npx playwright test "$@"
