#!/usr/bin/env sh
# Load the production images on wa-phkg from the tar.gz bundle.
#
# Run FROM this directory, on wa-phkg, naming the stamp of the build being
# transferred (no default — a stale hardcoded stamp here once silently
# reloaded an old build while believing it had loaded a new one):
#
#   ./load-images.sh 20261102
#   STAMP=20261102 ./load-images.sh
#
# Idempotent: `docker load` on an image that is already present is a no-op,
# and the alias tags are re-applied every run.
#
# POSIX sh, no bashisms — this host has no `make`, no Go toolchain, and there
# is no reason to assume anything beyond /bin/sh either.
set -eu

IMAGES_DIR=${IMAGES_DIR:-./images}
STAMP=${1:-${STAMP:-}}

if [ -z "$STAMP" ]; then
    echo "usage: $0 <stamp>   (or: STAMP=<stamp> $0)" >&2
    echo "  e.g. $0 20261102" >&2
    exit 1
fi

cd "$IMAGES_DIR"

echo "### verify checksums"
# The checksum file travels under the SAME stamp as the tars — SHA256SUMS
# alone, with no stamp, would silently go stale the moment a second
# transfer landed in the same directory and nobody noticed it was
# checksumming the WRONG tars.
sha256sum -c "SHA256SUMS-$STAMP"

echo
echo "### load"
# Only the app and migrate images travel in a normal transfer. waxum is
# NOT part of this: it already has its own pinned, meaningful tag
# (ardihikaru/waxum:$WAXUM_TAG, read from .env) and is pulled/loaded
# separately, on its own cadence, only when waxum itself changes — bundling
# a waxum tar into every app-only transfer just made every bundle bigger
# for no reason tied to what actually changed.
for f in wsm-app-$STAMP.tar.gz wsm-migrate-$STAMP.tar.gz; do
    echo "--- $f"
    gzip -dc "$f" | docker load
done

echo
echo "### alias tags"
# `:local` and `:latest` are the tags the production build machine used; they
# say nothing about WHICH build this is. docker-compose.yml names the dated
# aliases instead, so the running container is traceable to one transfer.
docker tag whatsapp-with-dashboard:local          "whatsapp-with-dashboard:phkg-$STAMP"
docker tag whatsapp-with-dashboard-migrate:latest "whatsapp-with-dashboard-migrate:phkg-$STAMP"

echo
echo "### result — these IDs must match production"
docker images --format '{{.Repository}}:{{.Tag}}\t{{.ID}}\t{{.Size}}' \
  | grep -E 'whatsapp-with-dashboard|waxum' \
  | sort
