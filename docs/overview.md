# Self Node Remediation (SNR) — Overview

## What problem SNR solves

Many remediation flows assume **out-of-band** power control (BMC/IPMI). Clusters without BMCs—or policies that forbid using them—still need a **safe** way to recover when a node is unhealthy: workloads must not be rescheduled onto a node that might still be running (risking split brain or data corruption).

**Self Node Remediation** lets an **unhealthy node** participate in its own isolation: the cluster coordinates via Kubernetes APIs and **peer agents**, uses a **local watchdog** (with optional software reboot fallback), and applies **taints / workload eviction** so surviving control plane and schedulers can treat the node as reaching a **defined safe state** for recovery.

---

## How it works (plain language)

1. **Detection is external.** SNR does not decide “this node is unhealthy” by itself in production — typically **Node Health Check (NHC)** observes failing nodes and creates a **`SelfNodeRemediation`** (SNR) CR per unhealthy machine.

2. **Operator (“manager”)** runs cluster-wide: validates CRDs via webhooks, reconciles **`SelfNodeRemediationConfig`**, installs the **DaemonSet** of agents from manifests under `install/`, ensures default **templates**, and runs the **`SelfNodeRemediation` reconciler** in **manager** mode (orchestration only — no reboot on this pod).

3. **Agents** run on every node (DaemonSet): they maintain **API connectivity checks**, **peer lists**, a **gRPC peer-health service**, optional **hardware watchdog**, and the **`SelfNodeRemediation` reconciler** in **agent** mode. When remediation reaches the right **phase**, the agent on the **target node** triggers **reboot** (watchdog stop / software reboot).

4. **Remediation strategies** (on the SNR CR): **`Automatic`** chooses between **`ResourceDeletion`** and **`OutOfServiceTaint`** at runtime based on Kubernetes capabilities (see Architecture). **`ResourceDeletion`** deletes pods and VolumeAttachments for the unhealthy node after fencing timing. **`OutOfServiceTaint`** applies the well-known **`node.kubernetes.io/out-of-service`** taint so volume-attached pods can be force-deleted per Kubernetes semantics (when supported).

5. **Conditions on the SNR CR** (`Processing`, `Succeeded`, `Disabled`) communicate progress back to orchestrators such as NHC.

---

## Key concepts

| Concept | Meaning |
|--------|---------|
| **Manager vs agent** | Same binary; **`--is-manager`** selects operator + DS install + webhooks; default is **per-node agent**. |
| **Phases** | Fencing progresses through **Fencing-Started → Pre-Reboot-Completed → Reboot-Completed → Fencing-Completed** (string values on `.status.phase`). |
| **Peer health** | When API access flaps, agents ask **peers** over **gRPC** whether a **`SelfNodeRemediation` CR** exists for “this” node before treating local API loss as fatal. |
| **Safe reboot window** | **`status.timeAssumedRebooted`** — after this instant, other nodes assume the unhealthy one has rebooted so workload cleanup can proceed safely; derived from config + watchdog + timing calculator. |
| **NoSchedule taint** | **`remediation.medik8s.io/self-node-remediation:NoSchedule`** — applied before reboot window to steer scheduling away from the failing node. |
| **NHC timeout** | Annotation **`remediation.medik8s.io/nhc-timed-out`** stops remediation when NHC gives up. |
| **Exclude from remediation** | Node label **`remediation.medik8s.io/exclude-from-remediation=true`** — SNR skips that node (no phase progression). |

---

## Relationship to the medik8s ecosystem

| Component | Relationship |
|-----------|----------------|
| **NHC (Node Health Check)** | Creates **`SelfNodeRemediation`** CRs (often via **`SelfNodeRemediationTemplate`**); SNR executes remediation and reports **Processing/Succeeded**. |
| **`SelfNodeRemediationConfig`** | Namespaced configuration applied cluster-wide — a singleton (name **`self-node-remediation-config`**) honored only when created in the operator's own namespace; timings, watchdog path, peer thresholds, DaemonSet tolerations, etc. Without it, SNR is **Disabled** for new remediations. |
| **SBR (Storage-Based Remediation)** | Both systems use the **watchdog** on the node. Running **full** SBR and **full** SNR remediation on the same node can **conflict**. Supported coexistence patterns use SBR **detect-only** mode or a single active remediator — validate architecture for your cluster. |
| **Machine API (OpenShift)** | SNR CRs may be owned by a **`Machine`**; node name is resolved via **`Machine.status.nodeRef`**. |
| **Node name on CR** | Prefer annotation **`remediation.medik8s.io/node-name`**; else **`SelfNodeRemediation.metadata.name`**. |

---

## When to use SNR

- You need **automatic node-level** remediation **without BMC**.
- Nodes run Linux with SNR **agent pods** scheduled (privileged capabilities as required by the bundle).
- You integrate **NHC** (or equivalent) to create **`SelfNodeRemediation`** objects when nodes fail health checks.

---

## Current status (engineering)

- API group **`self-node-remediation.medik8s.io`**, CRDs **`v1alpha1`**.
- **Two reconciliation personalities** (manager vs agent) are intentionally split so reboot and cluster orchestration do not run in the same pod logic path.
