# AGENTS.md — Self Node Remediation Operator

> **IMPORTANT — read this first.** Before making any changes in this repository, you MUST
> read the medik8s **common agent guide**, the **OFFICIAL guidance** for all medik8s
> operators: **https://github.com/medik8s/.github/blob/main/AGENTS.md** . It is
> authoritative project guidance and must not be ignored.

## Medik8s context

SNR is a remediation **provider** in the [medik8s](https://medik8s.io) family. The orchestrator is **Node Healthcheck Operator (NHC)**: it detects unhealthy nodes and creates a `SelfNodeRemediation` CR; SNR then remediates.

SNR is the primary remediator for clusters without BMC/IPMI power fencing. It uses peer-consensus over gRPC to decide whether a node has truly lost API connectivity, then self-fences by triggering a reboot through a hardware watchdog or software reboot.

## How SNR works

Each SNR agent pod (DaemonSet) periodically checks API server connectivity. After the local API check fails past the error-count threshold, the agent asks its peers (other SNR agents) over gRPC whether a `SelfNodeRemediation` CR exists for this node. Each peer answers one of:

- **Healthy** — the peer reached the API and found no SNR CR for this node.
- **Unhealthy** — the peer reached the API and found an SNR CR for this node (NHC created it).
- **ApiError** — the peer itself cannot reach the API.
- *(no response)* — the peer is unreachable.

The node then decides whether to self-fence (reboot via watchdog). It reboots whenever it is considered unhealthy:

1. **API reachable** — healthy; no action (the peer check only runs after API failure).
2. **Any peer reports Unhealthy** — an SNR CR exists for this node; **self-fence**.
3. **Any peer reports Healthy** — considered healthy; no reboot.
4. **> 50% of reachable peers report ApiError** — assumed control-plane / API-wide outage; **do not reboot** (rebooting would not help).
5. **Too few peers to ask** (fewer than `MinPeersForRemediation`) — cannot get a reliable second opinion; **do not reboot**.
6. **No peer responds at all** (node is isolated) — **self-fence** once `MaxTimeForNoPeersResponse` elapses, so the node's at-most-one workloads can safely reschedule. Short blips are ridden out by the error-count and no-response grace windows.

Control-plane nodes additionally re-verify peer reachability even when the local `readyz` passes, because a network-isolated API server can still return 200 (it does not check etcd connectivity).

The local watchdog (softdog or hardware BMC watchdog) is armed at startup; SNR feeds it periodically. If the process stops feeding (due to reboot decision or crash), the kernel triggers a reboot after the watchdog timeout (~60 s for softdog on EC2).

## Build & test

```bash
# Unit tests (also runs generate, fmt, vet, imports, go-verify)
make test

# Build operator binary
make build

# Build container image
make docker-build

# Regenerate CRDs + RBAC + protobuf/gRPC after API changes
make manifests generate   # generate also runs protoc

# Format + vet
make fmt vet
make fix-imports

# e2e tests (KUBECONFIG must point at a cluster with SNR already deployed)
make e2e-test
```

> `make test` also runs `go-verify` (tidy + vendor) — do not skip it before a PR.  
> `make generate` also regenerates protobuf/gRPC stubs — run it after changing `.proto` files.

## Local development & testing

Follow the shared workflow in the
[common agent guide](https://github.com/medik8s/.github/blob/main/AGENTS.md) — it documents
the standardized `dev-*` make targets (`dev-setup`, `dev-deploy`, `dev-redeploy`,
`dev-undeploy`, `dev-describe`, `dev-help`, …) provided by `medik8s/tools` (`dev/dev.mk`).

**To develop and test against a real OpenShift / Kubernetes cluster**: 
`export SKIP_KIND=true` before the `dev-*` targets; images are pushed to `ttl.sh`. 

Github CI workflow runs on a Kind cluster. A special reboot watcher script handles reboots of the nodes on Kind.

**SNR manifests use `${IMG}` placeholders expanded by `envsubst`, so
always deploy via `make dev-deploy` / `make dev-redeploy` rather than applying
`kustomize build` output directly.**

## Key design constraints

- **Watchdog must be armed at startup** and fed continuously. If SNR crashes without triggering a graceful shutdown, the watchdog fires after its timeout — this is intentional (fail-safe reboot).
- **Softdog fires on EC2**: when the agent stops feeding, the kernel triggers reboot in ~60 s. Use `journalctl --list-boots` to confirm reboot happened.
- **gRPC peer communication uses mTLS** (mutual TLS). Cert rotation is managed by the `certificates` package — do not bypass it.
- The `SelfNodeRemediationConfig` CR carries cluster-wide defaults (watchdog path, safe-time-to-assume-node-rebooted, peer timeout, etc.). Most fields have sane defaults; do not change them without understanding the consensus timing implications.
- **`safeTimeToAssumeNodeRebooted`** must be longer than the watchdog timeout + reboot time; if it is too short, NHC may allow workloads to reschedule before the node has actually rebooted, risking dual-writer data corruption.

## Security

- Agent DaemonSet runs privileged (needs `/dev/watchdog*` access and ability to trigger reboot).
- Peer gRPC is mTLS-authenticated — SNR manages its own CA and leaf certs.
