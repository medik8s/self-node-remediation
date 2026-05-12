# Build the manager binary
FROM quay.io/centos/centos:stream9 AS builder
RUN yum install git jq -y && yum clean all

WORKDIR /workspace
# Copy the Go Modules manifests
COPY go.mod go.mod
COPY go.sum go.sum

# use latest Go z release
ENV GOTOOLCHAIN=auto

# Ensure correct Go version
RUN \
    # get Go version from mod file
    export GO_VERSION=$(grep -oE "toolchain go[[:digit:]]\.[[:digit:]]+\.[[:digit:]]" go.mod | awk '{print $2}') && \
    echo ${GO_VERSION} && \
    # find filename for latest z version from Go download page
    export GO_FILENAME=$(curl -sL 'https://go.dev/dl/?mode=json&include=all' | jq -r "[.[] | select(.version == \"${GO_VERSION}\")][0].files[] | select(.os == \"linux\" and .arch == \"amd64\") | .filename") && \
    echo ${GO_FILENAME} && \
    # download and unpack
    curl -sL -o go.tar.gz "https://golang.org/dl/${GO_FILENAME}" && \
    tar -C /usr/local -xzf go.tar.gz && \
    rm go.tar.gz

# add Go to PATH
ENV PATH="/usr/local/go/bin:${PATH}"
RUN go version

# Copy the go source
COPY vendor/ vendor/
COPY version/ version/
COPY cmd/ cmd/
COPY hack/ hack/
COPY api/ api/
COPY internal/ internal/
# for getting version info
COPY .git/ .git/
COPY install/ install/
# Build
RUN ./hack/build.sh

FROM registry.access.redhat.com/ubi9/ubi:latest

WORKDIR /
COPY --from=builder /workspace/install/ install/
COPY --from=builder /workspace/bin/manager .

USER 65532:65532
ENTRYPOINT ["/manager"]
