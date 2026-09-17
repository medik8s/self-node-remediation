# Self Node Remediation (SNR) — Architecture

## High level

SNR ships as **one Go module** with **two runtime modes**:

| Mode | Flag | Runs where | Primary responsibility |
|------|------|------------|-------------------------|
| **Manager** | `--is-manager` | Operator deployment | Webhooks, **`SelfNodeRemediationConfig`** reconciliation (DaemonSet install), default config/template helpers, **`SelfNodeRemediation` reconciler** without reboot |
| **Agent** | *(default)* | DaemonSet on each node | Watchdog + **API connectivity check** + **peers** + **gRPC peer-health server** + **`SelfNodeRemediation` reconciler** that **reboots** when phase allows |

---

## Entry point (`cmd/main.go`)

- **controller-runtime** `Manager`: metrics, health probes, optional **webhooks** on port **9443** (TLS; OLM may inject certs under `/apiserver.local.config/certificates`).
- **`InitOutOfServiceTaintFlagsWithRetry`**: probes Kubernetes version to set **`IsOutOfServiceTaintSupported`** / **`IsOutOfServiceTaintGA`** (`internal/utils/taints.go`) — affects **`Automatic`** remediation strategy selection.
- **Manager path** registers webhooks for **`SelfNodeRemediationConfig`**, **`SelfNodeRemediationTemplate`**, **`SelfNodeRemediation`**; adds **`SelfNodeRemediationConfigReconciler`**; **`snrconfighelper`** default config initializer; **`template.Creator`**; **`SelfNodeRemediationReconciler`** with **`IsAgent: false`**.
- **Agent path** sets **`MY_NODE_NAME`**; initializes **watchdog** (`internal/watchdog`); updates **node annotations** (`is-reboot-capable.self-node-remediation.medik8s.io`, watchdog timeout); starts **`peers.Peers`**; **`apicheck.ApiConnectivityCheck`**; **`controlplane.Manager`** (for control-plane nodes); **`SelfNodeRemediationReconciler`** with **`IsAgent: true`** and **`Rebooter`**; **`peerhealth.Server`** (gRPC on **`HOST_PORT`** env, default port aligned with **`SelfNodeRemediationConfig.spec.hostPort`**, typically **30001**).

---

## Remediation reconciler (`internal/controller/selfnoderemediation_controller.go`)

### Manager (`ReconcileManager`)

- Loads **`SelfNodeRemediation`** CR; if **`SelfNodeRemediationConfig`** is missing → sets **`Disabled`** condition and stops.
- Respects **NHC timeout** annotation **`remediation.medik8s.io/nhc-timed-out`**: stops remediation and updates conditions / cleanup.
- Resolves **target node** via **`internal/controller/owner_and_name.go`**:
  - Owner **NodeHealthCheck** → node name from **`remediation.medik8s.io/node-name`** or **CR `.metadata.name`**.
  - Owner **Machine** → **`Machine.status.nodeRef.name`** (OpenShift Machine API).
  - Else → annotation or CR name.
- Honors **`remediation.medik8s.io/exclude-from-remediation`** label on Node **`true`** → skip.
- **Strategy selection** — see below.
- **Phase machine** (`.status.phase`):
  - **Fencing-Started**: ensure node **can reboot** (agent pod + **`is-reboot-capable`** annotation); add **finalizer**; apply **NoSchedule** taint **`remediation.medik8s.io/self-node-remediation`**; compute **`status.timeAssumedRebooted`** via **`RebootDurationCalculator`**; advance to **Pre-Reboot-Completed**.
  - **Pre-Reboot-Completed**: wait until **`timeAssumedRebooted`** has passed (assumes agent rebooted the node).
  - **Reboot-Completed**: run strategy-specific resource cleanup (**delete pods / VolumeAttachments** or **out-of-service taint** workflow).
  - **Fencing-Completed**: remove **NoSchedule** taint; remove **finalizer** on delete; set **Succeeded** conditions.

### Agent (`ReconcileAgent`)

- Confirms this **`SelfNodeRemediation`** targets **`MY_NODE_NAME`** (or owning Machine) via **`IsSNRMatching`**.
- Only when phase is **Pre-Reboot-Completed**, calls **`rebootIfNeeded`** — uses **`Rebooter`** (watchdog / software reboot). Manager pod **never** calls **`Rebooter`**.

---

## Remediation strategies

Implemented on **`SelfNodeRemediation.spec.remediationStrategy`**:

| Strategy | Behavior |
|----------|-----------|
| **`ResourceDeletion`** | After reboot window, **`resources.DeletePods`** + VolumeAttachment cleanup path via shared remediate helper. |
| **`OutOfServiceTaint`** | Applies **`node.kubernetes.io/out-of-service`** (`NoExecute`); relies on Kubernetes **GA** semantics for forced volume detach where enabled; uses **timer** **`OutOfServiceTimeoutDuration`** ( **1 minute** in controller) for edge cases when deletion does not complete. |
| **`Automatic`** | At reconcile time: if **`IsOutOfServiceTaintGA`** is **true** (Kubernetes **1.28+** GA path in code), use **`OutOfServiceTaint`**; else **`ResourceDeletion`**. |

> **Note:** CRD field comments may mention 1.26 for out-of-service; the **Automatic** branch in code keys off **`IsOutOfServiceTaintGA`**, not merely “supported”.

---

## API connectivity and peers (`internal/apicheck`, `internal/peers`, `internal/peerhealth`)

- Agent polls **`/readyz?exclude=shutdown`** on the API server on **`ApiCheckInterval`** with **`ApiServerTimeout`**.
- Consecutive failures increment a counter; below **`MaxApiErrorThreshold`**, the node is still treated as healthy.
- Above threshold, **worker peers** are consulted via **gRPC** (`peerhealth` client/server) to see if a **`SelfNodeRemediation`** exists for this node — peers answer using Kubernetes API + **`IsSNRMatching`** logic.
- **`MinPeersForRemediation`**: if not enough peer addresses are discovered, the implementation **avoids** declaring unhealthy (reduces false-positive reboots). **Isolated** node scenarios may still mark unhealthy when peers cannot be contacted (**`UnHealthyBecauseNodeIsIsolated`**).
- **`controlplane.Manager`** (`internal/controlplane`): on control-plane nodes, “healthy?” combines worker peer responses with **diagnostics** (configurable **endpoint URL**, kubelet reachability, ping to other control-plane machines).

---

## Reboot path (`internal/reboot`, `internal/watchdog`)

- **`WatchdogRebooter`**: prefers **stopping watchdog feed** to trigger hardware reset; if no watchdog, watchdog malfunction, or stuck **Triggered** state beyond **`TimeToAssumeRebootHasStarted` (30s)**, falls back to **software reboot** via **`nsenter`** + **`echo b > /proc/sysrq-trigger`**.
- **`IsSoftwareRebootEnabled`** in **`SelfNodeRemediationConfig`** gates whether software reboot is allowed when watchdog cannot be used.

---

## Safe timing (`internal/reboot/calculator.go`)

- **`GetRebootDuration`**: max of user **`safeTimeToAssumeNodeRebootedSeconds`** (if set and **not below** minimum) and a **calculated minimum** from config intervals, peer timeouts, **`MaxTimeForNoPeersResponse` (30s)** floor for peer interaction, and node **watchdog timeout** annotation **`self-node-remediation.medik8s.io/watchdog-timeout`**.

---

## Custom resources (`api/v1alpha1/`)

| CRD | Purpose |
|-----|---------|
| **`SelfNodeRemediation`** (`snr`) | Per-node remediation instance; **Processing/Succeeded/Disabled** conditions; **phase**, **timeAssumedRebooted**, **lastError**. |
| **`SelfNodeRemediationConfig`** (`snrconfig`) | Operator namespace singleton **`self-node-remediation-config`**: watchdog path, intervals, thresholds, **hostPort**, tolerations, **minPeersForRemediation**, etc. |
| **`SelfNodeRemediationTemplate`** (`snrt`) | Template for NHC — default **`self-node-remediation-automatic-strategy-template`** with **`Automatic`** strategy. |

---

## Coexistence (related operators)

**Storage-Based Remediation (SBR)** and SNR both use the **watchdog** and node-level fencing semantics. Running **two** fully active watchdog-owning remediation flows on the **same** node is **unsafe** without explicit product guidance; common patterns use **SBR detect-only** plus SNR remediation, or a **single** remediator. See **overview** and **runbook** §7.

---

## Networking and TLS

- Agents expose **gRPC** on **host port** ( **`HOST_PORT`** env / **`spec.hostPort`** ) for peer health checks — cluster firewall must allow **node ↔ node** traffic on that port.
- **TLS** for gRPC uses certificates provided via **`internal/certificates`** (Kubernetes Secret storage, reader from operator namespace).
