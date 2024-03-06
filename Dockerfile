# Build the manager binary
FROM quay.io/centos/centos:stream8 AS builder
RUN yum install git golang -y && yum clean all

WORKDIR /workspace
# Copy the Go Modules manifests
COPY go.mod go.mod
COPY go.sum go.sum

# Ensure correct Go version
RUN export GO_VERSION=$(grep -E "go [[:digit:]]\.[[:digit:]][[:digit:]]" go.mod | awk '{print $2}') && \
    go get go@${GO_VERSION} && \
    go version


# Copy the go source
COPY vendor/ vendor/
COPY version/ version/
COPY main.go main.go
COPY hack/ hack/
COPY api/ api/
COPY controllers/ controllers/
# for getting version info
COPY .git/ .git/
COPY pkg/ pkg/
COPY install/ install/
# Build
RUN ./hack/build.sh

FROM registry.access.redhat.com/ubi8/ubi:latest

WORKDIR /
COPY --from=builder /workspace/install/ install/
COPY --from=builder /workspace/bin/manager .

ENTRYPOINT ["/manager"]
