# Self Node Remediation (SNR) — Operational Runbook

## 1. Components

| Piece | Role |
|-------|------|
| **Operator (manager)** | **`--is-manager`**: installs DaemonSet from **`install/`**, webhooks, **`SelfNodeRemediationConfig`** reconciliation, orchestrates **`SelfNodeRemediation`** (no reboot). |
| **DaemonSet agents** | Per-node: API check, peers, gRPC **peer health**, watchdog, **reboot** execution. |

Ensure **`SelfNodeRemediationConfig`** named **`self-node-remediation-config`** exists in the **operator namespace** — without it, new remediations report **`Disabled`**.

---

## 2. Prerequisites

| Requirement | Note |
|-------------|------|
| Kubernetes compatible with shipped bundle | Webhooks + CRDs **`v1alpha1`**. |
| **Peer port** | Default **hostPort** **30001** — **hostNetwork**/port mapping must allow **node-to-node** gRPC between agents. |
| **Watchdog** (recommended) | **`spec.watchdogFilePath`** default **`/dev/watchdog`**; agents annotate nodes with **`is-reboot-capable`** and watchdog timeout. |
| **Privileged / host access** | DaemonSet typically needs host mount access per **OLM/SCC** bundle — follow install docs for OpenShift. |

---

## 3. Quick health checks

```bash
# Config present (replace namespace)
kubectl get snrconfig self-node-remediation-config -n <operator-ns>

# Agents running
kubectl get pods -n <operator-ns> -l app.kubernetes.io/name=self-node-remediation -o wide

# Recent remediations
kubectl get snr -A
kubectl describe snr -n <ns> <name>
```

Inspect **Conditions** (**Processing**, **Succeeded**, **Disabled**), **`status.phase`**, **`status.lastError`**, **`status.timeAssumedRebooted`**. For stuck fencing, **`kubectl describe node <node>`** confirms **taints** and whether the SNR **finalizer** is still in play.

---

## 4. Configuration knobs (`SelfNodeRemediationConfig`)

| Field area | Effect |
|------------|--------|
| **`safeTimeToAssumeNodeRebootedSeconds`** | Upper bound for safe workload migration timing — **ignored if below** calculated minimum (see calculator). |
| **`apiCheckInterval`**, **`apiServerTimeout`**, **`maxApiErrorThreshold`** | API flake tolerance before peer escalation. |
| **`peerUpdateInterval`**, **`peerDialTimeout`**, **`peerRequestTimeout`**, **`peerApiServerTimeout`** | Peer list freshness and RPC timeouts. |
| **`hostPort`** | gRPC peer health port (**must align** with firewall / other services). |
| **`minPeersForRemediation`** | Minimum worker peers required before trusting peer-based isolation signals (**0** disables peer requirement — use with care). |
| **`isSoftwareRebootEnabled`** | Allow **software reboot** when watchdog unusable. |
| **`customDsTolerations`** | Schedule agents on tainted nodes (e.g. infra). |
| **`endpointHealthCheckUrl`** | Control-plane diagnostic HTTP check when workers cannot be used as peers. |

Environment variables on the DaemonSet mirror many of these (see **`cmd/main.go`** **`getDurEnvVarOrDie`** / config reconciliation).

---

## 5. Strategy selection (`SelfNodeRemediation`)

- **`Automatic`:** Uses **`OutOfServiceTaint`** only when **`IsOutOfServiceTaintGA`** is true at runtime (Kubernetes **1.28+ GA** path in code); otherwise **`ResourceDeletion`**.
- **`ResourceDeletion`:** Deletes workloads / attachments after fencing timeline — does **not** depend on OOS GA.
- **`OutOfServiceTaint`:** Applies **`node.kubernetes.io/out-of-service`** — requires cluster support for the intended detach/eviction semantics. After **`status.timeAssumedRebooted`**, controller housekeeping uses a **~1 minute** window (**`OutOfServiceTimeoutDuration`**); if detach/cleanup looks stuck, see **failure_modes §4.2**.

Verify **`kubectl version`** / **`Server Version`** vs operator expectations if remediation appears to stick in **Reboot-Completed**.

---

## 6. Debugging checklist

1. **SNR CR** — phase stuck? **`lastError`**? **Conditions**? **Events** on the SNR?
2. **Target identity** — **`kubectl get snr -n <ns> <name> -o yaml`**: align **`metadata.name`**, annotation **`remediation.medik8s.io/node-name`**, and (if **Machine**-owned) **`Machine.status.nodeRef`** (`kubectl get machine`).
3. **Agent on node** — SNR agent **pod** scheduled? Annotation **`is-reboot-capable.self-node-remediation.medik8s.io=true`**?
4. **Exclude label** — Node has **`remediation.medik8s.io/exclude-from-remediation=true`**? Remediation is skipped; look for **`RemediationSkipped`** on the SNR.
5. **Node taints / finalizer** — **`kubectl describe node <node>`**: **`remediation.medik8s.io/self-node-remediation`** (NoSchedule), **`node.kubernetes.io/out-of-service`** when using OOS strategy; SNR **`self-node-remediation.medik8s.io/snr-finalizer`** until cleanup completes.
6. **Peers** — enough nodes? **`minPeersForRemediation`**? Network **TCP** to peer IPs on **`hostPort`**?
7. **API check** — agent logs for **`/readyz`** failures; transient vs sustained outage.
8. **Watchdog** — logs for **Malfunction** / **software reboot** path; **`IS_SOFTWARE_REBOOT_ENABLED`** / config **`isSoftwareRebootEnabled`**.
9. **NHC** — timeout annotation **`remediation.medik8s.io/nhc-timed-out`** ends remediation early.
10. **Duplicate reboot skip** — If behaviour looks wrong after a recent reboot, compare **node uptime** to SNR **`metadata.creationTimestamp`** (code avoids a **second** reboot in the same lifecycle via **`didIRebootMyself`**).

---

## 7. Coexistence with SBR

Do **not** assume two watchdog-owning remediations are safe on the same node. Prefer **one** active fencing story per node (or **SBR detect-only** + SNR remediation, per product guidance).

---

## 8. Upgrade / version

Operator logs print **Go**, **Operator Version**, **Git Commit** (`cmd/main.go` **`printVersion`**). Use operator release notes for Kubernetes **CE** / **OpenShift** mapping.
