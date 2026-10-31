# syntax=docker/dockerfile:1

FROM docker.io/library/node:24.22.2-trixie-slim@sha256:54f84b4cf8432b21aaffd5d3ce8d41e12cea0d10d33c09d2f3c9879d49d6b446 AS frontend
WORKDIR /src/web
COPY web/package.json web/package-lock.json ./
RUN npm ci
COPY web/ ./
RUN npm run check && npm run build

# Keep the builder and runtime on the same Debian generation for glibc.
# Official image digests resolved 2026-10-30; refresh both with security updates.
FROM docker.io/library/golang:1.27.3-trixie@sha256:c65ecfe613b539ac74c62a4c3b2c27e8f8a6076384c8f27e8a3c0f3be17cc385 AS build

WORKDIR /src
ENV CGO_ENABLED=1

COPY go.mod go.sum ./
RUN go mod download
COPY cmd/ ./cmd/
COPY internal/ ./internal/
COPY --from=frontend /src/internal/web/dist ./internal/web/dist
RUN go build -trimpath -ldflags="-s -w" -o /out/server-fan-control ./cmd/server-fan-control

FROM docker.io/library/debian:trixie-slim@sha256:f6b08b09a05725f9431502d3b5714f5c4576e82c1a84fa1debe0c24a7e0c3ec4 AS runtime

RUN apt-get update && apt-get install -y --no-install-recommends ipmitool smartmontools \
    && rm -rf /var/lib/apt/lists/*

# The host NVIDIA Container Toolkit supplies NVML and the selected device nodes.
ENV NVIDIA_DRIVER_CAPABILITIES=utility
COPY --from=build /out/server-fan-control /usr/local/bin/server-fan-control
COPY LICENSE /usr/share/doc/server-fan-control/LICENSE
WORKDIR /data
USER 0:0
ENTRYPOINT ["/usr/local/bin/server-fan-control"]
EXPOSE 8080
CMD ["--config", "/data/config.json", "--listen", "0.0.0.0:8080", "--admin-token-file", "/run/secrets/admin-token"]
