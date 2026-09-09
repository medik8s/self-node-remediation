# Certsuite Best Practices Exceptions for Self Node Remediation Operator

This document justifies Self Node Remediation (SNR) operator configurations that deviate from Red Hat's [certsuite](https://github.com/redhat-best-practices-for-k8s/certsuite) (Cloud-native best practices test suite for Kubernetes workloads). The SNR operator follows these best practices where applicable, but requires specific exceptions that are architectural requirements for node-level self-remediation.

---

## 1. [`access-control-cluster-role-bindings`](https://github.com/redhat-best-practices-for-k8s/certsuite/blob/main/CATALOG.md#access-control-cluster-role-bindings)

The SNR operator requires the system:auth-delegator pattern for secure metrics serving and webhook authentication.

### Justification

These ClusterRoleBindings delegate token review and subject access review to the API server, a standard Kubernetes pattern for operators with conversion webhooks. Without these bindings, the operator cannot authenticate incoming requests to its metrics and webhook endpoints.

The operator has CRD conversion webhooks that require auth delegation. [RHWA-1743](https://redhat.atlassian.net/browse/RHWA-1743) further reduces cluster-wide permissions by moving secret RBAC from ClusterRole to namespace-scoped Role.

---

## 2. [`access-control-pod-host-path`](https://github.com/redhat-best-practices-for-k8s/certsuite/blob/main/CATALOG.md#access-control-pod-host-path)

The SNR DaemonSet agent mounts `/dev` to access the hardware watchdog device (`/dev/watchdog`).

### Justification

The watchdog is the primary self-remediation mechanism: if the agent stops feeding the watchdog timer, the hardware automatically reboots the node. Without host device access, the operator cannot perform its core function.

The mount uses `type: Directory` and the container has `readOnlyRootFilesystem: true` to limit write surface.

---

## 3. [`access-control-pod-host-pid`](https://github.com/redhat-best-practices-for-k8s/certsuite/blob/main/CATALOG.md#access-control-pod-host-pid)

The SNR DaemonSet agent requires host PID namespace access for software reboot and host process state observation.

### Justification

The agent performs software reboot via `nsenter -t 1 -m -- /sbin/reboot` as a fallback when the hardware watchdog is unavailable. Without `hostPID: true`, this fallback remediation mechanism cannot function.

---

## 4. [`access-control-pod-role-bindings`](https://github.com/redhat-best-practices-for-k8s/certsuite/blob/main/CATALOG.md#access-control-pod-role-bindings)

The RoleBinding `service-auth-reader` in the `kube-system` namespace is automatically created by [Operator Lifecycle Manager (OLM)](https://olm.operatorframework.io/) when the operator deploys a webhook server.

### Justification

The operator code does not create this binding. It is OLM infrastructure behavior that the operator cannot prevent or control. All OLM-managed operators with webhooks receive this binding for TLS auth delegation.

**RoleBinding details:**
- **Created by:** OLM (not the operator)
- **Purpose:** Webhook TLS authentication
- **Standard pattern:** All OLM webhook-based operators

---

## 5. [`access-control-security-context-non-root-user-id-check`](https://github.com/redhat-best-practices-for-k8s/certsuite/blob/main/CATALOG.md#access-control-security-context-non-root-user-id-check)

The SNR DaemonSet agent requires root UID and privileged mode for host-level operations.

### Justification

The DaemonSet agent requires root UID and privileged mode for:
- Watchdog device access
- `nsenter` for software reboot (CAP_SYS_ADMIN)
- Host-level process management

The controller-manager runs as non-root (UID 65532). Only the DS agent needs elevated privileges. The agent uses `readOnlyRootFilesystem: true` to limit write surface.

---

## 6. [`lifecycle-container-poststart`](https://github.com/redhat-best-practices-for-k8s/certsuite/blob/main/CATALOG.md#lifecycle-container-poststart)

The SNR DaemonSet agent does not implement a `postStart` lifecycle hook.

### Justification

The only possible `postStart` would be a no-op (`/bin/true`). The agent starts immediately via controller-runtime and requires no initialization hook.

A no-op hook on every node adds exec overhead with no operational value. In remediation scenarios, fast agent start matters for timely node recovery.

**Note:** This is a SOFT exception - technically implementable but provides no operational benefit.

---

## 7. [`lifecycle-container-prestop`](https://github.com/redhat-best-practices-for-k8s/certsuite/blob/main/CATALOG.md#lifecycle-container-prestop)

The SNR DaemonSet agent does not implement a `preStop` lifecycle hook.

### Justification

The DS agent is not behind a Kubernetes Service. Peer health monitoring uses direct pod IPs, not Service endpoints. A `preStop` sleep has no endpoints to drain.

Adding shutdown delay to a remediation agent risks slower watchdog response. The controller-manager correctly has `preStop` because it IS behind a Service.

**Note:** This is a SOFT exception - technically implementable but counterproductive for remediation latency.

---

## 8. [`lifecycle-pod-owner-type`](https://github.com/redhat-best-practices-for-k8s/certsuite/blob/main/CATALOG.md#lifecycle-pod-owner-type)

The SNR remediation agent is deployed as a DaemonSet, not a Deployment or ReplicaSet.

### Justification

SNR must run exactly one agent pod per node. DaemonSet is the only Kubernetes workload type that guarantees per-node coverage with automatic scheduling as nodes join.

A ReplicaSet or StatefulSet cannot guarantee per-node coverage and could leave nodes without self-remediation capability.

---

## 9. [`lifecycle-pod-toleration-bypass`](https://github.com/redhat-best-practices-for-k8s/certsuite/blob/main/CATALOG.md#lifecycle-pod-toleration-bypass)

The SNR DaemonSet agent uses non-default tolerations for control-plane and infrastructure node taints.

### Justification

SNR must monitor ALL nodes including control-plane. Without these tolerations, control-plane nodes would lack self-remediation capability.

The `remediation.medik8s.io/self-node-remediation` toleration enables SNR's isolation mechanism during active remediation. All Medik8s node-level operators use identical tolerations.

---

## 10. [`operator-install-status-no-privileges`](https://github.com/redhat-best-practices-for-k8s/certsuite/blob/main/CATALOG.md#operator-install-status-no-privileges)

The CSV declares privileged SCC access for the SNR DaemonSet agent.

### Justification

The CSV declares privileged SCC access because the DS agent requires host-level access (watchdog device, host PID namespace, root UID).

This is a direct consequence of exceptions 2, 3, and 5 above. Only the DS agent uses privileged SCC; the controller-manager uses the restricted SCC.

---

## Summary

All 10 configurations are architectural requirements for node-level self-remediation:

**HARD exceptions (8):** Architecturally required, cannot be changed without breaking core functionality
- Exceptions 1-5, 8-10

**SOFT exceptions (2):** Design choices that provide no operational benefit
- Exceptions 6-7 (lifecycle hooks)

These configurations follow the same patterns as other Medik8s node-level operators (Node Maintenance Operator, Fence Agents Remediation).
