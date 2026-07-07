# syntax=docker/dockerfile:1
#
# Single multi-arch image containing both binaries. The entrypoint is the operator;
# exporter Deployments the operator creates override the command to /exporter. Go
# cross-compiles from the native BUILDPLATFORM (no QEMU needed for the compile step).

FROM --platform=$BUILDPLATFORM golang:1.26-bookworm AS build
WORKDIR /src

COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod go mod download

COPY . .
ARG TARGETOS TARGETARCH
ARG VERSION=dev COMMIT=none DATE=unknown
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    set -eu; \
    for cmd in operator exporter; do \
      CGO_ENABLED=0 GOOS="$TARGETOS" GOARCH="$TARGETARCH" \
      go build -trimpath \
        -ldflags="-s -w -X main.version=${VERSION} -X main.commit=${COMMIT} -X main.date=${DATE}" \
        -o "/out/${cmd}" "./cmd/${cmd}"; \
    done

# Pinned by digest for reproducibility (multi-arch index); refresh via Renovate.
FROM gcr.io/distroless/static-debian13:nonroot@sha256:963fa6c544fe5ce420f1f54fb88b6fb01479f054c8056d0f74cc2c6000df5240
COPY --from=build /out/operator /operator
COPY --from=build /out/exporter /exporter
USER 65532:65532
ENTRYPOINT ["/operator"]
