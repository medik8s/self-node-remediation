# Build the manager binary
FROM quay.io/centos/centos:stream8 AS builder
RUN yum install golang -y

# Ensure correct Go version
ENV GO_VERSION=1.18
RUN go install golang.org/dl/go${GO_VERSION}@latest
RUN ~/go/bin/go${GO_VERSION} download
RUN /bin/cp -f ~/go/bin/go${GO_VERSION} /usr/bin/go
RUN go version

WORKDIR /workspace
# Copy the Go Modules manifests
COPY go.mod go.mod
COPY go.sum go.sum
# cache deps before building and copying source so that we don't need to re-download as much
# and so that source changes don't invalidate our downloaded layer
RUN go mod download

# Copy the go source
COPY main.go main.go
COPY api/ api/
COPY controllers/ controllers/
COPY pkg/ pkg/
COPY install/ install/
# Build
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 GO111MODULE=on go build -a -o manager main.go

# Use ubi-micro as minimal base image to package the manager binary - https://catalog.redhat.com/software/containers/ubi8/ubi-micro/5ff3f50a831939b08d1b832a
FROM registry.access.redhat.com/ubi8/ubi-micro:latest

WORKDIR /
COPY --from=builder /workspace/install/ install/
COPY --from=builder /workspace/manager .

ENTRYPOINT ["/manager"]
