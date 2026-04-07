# Multi-Architecture Support

This document describes the multi-architecture support added to the Self Node Remediation operator.

## Supported Architectures

The operator now supports the following architectures:
- **amd64** (x86_64)
- **s390x** (IBM Z)

## Changes Made

### 1. Dockerfile Updates
- Added `ARG TARGETARCH` and `ARG TARGETOS` to support Docker buildx multi-platform builds
- Modified Go binary download logic to dynamically select the correct architecture
- Added architecture mapping to handle different naming conventions

### 2. Makefile Updates
- Updated `protoc` target to detect and download the correct architecture binary
- Updated `operator-sdk` target to detect and download the correct architecture binary
- Updated `opm` target to detect and download the correct architecture binary
- All tools now use `uname -m` to detect the host architecture and map it appropriately

## Building Multi-Architecture Images

### Prerequisites
1. Docker with buildx support (Docker 19.03+)
2. QEMU for cross-platform emulation (if building on a different architecture)

### Setup Docker Buildx
```bash
# Create a new builder instance
docker buildx create --name multiarch --driver docker-container --use

# Bootstrap the builder
docker buildx inspect --bootstrap
```

### Build Multi-Architecture Operator Image
```bash
# Build for amd64 and s390x
docker buildx build \
  --platform linux/amd64,linux/s390x \
  -t ${IMG} \
  --push \
  .
```

### Build for Specific Architecture
```bash
# Build for s390x only
docker buildx build \
  --platform linux/s390x \
  -t ${IMG} \
  --load \
  .
```

### Build Bundle Image (Multi-Arch)
```bash
# Build bundle for amd64 and s390x
docker buildx build \
  --platform linux/amd64,linux/s390x \
  -f bundle.Dockerfile \
  -t ${BUNDLE_IMG} \
  --push \
  .
```

## Using the Makefile

The Makefile targets will automatically detect your host architecture when downloading tools:

```bash
# These will work on any supported architecture
make operator-sdk
make opm
make protoc
```

## Building and Pushing to Your Private Quay.io Repository

### Step 1: Login to Quay.io

```bash
# Login to quay.io registry
docker login quay.io

# You will be prompted for:
# Username: your-quay-username
# Password: your-quay-password (or robot token)
```

**Note:** For automated builds, you can use a robot account token from Quay.io:
1. Go to https://quay.io/organization/YOUR_ORG?tab=robots (or your user settings)
2. Create a robot account with write permissions to your repository
3. Use the robot token for authentication

### Step 2: Set Your Private Repository Variables

```bash
# Replace with your private quay.io repository details
export IMAGE_REGISTRY=quay.io/your-username  # or quay.io/your-org
export VERSION=0.1.0
export IMG=${IMAGE_REGISTRY}/self-node-remediation-operator:v${VERSION}
export BUNDLE_IMG=${IMAGE_REGISTRY}/self-node-remediation-operator-bundle:v${VERSION}

# Example:
# export IMAGE_REGISTRY=quay.io/johndoe
# export IMG=quay.io/johndoe/self-node-remediation-operator:v0.1.0
```

### Step 3: Build and Push Multi-Arch Images

#### Option A: Build on s390x Hardware (Native Build)

If you're building directly on s390x hardware:

```bash
# Build the operator image for s390x
docker build -t ${IMG} .

# Push to your private quay.io repository
docker push ${IMG}

# Build and push bundle
make bundle VERSION=${VERSION}
docker build -f bundle.Dockerfile -t ${BUNDLE_IMG} .
docker push ${BUNDLE_IMG}
```

#### Option B: Build Multi-Arch with Docker Buildx (Recommended)

This approach builds both amd64 and s390x in one command and pushes to your private repo:

```bash
# Create and use a buildx builder (one-time setup)
docker buildx create --name multiarch --driver docker-container --use
docker buildx inspect --bootstrap

# Build and push operator image for both architectures to your private repo
docker buildx build \
  --platform linux/amd64,linux/s390x \
  -t ${IMG} \
  --push \
  .

# Generate bundle manifests
make bundle VERSION=${VERSION}

# Build and push bundle image for both architectures
docker buildx build \
  --platform linux/amd64,linux/s390x \
  -f bundle.Dockerfile \
  -t ${BUNDLE_IMG} \
  --push \
  .
```

#### Option C: Build Separately on Different Machines and Create Manifest

If you have separate amd64 and s390x machines:

```bash
# On amd64 machine:
docker build -t ${IMG}-amd64 .
docker push ${IMG}-amd64

# On s390x machine:
docker build -t ${IMG}-s390x .
docker push ${IMG}-s390x

# On any machine with docker CLI, create and push manifest:
docker manifest create ${IMG} \
  ${IMG}-amd64 \
  ${IMG}-s390x

docker manifest push ${IMG}
```

### Step 4: Verify Multi-Arch Images in Your Private Repo

```bash
# Inspect the manifest to verify both architectures
docker manifest inspect ${IMG}

# You should see entries for both linux/amd64 and linux/s390x
# Example output:
# {
#   "manifests": [
#     {
#       "platform": {
#         "architecture": "amd64",
#         "os": "linux"
#       }
#     },
#     {
#       "platform": {
#         "architecture": "s390x",
#         "os": "linux"
#       }
#     }
#   ]
# }
```

### Step 5: Make Your Repository Public or Private

By default, new repositories on Quay.io are private. To manage visibility:

1. Go to https://quay.io/repository/your-username/self-node-remediation-operator?tab=settings
2. Under "Repository Visibility", choose:
   - **Private**: Only you and users you grant access can pull
   - **Public**: Anyone can pull the image

## Complete Build and Push Workflow for Private Repo

Here's a complete script for building and pushing to your private quay.io repository:

```bash
#!/bin/bash
set -e

# ============================================
# Configuration - UPDATE THESE VALUES
# ============================================
export IMAGE_REGISTRY=quay.io/your-username  # Change to your quay.io username or org
export VERSION=0.1.0
export PREVIOUS_VERSION=0.0.1

# Derived variables
export IMG=${IMAGE_REGISTRY}/self-node-remediation-operator:v${VERSION}
export BUNDLE_IMG=${IMAGE_REGISTRY}/self-node-remediation-operator-bundle:v${VERSION}
export CATALOG_IMG=${IMAGE_REGISTRY}/self-node-remediation-operator-catalog:v${VERSION}

echo "============================================"
echo "Building and Pushing to Private Quay.io Repo"
echo "============================================"
echo "Registry: ${IMAGE_REGISTRY}"
echo "Operator Image: ${IMG}"
echo "Bundle Image: ${BUNDLE_IMG}"
echo "============================================"

# Login to quay.io
echo "Step 1: Logging in to quay.io..."
docker login quay.io

# Setup buildx for multi-arch
echo "Step 2: Setting up buildx for multi-arch builds..."
docker buildx create --name multiarch --driver docker-container --use 2>/dev/null || docker buildx use multiarch
docker buildx inspect --bootstrap

# Build and push operator image
echo "Step 3: Building and pushing operator image for amd64 and s390x..."
docker buildx build \
  --platform linux/amd64,linux/s390x \
  -t ${IMG} \
  --push \
  .

echo "✅ Operator image pushed: ${IMG}"

# Generate bundle
echo "Step 4: Generating bundle manifests..."
make bundle VERSION=${VERSION} PREVIOUS_VERSION=${PREVIOUS_VERSION}

# Build and push bundle image
echo "Step 5: Building and pushing bundle image for amd64 and s390x..."
docker buildx build \
  --platform linux/amd64,linux/s390x \
  -f bundle.Dockerfile \
  -t ${BUNDLE_IMG} \
  --push \
  .

echo "✅ Bundle image pushed: ${BUNDLE_IMG}"

# Build and push catalog (optional)
echo "Step 6: Building and pushing catalog..."
make catalog-build CATALOG_IMG=${CATALOG_IMG}
docker push ${CATALOG_IMG}

echo "✅ Catalog image pushed: ${CATALOG_IMG}"

# Verify multi-arch support
echo "============================================"
echo "Verifying multi-arch support..."
echo "============================================"
docker manifest inspect ${IMG} | grep -E "architecture|os"

echo ""
echo "============================================"
echo "✅ All images pushed successfully to your private repo!"
echo "============================================"
echo "Operator: ${IMG}"
echo "Bundle: ${BUNDLE_IMG}"
echo "Catalog: ${CATALOG_IMG}"
echo ""
echo "To pull these images, users will need:"
echo "1. docker login quay.io"
echo "2. Access permissions to your private repository"
echo "============================================"
```

### Step 6: Grant Access to Your Private Repository (Optional)

If you want to share your private images with others:

1. Go to https://quay.io/repository/your-username/self-node-remediation-operator?tab=settings
2. Click on "User and Robot Permissions"
3. Add users or robot accounts with appropriate permissions (read, write, or admin)

## Testing on s390x

To test the operator on s390x:

1. **On an s390x machine:**
   ```bash
   # Set your private repo
   export IMG=quay.io/your-username/self-node-remediation-operator:v0.1.0
   
   # Build locally
   make docker-build
   
   # Or pull from your private repo
   docker pull ${IMG}
   ```

2. **Deploy to s390x cluster:**
   ```bash
   # Make sure you're logged in to quay.io
   docker login quay.io
   
   # Deploy using your private image
   make deploy IMG=${IMG}
   ```

## CI/CD Integration

For CI/CD pipelines, use Docker buildx to build and push multi-arch images to your private repo:

```bash
# Login to registry (use CI secrets)
echo "${QUAY_PASSWORD}" | docker login quay.io -u "${QUAY_USERNAME}" --password-stdin

# Build and push multi-arch images to your private repo
docker buildx build \
  --platform linux/amd64,linux/s390x \
  -t ${IMG} \
  --push \
  .
```

## Architecture Mapping

The following architecture mappings are used:

| `uname -m` | Docker TARGETARCH | Go GOARCH | Protoc Arch |
|------------|-------------------|-----------|-------------|
| x86_64     | amd64             | amd64     | x86_64      |
| s390x      | s390x             | s390x     | s390_64     |

## Troubleshooting

### Issue: Binary not found for architecture
**Solution:** Ensure the upstream project (operator-sdk, opm, protoc) provides binaries for your architecture. Check their release pages.

### Issue: QEMU emulation is slow
**Solution:** For best performance, build on native hardware or use a CI/CD system with native runners for each architecture.

### Issue: Go download fails for architecture
**Solution:** Verify that the Go version specified in `go.mod` has releases for your target architecture at https://go.dev/dl/

## References

- [Docker Buildx Documentation](https://docs.docker.com/buildx/working-with-buildx/)
- [Multi-platform Images](https://docs.docker.com/build/building/multi-platform/)
- [Go Downloads](https://go.dev/dl/)
- [Operator SDK Releases](https://github.com/operator-framework/operator-sdk/releases)
- [OPM Releases](https://github.com/operator-framework/operator-registry/releases)