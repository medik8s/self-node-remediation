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

Inspect **Conditions** (**Processing**, **Succeeded**, **Disabled**), **`status.phase`**, **`status.lastError`**, **`status.timeAssumedRebooted`**, **`status.nodeBootID`** (the node's boot ID before the reboot; with **`requireBootIDChange`** the remediation stays in **Pre-Reboot-Completed** until the node reports a different one or its agent sets **`status.nodeRebootConfirmedAt`**, which it does when it finds the node already rebooted since the remediation was created), **`status.nodeUID`** (the Node object's UID before the reboot; a node with the same name and another UID is not the remediated node, and the remediation waits in **Pre-Reboot-Completed** until NHC times it out or it is deleted), **`status.rebootProofRequired`** (**true** when the reboot was prepared with **`armWatchdogOnlyForReboot`** on: the remediation waits for that proof whatever the configuration says now, and without a recorded boot ID only **`status.nodeRebootConfirmedAt`** releases it). For stuck fencing, **`kubectl describe node <node>`** confirms **taints** and whether the SNR **finalizer** is still in play.

---

## 4. Configuration knobs (`SelfNodeRemediationConfig`)

| Field area | Effect |
|------------|--------|
| **`safeTimeToAssumeNodeRebootedSeconds`** | A **requested** duration, not an upper bound: **`GetRebootDuration`** returns this value only if it's **at or above** the calculated minimum, otherwise it returns the (higher) calculated minimum — so setting it higher than the minimum **increases** the wait, it never caps it (see calculator). |
| **`apiCheckInterval`**, **`apiServerTimeout`**, **`maxApiErrorThreshold`** | API flake tolerance before peer escalation; also feed directly into the calculated minimum reboot duration as **`(apiCheckInterval + apiServerTimeout) × maxApiErrorThreshold`**. |
| **`peerUpdateInterval`**, **`peerDialTimeout`**, **`peerRequestTimeout`**, **`peerApiServerTimeout`** | Peer list freshness and RPC timeouts. |
| **`maxTimeForNoPeersResponse`** | Floor for peer-interaction timing in the calculated minimum reboot duration; **30s** default, now configurable (previously a hardcoded constant). |
| **`hostPort`** | gRPC peer health port (**must align** with firewall / other services). |
| **`minPeersForRemediation`** | Minimum worker peers required before trusting peer-based isolation signals (**0** disables peer requirement — use with care). |
| **`peerTopologyKey`** | Node label naming the failure domain (e.g. **`topology.kubernetes.io/zone`**); empty by default (unchanged evaluation). ApiError answers from peers of the node's own domain are ignored when a strict majority of the control plane and at least **`minPeersForRemediation`** peers are outside of it, so a partitioned domain self-reboots. The agent reads its own node's label at start. |
| **`isSoftwareRebootEnabled`** | Allow **software reboot** when watchdog unusable. |
| **`armWatchdogOnlyForReboot`** | **false** by default: the agent arms and feeds the watchdog at start, so a killed or crashed agent resets its node after the watchdog timeout. **true**: the device stays closed until a reboot is decided or requested (the agent still publishes the timeout from sysfs and **`is-reboot-capable=true`**). The dead-agent reset is also the fence for an agent that died after a reboot was prepared, so the option is accepted only together with **`requireBootIDChange`** (the webhook refuses it otherwise). The pair is one option: watchdog armed only for the reboot, workloads released only on proof of the reboot. |
| **`requireBootIDChange`** | **false** by default. When **true**, the manager releases the node's pods and volumes only once the node reports a **`status.nodeInfo.bootID`** different from the one recorded at **`status.nodeBootID`**, or its agent confirms in **`status.nodeRebootConfirmedAt`** that the node booted after the remediation was created, in addition to the safe time. Both proofs count only from the node the reboot was prepared for (**`status.nodeUID`**); a node that registered under the same name with another UID does not release it. A node that never comes back, or whose Node object was replaced, keeps its workloads until the remediation is deleted or NHC times it out. A node that reports no boot ID is released on the safe time alone, unless **`armWatchdogOnlyForReboot`** is on: then only **`status.nodeRebootConfirmedAt`** releases it. Required by **`armWatchdogOnlyForReboot`**; usable on its own. To turn both off, turn **`armWatchdogOnlyForReboot`** off first and let the agent DaemonSet roll out, then this one; the webhook refuses both in one update. Remediations prepared while **`armWatchdogOnlyForReboot`** was on carry **`status.rebootProofRequired: true`** and keep waiting for proof of the reboot through both steps; the rollout wait covers the remediations prepared after the first step on nodes whose agent still runs the old template. |
| **`customDsTolerations`** | Schedule agents on tainted nodes (e.g. infra). |
| **`endpointHealthCheckUrl`** | Control-plane diagnostic **ping** (ICMP, not HTTP) check when workers cannot be used as peers. |

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
6. **Peers** — enough nodes? **`minPeersForRemediation`**? Network **TCP** to peer IPs on **`hostPort`**? With **`peerTopologyKey`**, the agent logs whether failure domain awareness is active, and why not.
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
