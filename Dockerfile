# Build the manager binary
FROM quay.io/centos/centos:stream9 AS builder

# Define build arguments for multi-arch support
ARG TARGETARCH
ARG TARGETOS=linux

# Validate TARGETARCH if set, or detect from system
# Currently supporting amd64 (x86_64) and s390x (IBM Z) architectures
# TARGETARCH is automatically set by Docker BuildKit during multi-platform builds
# If not set, we'll detect it from the system in the next step
RUN if [ -n "${TARGETARCH}" ]; then \
        case ${TARGETARCH} in \
            amd64|s390x) echo "Building for specified architecture: ${TARGETARCH}" ;; \
            *) echo "ERROR: Unsupported architecture: ${TARGETARCH}. Only amd64 and s390x are supported." && exit 1 ;; \
        esac; \
    else \
        echo "TARGETARCH not set, will auto-detect from system"; \
    fi

RUN yum install git jq -y && yum clean all

WORKDIR /workspace
# Copy the Go Modules manifests
COPY go.mod go.mod
COPY go.sum go.sum

# use latest Go z release
ENV GOTOOLCHAIN=auto

# Ensure correct Go version
RUN \
    # Use TARGETARCH if set by BuildKit, otherwise detect from system
    if [ -n "${TARGETARCH}" ]; then \
        GOARCH=${TARGETARCH}; \
    else \
        SYSTEM_ARCH=$(uname -m); \
        case ${SYSTEM_ARCH} in \
            x86_64) GOARCH=amd64 ;; \
            s390x) GOARCH=s390x ;; \
            *) echo "ERROR: Unsupported system architecture: ${SYSTEM_ARCH}. Only x86_64 and s390x are supported." && exit 1 ;; \
        esac; \
    fi && \
    # get Go version from mod file
    export GO_VERSION=$(grep -oE "toolchain go[[:digit:]]\.[[:digit:]]+\.[[:digit:]]" go.mod | awk '{print $2}') && \
    echo "Go version: ${GO_VERSION}" && \
    echo "Target architecture: ${GOARCH}" && \
    # find filename for latest z version from Go download page
    export GO_FILENAME=$(curl -sL 'https://go.dev/dl/?mode=json&include=all' | jq -r "[.[] | select(.version == \"${GO_VERSION}\")][0].files[] | select(.os == \"linux\" and .arch == \"${GOARCH}\") | .filename") && \
    echo "Go filename: ${GO_FILENAME}" && \
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
COPY main.go main.go
COPY hack/ hack/
COPY api/ api/
COPY controllers/ controllers/
# for getting version info
COPY .git/ .git/
COPY pkg/ pkg/
COPY install/ install/

# Build - pass TARGETARCH to build script
ENV TARGETARCH=${TARGETARCH}
RUN ./hack/build.sh

FROM registry.access.redhat.com/ubi9/ubi:latest

WORKDIR /
COPY --from=builder /workspace/install/ install/
COPY --from=builder /workspace/bin/manager .

USER 65532:65532
ENTRYPOINT ["/manager"]
