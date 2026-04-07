#!/bin/bash -ex

GIT_VERSION=$(git describe --always --tags || true)
VERSION=${CI_UPSTREAM_VERSION:-${GIT_VERSION}}
GIT_COMMIT=$(git rev-list -1 HEAD || true)
COMMIT=${CI_UPSTREAM_COMMIT:-${GIT_COMMIT}}
BUILD_DATE=$(date --utc -Iseconds)

mkdir -p bin

LDFLAGS_VALUE="-X github.com/medik8s/self-node-remediation/version.Version=${VERSION} "
LDFLAGS_VALUE+="-X github.com/medik8s/self-node-remediation/version.GitCommit=${COMMIT} "
LDFLAGS_VALUE+="-X github.com/medik8s/self-node-remediation/version.BuildDate=${BUILD_DATE} "
# allow override for debugging flags
LDFLAGS_DEBUG="${LDFLAGS_DEBUG:-" -s -w"}"
LDFLAGS_VALUE+="${LDFLAGS_DEBUG}"
# must be single quoted for use in GOFLAGS, and for more options see https://pkg.go.dev/cmd/link
LDFLAGS="'-ldflags=${LDFLAGS_VALUE}'"

# add ldflags to goflags
export GOFLAGS+=" ${LDFLAGS}"
echo "goflags: ${GOFLAGS}"

# allow override and use zero by default- static linking
export CGO_ENABLED=${CGO_ENABLED:-0}
echo "cgo: ${CGO_ENABLED}"

# export in case it was set
export GOEXPERIMENT="${GOEXPERIMENT}"

# Use TARGETARCH from Docker buildx, or detect from system
export GOARCH=${TARGETARCH:-$(go env GOARCH)}
export GOOS=linux

echo "Building for GOOS=${GOOS} GOARCH=${GOARCH}"

go build -o bin/manager main.go

echo "Build complete. Binary info:"
file bin/manager || true
