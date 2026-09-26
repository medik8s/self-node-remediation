# Self Node Remediation (SNR) — Failure Modes

Behaviour references **`internal/controller/selfnoderemediation_controller.go`**, **`internal/apicheck/check.go`**, **`internal/reboot/`**, unless noted.

---

## Governing constants (selected)

| Constant | Value | Where |
|----------|-------|-------|
| **`OutOfServiceTimeoutDuration`** | **1 minute** — window after **`timeAssumedRebooted`** for out-of-service strategy housekeeping | `internal/controller/selfnoderemediation_controller.go` |
| **`TimeToAssumeRebootHasStarted`** | **30s** — watchdog reboot considered stuck if no progress | `internal/reboot/rebooter.go` |
| **`MaxTimeForNoPeersResponse`** | **30s** — floor for peer timing in reboot-duration calculation; also used in api-check staleness | `internal/reboot/calculator.go`, `internal/apicheck/check.go` |
| **`SNRFinalizer`** | `self-node-remediation.medik8s.io/snr-finalizer` | `internal/controller/selfnoderemediation_controller.go` |

Config defaults (override via **`SelfNodeRemediationConfig`**): **`apiCheckInterval`** default **15s**, **`apiServerTimeout`** **5s**, **`maxApiErrorThreshold`** **3**, **`peerUpdateInterval`** default **15m**, **`hostPort`** default **30001**, **`minPeersForRemediation`** default **1**.

---

## 1. Configuration and enablement

### 1.1 No `SelfNodeRemediationConfig`

- **Detection:** Config CR **`self-node-remediation-config`** absent or deleting in operator namespace.
- **Behaviour:** **`Disabled`** condition on **`SelfNodeRemediation`**; remediation does not proceed (**`isConfigurationExist`**).
- **Ops:** Create/restore config; verify operator reconciliation.

---

## 2. Target node resolution

### 2.1 Node not found

- **Detection:** **`getNodeFromSnr`** returns **NotFound**.
- **Behaviour:** **`remediationSkippedNodeNotFound`** — **Processing=False**, **Succeeded=False**; event **`GetTargetNodeFailed`**.
- **Ops:** CR name / **Machine** ownerRef / **`remediation.medik8s.io/node-name`** annotation alignment.

### 2.2 Machine owner but **`nodeRef` nil**

- **Detection:** **`getNodeNameFromMachine`** errors when **`Machine.status.nodeRef`** missing.
- **Behaviour:** Reconcile error; remediation cannot proceed until Machine registers node.

### 2.3 Node excluded from remediation

- **Detection:** Node label **`remediation.medik8s.io/exclude-from-remediation=true`**.
- **Behaviour:** Remediation skipped (no phase progression); event **`RemediationSkipped`**.

---

## 3. Fencing prerequisites (manager)

### 3.1 Node not “reboot capable”

- **Detection:** **`isNodeRebootCapable`** — missing SNR **agent pod** on node via **`GetSelfNodeRemediationAgentPod`**, or node annotation **`is-reboot-capable.self-node-remediation.medik8s.io`** ≠ **`true`**.
- **Behaviour:** **`prepareReboot`** returns error **“Node is not capable to reboot itself”** → exponential backoff; avoids deleting workloads while node might still run workloads unsafely.

### 3.2 Peer / API false positives (agent)

- **Detection:** **`ApiConnectivityCheck`** fails **`/readyz`** repeatedly.
- **Behaviour:** Until **`MaxApiErrorThreshold`**, errors ignored. Above threshold, **peer quorum** determines if node is unhealthy; **`MinPeersForRemediation`** not met → node may be treated as **healthy** to avoid wrong reboot (**`HealthyBecauseNoPeersWereFound`**). **Isolated** node with **zero** peers and **`MinPeersForRemediation` > 0** → **unhealthy** (**`UnHealthyBecauseNodeIsIsolated`**).
- **Outcome:** Possible **self-reboot** via **`Rebooter`** only when **`isConsideredHealthy()`** is false — see **`internal/apicheck/check.go`**.

### 3.3 Control-plane diagnostics (agent)

- **Context:** On **control-plane** nodes, **`controlplane.Manager`** combines worker **peer** answers with **HTTP** diagnostics (**`endpointHealthCheckUrl`** when set), **kubelet** reachability, and **ping** to other control-plane machines.
- **Behaviour:** A bad **URL**, network partition, or misconfigured endpoint can make **healthy vs unhealthy** decisions **flaky** or **misleading** compared to worker-only paths.
- **Ops:** Verify **`SelfNodeRemediationConfig.spec.endpointHealthCheckUrl`**, connectivity, and agent logs on the control-plane node.

---

## 4. Remediation execution

### 4.1 NHC timeout annotation

- **Detection:** **`remediation.medik8s.io/nhc-timed-out`** on **`SelfNodeRemediation`**.
- **Behaviour:** **`remediationTimeoutByNHC`**; cleanup paths — remove **out-of-service** taint if applicable; **`recoverNode`** removes **NoSchedule** taint and **finalizer** when deleting.

### 4.2 Strategy-specific stuck states

- **`OutOfServiceTaint`:** If pods / attachments not drained before **`timeAssumedRebooted + OutOfServiceTimeoutDuration`**, timer logic may remove **OOS** taint when CR is deleting and node considered healthy again — see **`isResourceDeletionExpired`** / **`removeOutOfServiceTaint`** paths.

### 4.3 Watchdog / reboot failures

- **Detection:** **`WatchdogRebooter.Reboot`** errors on unexpected watchdog **Status**; software reboot **`Run`** errors logged.
- **Behaviour:** Software reboot path still returns **nil** after logging (**`softwareReboot`** swallows run error — ops should inspect logs).

### 4.4 Agent reboot skipped as duplicate

- **`didIRebootMyself`** compares **Linux uptime** vs SNR **creation time** — if host already rebooted once in this lifecycle, avoids second reboot.

### 4.5 Safe timing vs calculated minimum

- **Symptom:** Workloads or **VolumeAttachments** still attached when cleanup runs, or fencing/cleanup feels **too early** or **too late** relative to an expected reboot.
- **Behaviour:** **`status.timeAssumedRebooted`** comes from **`RebootDurationCalculator`**: effective wait is the **max** of **`safeTimeToAssumeNodeRebootedSeconds`** (only if **not below** the computed minimum) and a **minimum** derived from API/peer intervals, **`MaxTimeForNoPeersResponse`**, and the node **watchdog-timeout** annotation — see **architecture** (Safe timing).
- **Ops:** Tune config intervals and **`safeTimeToAssumeNodeRebootedSeconds`**; confirm the node **`self-node-remediation.medik8s.io/watchdog-timeout`** annotation matches hardware expectations.

---

## 5. Observable signals

| Signal | Meaning |
|--------|---------|
| **`SelfNodeRemediation.status.conditions`** | **Processing**, **Succeeded**, **Disabled** — orchestrator contract for NHC |
| **`SelfNodeRemediation.status.phase`** | **Fencing-Started**, **Pre-Reboot-Completed**, **Reboot-Completed**, **Fencing-Completed** |
| **`status.lastError`** | Last reconcile error string |
| **Events** on SNR / Node | e.g. **RemediationStarted**, **AddNoSchedule**, **NodeReboot**, **DeleteResources**, **RemoveFinalizer** |
| **Node taints** | **`remediation.medik8s.io/self-node-remediation`** (NoSchedule); **`node.kubernetes.io/out-of-service`** (when strategy uses OOS) |
| **Node annotations** | **`is-reboot-capable.self-node-remediation.medik8s.io`**, **`self-node-remediation.medik8s.io/watchdog-timeout`** |
