# whatsapp-with-dashboard — the whatsmeow gateway.
#
# HARD SINGLETON: one device websocket + one SQLite crypto file. Never scale
# this service and never roll it. Two processes against one store make WhatsApp
# play StreamReplaced ping-pong until the session logs itself out.
#
# BUILD CONTEXT IS THE REPO ROOT (this is a standalone module now):
#   docker build -t whatsapp-with-dashboard:local .
#
# CGO_ENABLED=0 onto distroless: the crypto store is modernc.org/sqlite (pure
# Go), NOT mattn/go-sqlite3. Read internal/db.SQLiteDialect before swapping back.

# ---- build ----
FROM golang:1.25-alpine AS build
WORKDIR /src

# Dependencies first, so a source-only edit reuses the module layer.
COPY go.mod go.sum ./
RUN go mod download

COPY . .

RUN CGO_ENABLED=0 GOOS=linux GOFLAGS=-trimpath \
    go build -ldflags="-s -w" -o /out/wsm ./cmd/app

# /data is created HERE, owned by distroless's nonroot uid, and copied into the
# runtime image below.
#
# WITHOUT THIS THE APP CANNOT WRITE /data/wsm.db. distroless/static has no shell,
# so there is no `RUN mkdir` available in the final stage, and Docker creates an
# empty named volume as root:root — which uid 65532 cannot open for write. Docker
# seeds a FRESH named volume from the image path's contents *and ownership*, so
# this layer is what makes the wsm-sqlite volume writable on first boot.
RUN install -d -m 0700 -o 65532 -g 65532 /seed-data

# ---- runtime ----
FROM gcr.io/distroless/static:nonroot
COPY --from=build /out/wsm /app/wsm
COPY --from=build --chown=65532:65532 /seed-data /data
USER nonroot:nonroot
EXPOSE 8800

# WSM_DB_PATH=/data/wsm.db lives on the `wsm-sqlite` volume — the crown jewel.
# Losing it means re-pairing the handset by QR, in person. Back it up with the
# SQLite online-backup API, never a cp of a live WAL file.
#
# No HEALTHCHECK: distroless/static has no shell, wget or curl, and HEALTHCHECK
# has no non-exec form — a wget-based one would fail 100% of the time and mark a
# perfectly healthy container unhealthy. GET /api/v1/health is public and
# unauthenticated; probe it from outside.
ENTRYPOINT ["/app/wsm"]
