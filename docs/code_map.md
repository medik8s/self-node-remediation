# Self Node Remediation (SNR) — Code Map

**Repository:** `github.com/medik8s/self-node-remediation`

---

## Top-level layout

```
self-node-remediation/
├── cmd/
│   └── main.go                      # Entry: manager vs agent split, manager/agent init
├── api/v1alpha1/                    # CRD Go types + generated deepcopy (not admission webhooks)
├── internal/
│   ├── controller/                  # SelfNodeRemediation (+ config) reconcilers, owner/name helpers
│   ├── webhook/v1alpha1/            # Admission webhooks for CRDs
│   ├── apicheck/                    # API /readyz polling + peer-based health decision
│   ├── apply/                       # Merge/patch helpers
│   ├── certificates/                # TLS material for gRPC (Secret-backed storage)
│   ├── controlplane/                # Control-plane node diagnostics / reachability
│   ├── peerhealth/                  # gRPC server + client (protobuf), peer SNR CR checks
│   ├── peers/                       # Peer address discovery + response aggregation
│   ├── reboot/                      # Rebooter, reboot-duration calculator
│   ├── render/                      # Template rendering helpers
│   ├── snrconfighelper/             # Default SelfNodeRemediationConfig registration
│   ├── template/                    # SelfNodeRemediationTemplate creation
│   ├── utils/                       # Taints, nodes, pods, namespaces, uptime, annotations
│   └── watchdog/                    # Linux watchdog abstraction (+ tests / fakes)
├── install/                         # Raw manifests (e.g. DaemonSet templates) applied by config controller
├── config/                          # Kustomize / OLM bundle sources
├── bundle/                          # OLM bundle manifests
└── vendor/                          # Vendored deps (read-only for navigation)
```

---

## Key files

### `cmd/main.go`

| Concern | Description |
|---------|-------------|
| **`initSelfNodeRemediationManager`** | Webhooks for CRDs; **`SelfNodeRemediationConfigReconciler`**; **`snrconfighelper`**; **`template.Creator`**; **`SelfNodeRemediationReconciler`** `IsAgent=false`. |
| **`initSelfNodeRemediationAgent`** | Watchdog; node annotations; **`peers.New`**; **`apicheck.New`**; **`controlplane.NewManager`**; **`SelfNodeRemediationReconciler`** `IsAgent=true` + **`Rebooter`**; **`peerhealth.NewServer`**. |
| **Flags** | `--is-manager`, `--metrics-bind-address`, `--health-probe-bind-address`, `--leader-elect`, `--enable-http2`. |

### `internal/controller/selfnoderemediation_controller.go`

| Concern | Description |
|---------|-------------|
| **`Reconcile` / `ReconcileManager` / `ReconcileAgent`** | Manager orchestrates phases + taints + strategies; agent handles **reboot** only in **Pre-Reboot-Completed**. |
| **Phases** | **`fencingStartedPhase`**, **`preRebootCompletedPhase`**, **`rebootCompletedPhase`**, **`fencingCompletedPhase`**. |
| **Taints** | **`NodeNoScheduleTaint`** (`remediation.medik8s.io/...`); **`OutOfServiceTaint`** (`node.kubernetes.io/out-of-service`). |
| **`getRuntimeStrategy`** | **`Automatic`** → **`OutOfServiceTaint`** if **`IsOutOfServiceTaintGA`**, else **`ResourceDeletion`**. |

### `internal/controller/owner_and_name.go`

| Function | Description |
|----------|-------------|
| **`IsSNRMatching`** | Whether SNR applies to this agent’s node / Machine. |
| **`getNodeName`**, **`getNodeNameFromMachine`** | Resolve unhealthy node name from NHC vs Machine ownership. |

### `internal/controller/selfnoderemediationconfig_controller.go`

Installs/updates **DaemonSet** and related install artifacts from **`./install`**, consumes **`RebootDurationCalculator`**, watches **`SelfNodeRemediationConfig`**.

### `api/v1alpha1/`

| File | Role |
|------|------|
| `selfnoderemediation_types.go` | **`SelfNodeRemediation`**, strategies, condition types. |
| `selfnoderemediationconfig_types.go` | Config fields + defaults (`ConfigCRName`, watchdog path, timings). |
| `selfnoderemediationtemplate_types.go` | Template CRD + **`NewRemediationTemplates`**. |

### `internal/webhook/v1alpha1/`

Admission defaults/validation for CRDs (e.g. `*_webhook.go` files — layout may vary by branch).

### `internal/apicheck/check.go`

**`ApiConnectivityCheck`**: **`/readyz?exclude=shutdown`** loop; **`isConsideredHealthy`** integrates **`peers`** + **`controlplane.Manager`**; optional self-reboot via **`Rebooter`**.

### `internal/peerhealth/server.go` / `client.go`

gRPC **PeerHealth** service: answers whether requesting node should be considered unhealthy based on **`SelfNodeRemediation`** existence (uses **`internal/controller`** matching helpers such as **`IsSNRMatching`**).

### `internal/reboot/`

| File | Role |
|------|------|
| `rebooter.go` | **`WatchdogRebooter`**, **`sysrq-trigger`** software reboot fallback. |
| `calculator.go` | **`GetRebootDuration`**, **`MaxTimeForNoPeersResponse`**. |

### `internal/utils/taints.go`

Kubernetes version → **`IsOutOfServiceTaintSupported`** / **`IsOutOfServiceTaintGA`**.

### `internal/utils/annotations.go`

Node annotations **`is-reboot-capable.self-node-remediation.medik8s.io`**, **`self-node-remediation.medik8s.io/watchdog-timeout`**.

---

## API short names (CLI)

| Resource | Short names |
|----------|-------------|
| `SelfNodeRemediation` | `snr`, `snremediation` |
| `SelfNodeRemediationConfig` | `snrconfig`, `snrc` |
| `SelfNodeRemediationTemplate` | `snrt`, `snremediationtemplate`, `snrtemplate` |

*(Defined in kubebuilder `+kubebuilder:resource:shortName` tags on types.)*
