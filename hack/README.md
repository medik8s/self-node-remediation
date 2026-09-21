# Local E2E tests with Podman machine

`local-run.sh` runs the Kind E2E workflow locally. With `CONTAINER_TOOL=podman-machine`, Kind and the test images run through a Podman virtual machine while `kubectl` remains on the host.

## One-time setup

On Fedora, install the Podman machine dependencies:

```bash
sudo dnf install podman podman-machine podman-gvproxy
```

Initialize the default machine once:

```bash
podman machine init podman-machine-default
```

The local runner starts the machine automatically, but it can also be started explicitly:

```bash
podman machine start podman-machine-default
```

If `podman machine init` reports that the machine already exists, continue with `podman machine start`.

## Start the tests

Run these commands from the repository root:

```bash
CONTAINER_TOOL=podman-machine ./hack/local-run.sh
```

The script creates or reuses the `medik8s-ci` Kind cluster, starts the local registry, builds the SNR images, deploys the operators, and runs the E2E tests.

For a different Podman machine:

```bash
PODMAN_MACHINE_NAME=snr-machine \
CONTAINER_TOOL=podman-machine ./hack/local-run.sh
```

The script uses `E2E_REBOOT_CHECK=container-start-time` by default because Kind worker nodes are containers sharing the Podman machine kernel. To use the Kubernetes node boot ID instead:

```bash
E2E_REBOOT_CHECK=boot-id \
CONTAINER_TOOL=podman-machine ./hack/local-run.sh
```

## Inspect the running cluster

Run `kubectl` from the host shell, not through `podman machine ssh`:

```bash
kubectl config use-context kind-medik8s-ci
kubectl get nodes -o wide
kubectl get pods -A
```

`podman machine ssh` is only needed to inspect the VM itself.

## Re-run after a failure

The script leaves the Kind cluster running after a failure so it can be inspected. Reuse it with:

```bash
CONTAINER_TOOL=podman-machine ./hack/local-run.sh --skip-setup
```

To reuse both the cluster and existing operator installations:

```bash
CONTAINER_TOOL=podman-machine \
./hack/local-run.sh --skip-setup --skip-build
```

## Clean up

Delete the Kind cluster and its local registry, while keeping the Podman machine:

```bash
CONTAINER_TOOL=podman-machine ./hack/local-run.sh --teardown
```

Stop the Podman machine when it is no longer needed:

```bash
podman machine stop podman-machine-default
```

To start fresh later:

```bash
podman machine start podman-machine-default
CONTAINER_TOOL=podman-machine ./hack/local-run.sh
```

Removing the machine is optional and deletes its VM data:

```bash
podman machine rm podman-machine-default
```

