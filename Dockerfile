# syntax=docker/dockerfile:1

# Keep the builder and runtime on the same Debian generation for glibc.
# Official image digests resolved 2026-10-30; refresh both with security updates.
FROM docker.io/library/golang:1.27.3-trixie@sha256:c65ecfe613b539ac74c62a4c3b2c27e8f8a6076384c8f27e8a3c0f3be17cc385 AS build

WORKDIR /src
ENV CGO_ENABLED=1

COPY go.mod go.sum ./
RUN go mod download
COPY *.go ./
RUN go build -trimpath -ldflags="-s -w" -o /out/nvidia-fan-control .

FROM docker.io/library/debian:trixie-slim@sha256:f6b08b09a05725f9431502d3b5714f5c4576e82c1a84fa1debe0c24a7e0c3ec4 AS runtime

# The host NVIDIA Container Toolkit supplies NVML and the selected device nodes.
ENV NVIDIA_DRIVER_CAPABILITIES=utility
COPY --from=build /out/nvidia-fan-control /usr/local/bin/nvidia-fan-control
COPY LICENSE /usr/share/doc/nvidia-fan-control/LICENSE
WORKDIR /data
USER 0:0
ENTRYPOINT ["/usr/local/bin/nvidia-fan-control"]
# Phase 1 is a read-only diagnostic checkpoint; controller hardening is phase 2.
CMD ["--diagnose"]
