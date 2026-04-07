# Building s390x Images - Detailed Guide

This guide provides step-by-step instructions for building Self Node Remediation Operator images for the s390x (IBM Z) architecture and pushing them to Quay.io (including private repositories).

## Table of Contents
1. [Prerequisites](#prerequisites)
2. [Setting Up Quay.io Repository](#setting-up-quayio-repository)
3. [Option 1: Native Build on s390x Hardware](#option-1-native-build-on-s390x-hardware)
4. [Option 2: Cross-Platform Build using Docker Buildx](#option-2-cross-platform-build-using-docker-buildx)
5. [Option 3: Multi-Arch Build (amd64 + s390x)](#option-3-multi-arch-build-amd64--s390x)
6. [Pushing to Private Quay.io Repository](#pushing-to-private-quayio-repository)
7. [Verification](#verification)
8. [Troubleshooting](#troubleshooting)

---

## Prerequisites

### Common Requirements
- Git
- Go (version specified in `go.mod`)
- Access to the repository
- Quay.io account
- Container registry credentials

### For Cross-Platform Builds (Options 2 & 3)
- Docker with Buildx support (Docker 19.03+)
- QEMU for emulation
- Sufficient disk space (builds can be large, 10GB+ recommended)

---

## Setting Up Quay.io Repository

### Step 1: Create a Quay.io Account

1. Go to [https://quay.io](https://quay.io)
2. Sign up or log in
3. Verify your email address

### Step 2: Create a Repository

#### Create Repository via Web UI

1. Log in to Quay.io
2. Click the **"+"** icon in the top right, then **"New Repository"**
3. Enter repository details:
   - **Repository Name**: `self-node-remediation-operator`
   - **Description**: (optional) "Self Node Remediation Operator for Kubernetes"
   - **Visibility**:
     - **Public** - Anyone can pull the image
     - **Private** - Only authorized users can pull (requires authentication)
4. Click **"Create Public Repository"** or **"Create Private Repository"**

**Note:** You must create the repository before pushing images. Quay.io does not auto-create repositories on push by default.

### Step 3: Get Your Repository URL

Your repository URL will be:
```
quay.io/YOUR_USERNAME/self-node-remediation-operator
```

Or for organizations:
```
quay.io/YOUR_ORG/self-node-remediation-operator
```

---

## Option 1: Native Build on s390x Hardware

This is the **fastest and most reliable** method if you have access to s390x hardware.

### Step 1: Access s390x System

Connect to your s390x machine:
```bash
ssh user@s390x-machine
```

### Step 2: Install Dependencies

```bash
# For RHEL/CentOS/Fedora
sudo dnf install -y git jq podman

# For Ubuntu/Debian
sudo apt-get update
sudo apt-get install -y git jq podman

# For SLES (SUSE Linux Enterprise Server)
sudo zypper install -y git jq podman
```

### Step 3: Clone the Repository

```bash
git clone https://github.com/medik8s/self-node-remediation.git
cd self-node-remediation
```

### Step 4: Login to Quay.io

```bash
# Login with Podman
podman login quay.io

# You'll be prompted for:
# Username: your_username
# Password: your_password_or_token
```

**For Private Repositories**, you can also use a robot account or encrypted password (see [Pushing to Private Quay.io Repository](#pushing-to-private-quayio-repository) section).

### Step 5: Build the Image

```bash
# Set your image name with s390x tag (replace YOUR_USERNAME)
export IMAGE_REGISTRY=quay.io/YOUR_USERNAME
export IMG=${IMAGE_REGISTRY}/self-node-remediation-operator:s390x

# Build the operator image
make docker-build

# Check the built image
podman images | grep self-node-remediation-operator
```

### Step 6: Push the Image to Quay.io

```bash
# Push to Quay.io with s390x tag
make docker-push

# Or push directly with podman
podman push ${IMG}
```

### Step 7: Build and Push Bundle Image

```bash
# Generate bundle manifests (use default version)
make bundle

# Build bundle image with s390x tag
export BUNDLE_IMG=${IMAGE_REGISTRY}/self-node-remediation-operator-bundle:s390x
podman build -f bundle.Dockerfile -t ${BUNDLE_IMG} .

# Push bundle image
podman push ${BUNDLE_IMG}
```

### Step 8: Verify on Quay.io

1. Go to `https://quay.io/repository/YOUR_USERNAME/self-node-remediation-operator`
2. Check the **Tags** tab
3. Verify the `v${VERSION}` tag is present
4. Check the architecture in tag details (should show s390x)

---

## Option 2: Cross-Platform Build using Docker Buildx

This method allows building s390x images from an amd64 machine using emulation.

### Step 1: Install Docker and Buildx

```bash
# Verify Docker version (19.03+)
docker version

# Check if buildx is available
docker buildx version
```

### Step 2: Set Up QEMU for Cross-Platform Emulation

```bash
# Install QEMU static binaries
docker run --rm --privileged multiarch/qemu-user-static --reset -p yes

# Verify QEMU is registered
docker run --rm --platform linux/s390x alpine uname -m
# Should output: s390x
```

### Step 3: Create a Buildx Builder

```bash
# Create a new builder instance
docker buildx create --name s390x-builder --driver docker-container --use

# Bootstrap the builder
docker buildx inspect --bootstrap

# List available platforms
docker buildx inspect --bootstrap | grep Platforms
# Should include: linux/s390x
```

### Step 4: Clone the Repository

```bash
git clone https://github.com/medik8s/self-node-remediation.git
cd self-node-remediation
```

### Step 5: Login to Quay.io

```bash
# Login with Docker
docker login quay.io

# Enter your credentials when prompted
```

### Step 6: Build for s390x and Push to Quay.io

```bash
# Set your image name with s390x tag (replace YOUR_USERNAME)
export IMAGE_REGISTRY=quay.io/YOUR_USERNAME
export IMG=${IMAGE_REGISTRY}/self-node-remediation-operator:s390x

# Build and push s390x image
docker buildx build \
  --platform linux/s390x \
  -t ${IMG} \
  --push \
  .
```

**Note:** This build will take 30-60 minutes due to QEMU emulation.

### Step 7: Build and Push Bundle Image

```bash
# Generate bundle manifests (use default version)
make bundle

# Build and push bundle for s390x
export BUNDLE_IMG=${IMAGE_REGISTRY}/self-node-remediation-operator-bundle:s390x
docker buildx build \
  --platform linux/s390x \
  -f bundle.Dockerfile \
  -t ${BUNDLE_IMG} \
  --push \
  .
```

---

## Option 3: Multi-Arch Build (amd64 + s390x)

Build a single image manifest that supports both architectures.

### Step 1: Set Up Environment

Follow Steps 1-5 from Option 2.

### Step 2: Build Multi-Arch Image and Push to Quay.io

```bash
# Set your image name (replace YOUR_USERNAME)
export IMAGE_REGISTRY=quay.io/YOUR_USERNAME
export IMG=${IMAGE_REGISTRY}/self-node-remediation-operator:latest

# Build and push multi-arch image (amd64 + s390x)
docker buildx build \
  --platform linux/amd64,linux/s390x \
  -t ${IMG} \
  --push \
  .
```

This creates a single image tag that works on both amd64 and s390x systems.

### Step 3: Build and Push Multi-Arch Bundle

```bash
# Generate bundle manifests (use default version)
make bundle

# Build and push multi-arch bundle
export BUNDLE_IMG=${IMAGE_REGISTRY}/self-node-remediation-operator-bundle:latest
docker buildx build \
  --platform linux/amd64,linux/s390x \
  -f bundle.Dockerfile \
  -t ${BUNDLE_IMG} \
  --push \
  .
```

---

## Pushing to Private Quay.io Repository

### Method 1: Using Username and Password

```bash
# Login interactively
docker login quay.io
# Or
podman login quay.io

# Enter your credentials when prompted
```

### Method 2: Using Robot Accounts (Recommended for Automation)

Robot accounts are service accounts that provide secure, token-based authentication.

#### Creating a Robot Account:

1. Go to your Quay.io repository
2. Click **Settings** → **Robot Accounts**
3. Click **"Create Robot Account"**
4. Enter a name (e.g., `builder`)
5. Set permissions:
   - **Write** - For pushing images
   - **Read** - For pulling images
6. Click **"Create Robot Account"**
7. **Copy the token** (you won't see it again!)

#### Using Robot Account:

```bash
# Login with robot account
docker login quay.io
# Username: YOUR_USERNAME+builder
# Password: <paste the robot token>

# Or use non-interactive login
echo "YOUR_ROBOT_TOKEN" | docker login quay.io -u YOUR_USERNAME+builder --password-stdin
```

### Method 3: Using Encrypted Password (CLI Token)

1. Go to **Account Settings** → **CLI Password**
2. Click **"Generate Encrypted Password"**
3. Select **Docker Login**
4. Copy the provided command and run it:

```bash
docker login -u="YOUR_USERNAME" -p="ENCRYPTED_PASSWORD" quay.io
```

### Method 4: Using Docker Config File

Store credentials in Docker config file:

```bash
# Create/edit ~/.docker/config.json
mkdir -p ~/.docker

# Add credentials (base64 encoded)
cat > ~/.docker/config.json <<EOF
{
  "auths": {
    "quay.io": {
      "auth": "BASE64_ENCODED_USERNAME:PASSWORD"
    }
  }
}
EOF

# To generate base64 encoded credentials:
echo -n "YOUR_USERNAME:YOUR_PASSWORD" | base64
```

### Setting Repository Visibility

#### Make Repository Private:

1. Go to your repository on Quay.io
2. Click **Settings**
3. Under **Repository Visibility**, select **Private**
4. Click **Save**

#### Grant Access to Private Repository:

1. Go to **Settings** → **User and Robot Permissions**
2. Click **"Add User/Robot"**
3. Enter username or robot account
4. Set permission level:
   - **Read** - Can pull images
   - **Write** - Can push images
   - **Admin** - Full control
5. Click **"Add Permission"**

### Building and Pushing to Private Repository

```bash
# 1. Login to Quay.io
docker login quay.io

# 2. Set your private repository
export IMAGE_REGISTRY=quay.io/YOUR_USERNAME
export IMG=${IMAGE_REGISTRY}/self-node-remediation-operator:s390x

# 3. Build and push
# For native s390x build:
make docker-build docker-push

# For cross-platform build (s390x only):
docker buildx build --platform linux/s390x -t ${IMG} --push .

# For multi-arch build (amd64 + s390x) use 'latest' tag:
export IMG=${IMAGE_REGISTRY}/self-node-remediation-operator:latest
docker buildx build --platform linux/amd64,linux/s390x -t ${IMG} --push .
```

### Pulling from Private Repository

```bash
# Login first
docker login quay.io

# Pull the image
docker pull ${IMG}

# Or in Kubernetes, create an image pull secret:
kubectl create secret docker-registry quay-secret \
  --docker-server=quay.io \
  --docker-username=YOUR_USERNAME \
  --docker-password=YOUR_PASSWORD \
  --docker-email=YOUR_EMAIL

# Use the secret in your deployment:
# imagePullSecrets:
#   - name: quay-secret
```

---

## Verification

### Verify Image Architecture

```bash
# Using Docker
docker inspect ${IMG} | grep Architecture

# Using Podman
podman inspect ${IMG} | grep Architecture

# Using manifest inspection (for multi-arch)
docker buildx imagetools inspect ${IMG}
```

### Verify on Quay.io Web UI

1. Go to `https://quay.io/repository/YOUR_USERNAME/self-node-remediation-operator`
2. Click on the **Tags** tab
3. Click on your tag (e.g., `v0.1.0`)
4. Check **Manifest** section for architecture details
5. For multi-arch images, you'll see multiple manifests listed

### Test the Image

```bash
# Run a test container on s390x
docker run --rm --platform linux/s390x \
  ${IMG} \
  --version

# Or with Podman
podman run --rm --arch s390x \
  ${IMG} \
  --version
```

---

## Troubleshooting

### Issue: Authentication Failed

**Symptom:** `unauthorized: authentication required` or `denied: requested access to the resource is denied`

**Solutions:**

```bash
# 1. Re-login to Quay.io
docker login quay.io

# 2. Check if credentials are correct
cat ~/.docker/config.json

# 3. For private repos, ensure you have access
# Check repository permissions on Quay.io web UI

# 4. Try using robot account instead
docker login quay.io -u YOUR_USERNAME+robotname
```

### Issue: Repository Not Found

**Symptom:** `repository does not exist or may require 'docker login'`

**Solutions:**

1. **Create the repository first** on Quay.io web UI
2. **Enable auto-create** in Account Settings (Settings → Default Permissions)
3. **Check repository name** - ensure it matches exactly
4. **Check organization vs user** - use correct namespace

### Issue: QEMU Not Working

**Symptom:** `exec format error` when trying to run s390x containers

**Solution:**
```bash
# Re-register QEMU
docker run --rm --privileged multiarch/qemu-user-static --reset -p yes

# Verify registration
ls -la /proc/sys/fs/binfmt_misc/ | grep qemu

# Check if s390x is supported
docker run --rm --platform linux/s390x alpine uname -m
```

### Issue: Slow Build Times

**Symptom:** Cross-platform builds taking very long (30+ minutes)

**Solutions:**
1. **Use native s390x hardware** (Option 1) - Fastest method
2. **Build only when necessary** - Cache layers effectively
3. **Use CI/CD** - Let automated systems handle slow builds
4. **Increase Docker resources** - Allocate more CPU/RAM in Docker Desktop settings

### Issue: Out of Disk Space

**Symptom:** `no space left on device`

**Solution:**
```bash
# Clean up Docker/Podman
docker system prune -a
podman system prune -a

# Remove old builders
docker buildx prune -a

# Check disk usage
df -h
docker system df
```

### Issue: Private Repository Pull Fails in Kubernetes

**Symptom:** `ImagePullBackOff` or `ErrImagePull`

**Solution:**
```bash
# Create image pull secret
kubectl create secret docker-registry quay-secret \
  --docker-server=quay.io \
  --docker-username=YOUR_USERNAME \
  --docker-password=YOUR_PASSWORD \
  -n YOUR_NAMESPACE

# Add to deployment
kubectl patch deployment self-node-remediation-controller-manager \
  -n YOUR_NAMESPACE \
  -p '{"spec":{"template":{"spec":{"imagePullSecrets":[{"name":"quay-secret"}]}}}}'
```

### Issue: Makefile Targets Fail on s390x

**Symptom:** `operator-sdk`, `opm`, or `protoc` download fails

**Solution:**
The Makefile has been updated to support s390x. Ensure you're using the latest version:
```bash
# Pull latest changes
git pull origin main

# The Makefile will automatically detect s390x and download correct binaries
make operator-sdk
make opm
make protoc
```

---

## Build Time Comparison

| Method | Hardware | Typical Build Time | Pros | Cons |
|--------|----------|-------------------|------|------|
| Native s390x | IBM Z | 5-10 minutes | Fast, reliable | Requires s390x access |
| Cross-build (QEMU) | amd64 | 30-60 minutes | No special hardware | Very slow |
| CI/CD Pipeline | GitHub Actions | 30-60 minutes | Automated | Requires setup |

---

## Best Practices

### For Building

1. **Use Native Builds When Possible**
   - If you have s390x hardware, use Option 1
   - Much faster and more reliable

2. **Cache Effectively**
   - Docker buildx caches layers
   - Reuse builders between builds
   - Don't prune unnecessarily

3. **Test Before Pushing**
   - Build locally first
   - Verify the image works
   - Then push to registry

4. **Use Specific Version Tags**
   - Don't overwrite `latest` during testing
   - Use semantic versioning: `v1.0.0`
   - Tag with architecture if needed: `v1.0.0-s390x`

### For Quay.io

1. **Use Robot Accounts for Automation**
   - More secure than user passwords
   - Can be revoked independently
   - Scoped to specific repositories

2. **Use Specific Tags**
   - Don't overwrite `latest` during testing
   - Use version tags: `v1.0.0`
   - Use architecture tags if building separately: `v1.0.0-s390x`

3. **Set Appropriate Permissions**
   - Private repos for development
   - Public repos for releases
   - Use teams for organization access

4. **Enable Vulnerability Scanning**
   - Quay.io provides Clair security scanning
   - Enable in repository settings
   - Review scan results regularly

5. **Set Up Notifications**
   - Configure email notifications for builds
   - Set up webhooks for CI/CD integration
   - Monitor repository activity

---

## Quick Reference Commands

### Building

```bash
# Set variables
export IMAGE_REGISTRY=quay.io/YOUR_USERNAME
export VERSION=0.1.0
export IMG=${IMAGE_REGISTRY}/self-node-remediation-operator:v${VERSION}
export BUNDLE_IMG=${IMAGE_REGISTRY}/self-node-remediation-operator-bundle:v${VERSION}

# Native s390x build
make docker-build docker-push

# Cross-build s390x only
docker buildx build --platform linux/s390x -t ${IMG} --push .

# Multi-arch build (amd64 + s390x)
docker buildx build --platform linux/amd64,linux/s390x -t ${IMG} --push .

# Build bundle
make bundle VERSION=${VERSION}
docker buildx build --platform linux/amd64,linux/s390x -f bundle.Dockerfile -t ${BUNDLE_IMG} --push .
```

### Quay.io Authentication

```bash
# Interactive login
docker login quay.io

# Robot account login
docker login quay.io -u YOUR_USERNAME+robotname

# Non-interactive login
echo "TOKEN" | docker login quay.io -u YOUR_USERNAME --password-stdin
```

### Verification

```bash
# Verify architecture
docker buildx imagetools inspect ${IMG}

# Test s390x image
docker run --rm --platform linux/s390x ${IMG} --version

# Check manifest
docker manifest inspect ${IMG}
```

### Kubernetes Image Pull Secret

```bash
# Create secret
kubectl create secret docker-registry quay-secret \
  --docker-server=quay.io \
  --docker-username=YOUR_USERNAME \
  --docker-password=YOUR_PASSWORD \
  -n openshift-workload-availability

# Use in pod spec
# imagePullSecrets:
#   - name: quay-secret
```

---

## Complete Build Script Example

Here's a complete script for building and pushing to your private Quay.io repository:

```bash
#!/bin/bash
set -e

# ============================================
# Configuration - UPDATE THESE VALUES
# ============================================
export IMAGE_REGISTRY=quay.io/YOUR_USERNAME

# Use s390x as the tag (no version number)
export IMG=${IMAGE_REGISTRY}/self-node-remediation-operator:s390x
export BUNDLE_IMG=${IMAGE_REGISTRY}/self-node-remediation-operator-bundle:s390x
export CATALOG_IMG=${IMAGE_REGISTRY}/self-node-remediation-operator-catalog:s390x

echo "============================================"
echo "Building Self Node Remediation Operator for s390x"
echo "============================================"
echo "Registry: ${IMAGE_REGISTRY}"
echo "Operator Image: ${IMG}"
echo "Bundle Image: ${BUNDLE_IMG}"
echo "Catalog Image: ${CATALOG_IMG}"
echo "============================================"

# Login to quay.io
echo "Step 1: Logging in to quay.io..."
docker login quay.io

# Setup buildx for multi-arch
echo "Step 2: Setting up buildx for multi-arch builds..."
docker buildx create --name multiarch --driver docker-container --use 2>/dev/null || docker buildx use multiarch
docker buildx inspect --bootstrap

# Build and push operator image for s390x only
echo "Step 3: Building and pushing operator image for s390x..."
docker buildx build \
  --platform linux/s390x \
  -t ${IMG} \
  --push \
  .

echo "✅ Operator image pushed: ${IMG}"

# Generate bundle
echo "Step 4: Generating bundle manifests..."
make bundle

# Build and push bundle image for s390x
echo "Step 5: Building and pushing bundle image for s390x..."
docker buildx build \
  --platform linux/s390x \
  -f bundle.Dockerfile \
  -t ${BUNDLE_IMG} \
  --push \
  .

echo "✅ Bundle image pushed: ${BUNDLE_IMG}"

# Build and push catalog for s390x
echo "Step 6: Building and pushing catalog for s390x..."
make catalog-build CATALOG_IMG=${CATALOG_IMG}
docker push ${CATALOG_IMG}

echo "✅ Catalog image pushed: ${CATALOG_IMG}"

# Verify multi-arch support
echo "============================================"
echo "Verifying multi-arch support..."
echo "============================================"
docker buildx imagetools inspect ${IMG}

echo ""
echo "============================================"
echo "✅ Build Complete!"
echo "============================================"
echo "Operator: ${IMG}"
echo "Bundle: ${BUNDLE_IMG}"
echo "Catalog: ${CATALOG_IMG}"
echo ""
echo "To deploy:"
echo "  make deploy IMG=${IMG}"
echo "============================================"
```

Save this as `build-and-push.sh`, make it executable, and run:
```bash
chmod +x build-and-push.sh
./build-and-push.sh
```

---

## Additional Resources

- [Quay.io Documentation](https://docs.quay.io/)
- [Docker Buildx Documentation](https://docs.docker.com/buildx/working-with-buildx/)
- [QEMU User Emulation](https://www.qemu.org/docs/master/user/main.html)
- [Multi-Architecture Images](https://docs.docker.com/build/building/multi-platform/)
- [Kubernetes Image Pull Secrets](https://kubernetes.io/docs/tasks/configure-pod-container/pull-image-private-registry/)
- [Self Node Remediation Documentation](https://www.medik8s.io/)
- [Operator SDK Documentation](https://sdk.operatorframework.io/)