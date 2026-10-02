# AGENTS.md — Self Node Remediation Operator

## Medik8s context

SNR is a remediation **provider** in the [medik8s](https://medik8s.io) family. The orchestrator is **Node Healthcheck Operator (NHC)**: it detects unhealthy nodes and creates a `SelfNodeRemediation` CR; SNR then remediates.

SNR is the primary remediator for clusters without BMC/IPMI power fencing. It uses peer-consensus over gRPC to decide whether a node has truly lost API connectivity, then self-fences by triggering a reboot through a hardware or software watchdog.

## How SNR works

Each SNR agent pod (DaemonSet) periodically checks API server connectivity. If the local node loses API connectivity, it asks peers (other SNR agents) whether they can see the API server:

- **Peer consensus**: if a quorum of peers confirms the API is reachable, the local node concludes it is isolated and self-fences (reboots via watchdog).
- **Control-plane scenario**: if peers also cannot reach the API, SNR does not reboot (the cluster itself may be degraded; rebooting would make it worse).

Four scenarios govern the decision:
1. **API reachable** — node is healthy, no action.
2. **API unreachable, peers reachable, peers see API** — node is isolated; self-fence.
3. **API unreachable, peers reachable, peers also cannot see API** — cluster-wide outage; do not reboot.
4. **API unreachable, peers unreachable** — network split; conservative: do not reboot (cannot confirm).

The local watchdog (softdog or hardware BMC watchdog) is armed at startup; SNR feeds it periodically. If the process stops feeding (due to reboot decision or crash), the kernel triggers a reboot after the watchdog timeout (~60 s for softdog on EC2).

## Repository layout

```
api/v1alpha1/               CRD types: SelfNodeRemediation, Config, Template
internal/
  controller/               SNR + SNRConfig reconcilers
  apicheck/                 API server reachability check
  apply/                    Server-side apply / merge helpers
  certificates/             mTLS cert management for peer gRPC
  controlplane/             Control-plane detection logic
  peerhealth/               gRPC peer health service (server + client)
  peers/                    Peer list management
  reboot/                   Watchdog arming + reboot execution
  render/                   Template rendering helpers
  snrconfighelper/          Config defaults + helpers
  template/                 SelfNodeRemediationTemplate reconciler
  utils/                    Shared utilities
  watchdog/                 Watchdog device abstraction (hw + softdog)
  webhook/                  Admission webhook
cmd/                        Operator main package
e2e/                        Ginkgo e2e suite
hack/                       Dev scripts
install/                    Install manifests
config/                     Kustomize bases (operator, rbac, bundle)
bundle/                     Generated OLM bundle (manifests, metadata, tests)
version/                    Operator version package
```

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

## Local development & deployment

Deploying to a dev cluster is standardized across all medik8s operators via the
shared dev environment in [`medik8s/tools`](https://github.com/medik8s/tools)
(`dev/dev.mk`). The Makefile pulls these targets in automatically: it uses a
sibling `../tools` checkout if present, otherwise shallow-clones the repo into
`.tools/` on first `make dev-*` use.

```bash
make dev-setup       # Create a Kind cluster (1 control-plane + 3 workers) with deps
make dev-deploy      # Build image, load it, install CRDs, deploy the operator
make dev-describe    # Summarize nodes, pods, CRs, leases, and events
make dev-redeploy    # Rebuild and restart pods (fast iteration)
make dev-undeploy    # Remove the operator
make dev-teardown    # Destroy the Kind cluster
make dev-help        # List all dev-* targets
```

Deploy to an existing cluster (OCP, etc.) with `SKIP_KIND=true`; images are
pushed to the ephemeral `ttl.sh` registry:

```bash
export KUBECONFIG=~/.kube/my-cluster
SKIP_KIND=true make dev-setup dev-deploy
```

Exercise remediation with `make dev-simulate-failure` (blocks the API server from
a worker with `make dev-simulate-network`), then `make dev-recover` on Kind. For
SNR specifically, manifests use `${IMG}` placeholders expanded by `envsubst`, so
always deploy via `make dev-deploy`/`make dev-redeploy` rather than applying
`kustomize build` output directly. See
[`dev/README.md`](https://github.com/medik8s/tools/blob/main/dev/README.md) in
`medik8s/tools` for prerequisites, all targets, and per-operator coverage.

## Code style

- Go, Kubebuilder v4, controller-runtime; follows standard medik8s patterns.
- Imports must be sorted (`make fix-imports`).
- No direct commits to `main`; open a PR.

## Key design constraints

- **Watchdog must be armed at startup** and fed continuously. If SNR crashes without triggering a graceful shutdown, the watchdog fires after its timeout — this is intentional (fail-safe reboot).
- **Softdog fires on EC2** (verified): when the agent stops feeding, the kernel triggers reboot in ~60 s. Use `journalctl --list-boots` to confirm reboot happened.
- **gRPC peer communication uses mTLS** (mutual TLS). Cert rotation is managed by the `certificates` package — do not bypass it.
- The `SelfNodeRemediationConfig` CR carries cluster-wide defaults (watchdog path, safe-time-to-assume-node-rebooted, peer timeout, etc.). Most fields have sane defaults; do not change them without understanding the consensus timing implications.
- **`safeTimeToAssumeNodeRebooted`** must be longer than the watchdog timeout + reboot time; if it is too short, NHC may allow workloads to reschedule before the node has actually rebooted, risking dual-writer data corruption.

## Security

- Agent DaemonSet runs privileged (needs `/dev/watchdog*` access and ability to trigger reboot).
- Peer gRPC is mTLS-authenticated — SNR manages its own CA and leaf certs.
- Never loosen RBAC beyond the generated `config/rbac/` manifests without review.

## Keeping the docs current

If your changes affect anything described here — build commands, repo layout, consensus logic, watchdog behavior, CRD semantics, e2e setup — or any other existing documentation (`README.md`, `CONTRIBUTING.md`, anything under `docs/`, inline command or usage references), update all of it so the docs never drift from the code.

## Commit conventions

- Reference the relevant issue or PR number when applicable.
- Use WIP in title when creating draft PRs to save CI resources.