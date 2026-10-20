# Build the manager binary
# Digest re-pinned 2026-10-20, and before that 2026-08-14: each time quay.io
# re-pushed the 1.26 tag and removed the old digest (sha256:506a668e..., before
# it sha256:94758a60...), which then resolved to "not found". Every build
# without that layer cached failed -- CI could not build main at all, while
# local builds kept succeeding from cache and hid it.
FROM quay.io/projectquay/golang:1.26@sha256:d5acf07940fb5e6e2d07542abd853cece623d95ffd364769ec825aab5471d3b8 AS builder
ARG TARGETOS
ARG TARGETARCH

WORKDIR /workspace
# Copy the Go Modules manifests
COPY go.mod go.mod
COPY go.sum go.sum
# cache deps before building and copying source so that we don't need to re-download as much
# and so that source changes don't invalidate our downloaded layer
RUN go mod download

# Copy the go source
COPY cmd/main.go cmd/main.go
COPY internal/ internal/

# Build
# the GOARCH has not a default value to allow the binary be built according to the host where the command
# was called. For example, if we call make docker-build in a local env which has the Apple Silicon M1 SO
# the docker BUILDPLATFORM arg will be linux/arm64 when for Apple x86 it will be linux/amd64. Therefore,
# by leaving it empty we can ensure that the container and binary shipped on it will have the same platform.
RUN CGO_ENABLED=0 GOOS=${TARGETOS:-linux} GOARCH=${TARGETARCH} go build -a -o manager cmd/main.go

# Use distroless as minimal base image to package the manager binary
# Refer to https://github.com/GoogleContainerTools/distroless for more details
FROM gcr.io/distroless/static:nonroot@sha256:f7f8f729987ad0fdf6b05eeeae94b26e6a0f613bdf46feea7fc40f7bd72953e6

# The repository this image is built from. The workflows pass their own
# (github.server_url/github.repository), so a fork's image names the fork.
ARG IMAGE_SOURCE=https://github.com/ev-shindin/llm-scaling-manager
LABEL org.opencontainers.image.source="${IMAGE_SOURCE}"
LABEL org.opencontainers.image.description="Workload Variant Autoscaler (WVA) - GPU-aware autoscaler for LLM inference workloads"
LABEL org.opencontainers.image.licenses="Apache-2.0"

WORKDIR /
COPY --from=builder /workspace/manager .
USER 65532:65532

ENTRYPOINT ["/manager"]
