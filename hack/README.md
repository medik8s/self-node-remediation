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

The script creates or reuses the `medik8s-ci` Kind cluster with an HA topology (3 control-plane nodes and 3 workers), starts the local registry, builds the SNR images, deploys the operators, and runs the E2E tests.

If an older single-control-plane cluster already exists, recreate it explicitly:

```bash
CONTAINER_TOOL=podman-machine ./hack/local-run.sh --recreate-cluster
```

The local reboot watcher waits 90 seconds after SNR reaches its pre-reboot state. This gives the API-check and peer-health paths time to record their expected log messages. Override it when needed with `MEDIK8S_REBOOT_DELAY`.

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

## Monitor an E2E run

The runner writes its output to `/tmp/test`. Follow it from another host
terminal with:

```bash
tail -f /tmp/test
```

Useful cluster views while a test is running are:

```bash
kubectl --context kind-medik8s-ci get nodes -L remediation.medik8s.io/self-node-remediation
kubectl --context kind-medik8s-ci get nodes -o wide
kubectl --context kind-medik8s-ci get pods -A
kubectl --context kind-medik8s-ci get snr -A -o wide
```

During a remediation test, expect this sequence:

1. The test creates an SNR or blocks API connectivity on the selected worker.
2. SNR adds its remediation taint and transitions toward pre-reboot.
3. The Kind reboot watcher waits 90 seconds, then restarts the node
   container to simulate a reboot. This delay allows peer-health and API
   connectivity log messages to be recorded first.
4. The test waits for the container start-time marker to change and verifies
   that remediation taints are removed.

The first two remediation cases should therefore take about 2 minutes 40
seconds each with the default watcher delay. Negative tests intentionally
wait longer; the healthy-node/no-SNR case uses a 10-minute no-reboot
consistency window. A successful run ends with `Ran 6 of 6 Specs` and a
`4 Passed` or better result, depending on the selected test filters.

To inspect only the final test result or failures:

```bash
KIND_EXPERIMENTAL_PROVIDER=podman \
kind export logs --name medik8s-ci /tmp/kind-logs
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
