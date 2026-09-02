#!/usr/bin/env sh
# Load the three production images on wa-phkg from the tar.gz bundle.
#
# Run FROM this directory, on wa-phkg:
#
#   ./load-images.sh
#
# Idempotent: `docker load` on an image that is already present is a no-op,
# and the alias tags are re-applied every run.
#
# POSIX sh, no bashisms — this host has no `make`, no Go toolchain, and there
# is no reason to assume anything beyond /bin/sh either.
set -eu

IMAGES_DIR=${IMAGES_DIR:-./images}
STAMP=20260901

cd "$IMAGES_DIR"

echo "### verify checksums"
# The same SHA256SUMS travelled with the tars from the production host, so this
# checks the whole path — save, two scp hops, and the disk here.
sha256sum -c SHA256SUMS

echo
echo "### load"
for f in wsm-app-$STAMP.tar.gz wsm-migrate-$STAMP.tar.gz waxum-0.12.2-uploaded-media.tar.gz; do
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
# waxum keeps its own tag: it is already pinned, already meaningful, and
# docker-compose.yml reads it from WAXUM_TAG in .env.

echo
echo "### result — these IDs must match production"
#   whatsapp-with-dashboard          d7b40a798051
#   whatsapp-with-dashboard-migrate  7c09964d975f
#   ardihikaru/waxum                 e7c51aa15c99
docker images --format '{{.Repository}}:{{.Tag}}\t{{.ID}}\t{{.Size}}' \
  | grep -E 'whatsapp-with-dashboard|waxum' \
  | sort
