# syntax=docker/dockerfile:1

ARG GO_VERSION=1.27

# ---- build stage: full Go toolchain, never shipped ----
# --platform=$BUILDPLATFORM runs the compiler natively and cross-compiles,
# which is much faster than emulating e.g. arm64 under QEMU.
FROM --platform=$BUILDPLATFORM golang:${GO_VERSION}-alpine AS build
WORKDIR /src

# Copy only the module files first so the dependency layer stays cached
# until go.mod/go.sum actually change.
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod go mod download

COPY . .
ARG TARGETOS TARGETARCH
# CGO_ENABLED=0 gives a fully static binary that runs without libc.
# -trimpath drops local paths; -s -w strips debug symbols (~30% smaller).
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH \
    go build -trimpath -ldflags="-s -w" -o /out/server ./cmd/server

# ---- runtime stage: just the binary ----
# distroless/static has no shell or package manager (tiny attack surface)
# but, unlike scratch, ships CA certificates (needed to call
# https://api.github.com) and a nonroot user.
FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/server /server
USER nonroot:nonroot
EXPOSE 8080
ENTRYPOINT ["/server"]
