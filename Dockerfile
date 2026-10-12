# ---------- stage 1: build the WebUI ----------
# Kept separate so the Node toolchain never reaches the runtime image.
FROM node:22-alpine AS web

WORKDIR /src/web

# Copy the manifests first: the dependency layer is then cached across source
# edits, so a change to App.tsx does not re-run npm ci.
COPY web/package.json web/package-lock.json ./
RUN npm ci

COPY web/ ./
# tsc -b --noEmit && vite build, per web/package.json.
RUN npm run build


# ---------- stage 2: build the server ----------
FROM golang:1.27.2-alpine AS server

WORKDIR /src

# The module cache is its own layer for the same reason as above.
COPY go.mod go.sum ./
RUN go mod download

COPY cmd/ cmd/
COPY internal/ internal/

# CGO is off: the SQLite driver (glebarez/sqlite -> modernc) is pure Go, so the
# result is a static binary that runs on a bare scratch/alpine rootfs.
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags='-s -w' -o /qqbot ./cmd/qqbot


# ---------- stage 3: runtime ----------
FROM alpine:3.21

# ca-certificates: the service talks to new-api and NapCat over HTTPS.
# tzdata: the check-in day boundary is computed in the configured timezone
# (Asia/Shanghai by default), and a bare alpine has no zoneinfo database, so
# time.LoadLocation would silently fall back to UTC.
# wget (busybox) backs the healthcheck below.
RUN apk add --no-cache ca-certificates tzdata

# Run unprivileged. The data directory is the only thing that needs to be
# writable, and it is a volume, so the image itself stays read-only.
RUN adduser -D -u 10001 -h /app qqbot

WORKDIR /app

COPY --from=server /qqbot /app/qqbot
COPY --from=web /src/web/dist /app/web/dist

# State (SQLite database + config.yaml) lives here; compose mounts a volume.
RUN mkdir -p /app/data && chown -R qqbot:qqbot /app

USER qqbot

# Matches the built-in default; QQBOT_PORT in the environment overrides it.
EXPOSE 4096

# The panel answers /api/auth/state without a session, which is enough to prove
# the HTTP server is up. -addr is passed explicitly so the probe does not depend
# on QQBOT_PORT being set.
HEALTHCHECK --interval=30s --timeout=5s --start-period=10s --retries=3 \
  CMD wget -q -O /dev/null http://127.0.0.1:4096/api/auth/state || exit 1

ENTRYPOINT ["/app/qqbot"]
CMD ["-addr", ":4096", "-data", "/app/data", "-web", "/app/web/dist"]
