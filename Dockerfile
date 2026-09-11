# Build the manager binary
FROM quay.io/konveyor/builder:ubi9-latest AS builder
ARG TARGETOS
ARG TARGETARCH

WORKDIR /workspace

# Copy the Go Modules manifests
COPY go.mod go.sum ./

# Set GOTOOLCHAIN to auto to allow Go to download newer versions
# Set to local to avoid downloading newer versions of Go
ENV GOTOOLCHAIN=auto

# Copy the Go source and runtime install assets.
COPY vendor/ vendor/
COPY version/ version/
COPY cmd/ cmd/
COPY hack/ hack/
COPY api/ api/
COPY internal/ internal/
COPY install/ install/
# for getting version info
COPY .git/ .git/

RUN go version

RUN git config --global --add safe.directory /workspace
RUN ./hack/build.sh -o bin/manager ./cmd/main.go

FROM registry.access.redhat.com/ubi9/ubi-minimal:latest

WORKDIR /

# util-linux: nsenter is required by the self-node-remediation agent
# iproute: ip command is needed by e2e tests to simulate API server disconnection
RUN microdnf install -y --setopt=install_weak_deps=0 util-linux iproute && microdnf clean all -y

COPY --from=builder /workspace/install/ install/
COPY --from=builder /workspace/bin/manager .

USER 65532:65532
ENTRYPOINT ["/manager"]
