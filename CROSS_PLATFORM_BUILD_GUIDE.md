# Cross-Platform Build Guide

This guide explains how to build the Self Node Remediation Operator for multiple architectures.

## Supported Architectures

The operator currently supports the following architectures:
- **amd64** (x86_64) - Standard Intel/AMD 64-bit processors
- **s390x** - IBM Z mainframe architecture

## Prerequisites

### Docker BuildKit

Multi-architecture builds require Docker BuildKit, which provides the `TARGETARCH` build argument automatically.

**Enable BuildKit:**
```bash
# Temporary (current session only)
export DOCKER_BUILDKIT=1

# Permanent (add to ~/.bashrc or ~/.zshrc)
echo 'export DOCKER_BUILDKIT=1' >> ~/.bashrc
```

**Verify BuildKit is enabled:**
```bash
docker buildx version
```

## Building Images

### Single Architecture Build

Build for your current platform (automatically detected):

```bash
# Using Makefile (BuildKit enabled automatically)
make docker-build

# Or manually with Docker
DOCKER_BUILDKIT=1 docker build --platform linux/amd64 -t ${IMG} .
```

Build for a specific architecture:

```bash
# For amd64
DOCKER_BUILDKIT=1 docker build --platform linux/amd64 -t myimage:amd64 .

# For s390x
DOCKER_BUILDKIT=1 docker build --platform linux/s390x -t myimage:s390x .
```

### Multi-Architecture Build

Build for multiple architectures simultaneously using `docker buildx`:

```bash
# Create a new builder instance (one-time setup)
docker buildx create --name multiarch --use

# Build and push for multiple platforms
docker buildx build \
  --platform linux/amd64,linux/s390x \
  --tag ${IMAGE_REGISTRY}/${OPERATOR_NAME}:${VERSION} \
  --push \
  .
```

**Note:** Multi-arch builds with `buildx` require pushing to a registry. Local multi-arch images are not supported.

## Architecture Naming Conventions

Different tools use different architecture naming conventions:

| System Architecture | Docker/BuildKit | Go (GOARCH) | Protobuf |
|---------------------|-----------------|-------------|----------|
| x86_64              | amd64           | amd64       | x86_64   |
| s390x               | s390x           | s390x       | s390_64  |

The build system handles these mappings automatically.

## Troubleshooting

### Error: TARGETARCH not set

**Symptom:**
```
ERROR: TARGETARCH not set. Use --platform flag or enable BuildKit.
```

**Cause:** Building without BuildKit or without specifying a platform.

**Solutions:**

1. Enable BuildKit:
   ```bash
   export DOCKER_BUILDKIT=1
   docker build -t myimage .
   ```

2. Specify platform explicitly:
   ```bash
   docker build --platform linux/amd64 -t myimage .
   ```

3. Use the Makefile (BuildKit enabled automatically):
   ```bash
   make docker-build
   ```

### Error: Unsupported architecture

**Symptom:**
```
ERROR: Unsupported architecture: arm64. Only amd64 and s390x are supported.
```

**Cause:** Attempting to build for an unsupported architecture.

**Solution:** Currently, only amd64 and s390x are supported. To add support for additional architectures:

1. Update the Dockerfile case statement (around line 10)
2. Update the Makefile architecture mappings
3. Ensure Go toolchain supports the target architecture
4. Test the build on the target platform

### Build fails during Go download

**Symptom:** Error downloading Go for the target architecture.

**Cause:** The Go version or architecture combination may not be available.

**Solution:**
1. Check available Go versions at https://go.dev/dl/
2. Verify the architecture is supported by Go
3. Update `go.mod` if necessary

## Bundle and Catalog Images

Bundle and catalog images are architecture-independent (they only contain YAML manifests):

```bash
# Build bundle
make bundle-build

# Build catalog
make catalog-build
```

These can be built on any architecture and will work across all platforms.

## Additional Resources

- [Docker BuildKit Documentation](https://docs.docker.com/build/buildkit/)
- [Docker Buildx Documentation](https://docs.docker.com/buildx/working-with-buildx/)
- [Go Cross Compilation](https://go.dev/doc/install/source#environment)
- [Operator SDK Multi-Arch Guide](https://sdk.operatorframework.io/docs/building-operators/golang/references/multi-arch/)

## Support

For issues or questions about multi-architecture builds:
1. Check this guide first
2. Review the [Dockerfile](Dockerfile) and [Makefile](Makefile) for implementation details
3. Open an issue in the project repository with:
   - Your architecture (`uname -m`)
   - Docker version (`docker version`)
   - BuildKit status (`docker buildx version`)
   - Complete error message