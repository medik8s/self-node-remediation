# Fix: SNR Cannot Remediate Isolated Control Plane Nodes

## The Problem

SNR's health check loop calls `/readyz?exclude=shutdown` through the Kubernetes
ClusterIP service to determine whether the API server is reachable. On worker
nodes this works correctly — when a worker is network-isolated, the request
fails, the error count climbs, peers are consulted, and remediation proceeds.

On control plane nodes it does not work. When a CP node loses network
connectivity, kube-proxy routes the ClusterIP request to the only API server
it can still reach: the local one running on the same node. That local API
server returns HTTP 200 because the `/readyz` sub-checks (informer sync,
post-start hooks) do not verify etcd quorum. The result: `errorCount` never
increments, peer consultation is never triggered, the watchdog is fed
indefinitely, and the node never reboots.

Switching to `/healthz` does not help. The etcd healthz check tests local
etcd connectivity, not quorum — it would still pass on an isolated node.

### Why This Matters

In a 2+1 arbiter topology (or any cluster with 3+ CP nodes), a
network-isolated CP node holds its etcd member hostage. The remaining CP nodes
cannot form quorum until the isolated node's etcd member is removed or the node
reboots. SNR exists precisely to handle this — but the bug prevents it from
ever firing.

## Root Cause

The health check has a single signal: "can I reach the API server?" On CP
nodes this signal is tautologically true because the API server is local.
There is no second signal to distinguish "healthy and connected" from
"healthy but isolated."

## The Fix

Add a second signal: **peer reachability**. On a CP node, after readyz
returns 200, verify that at least one peer node is reachable via TCP. If no
peer is reachable, treat the check as failed and enter the existing error
threshold / peer consultation flow.

This is the minimal change that breaks the tautology. It requires no new CRD
fields, no configuration changes, and no modifications to worker node behavior.

### What Changes

**1. Peer reachability gate on CP nodes** (`Start()` in `internal/apicheck/check.go`)

After readyz returns 200 on a CP node, call `canReachAnyPeer()`:
- If any peer responds to a TCP dial → the node has network connectivity →
  reset error count (existing behavior).
- If no peer responds → treat this cycle as a failure → enter
  `isConsideredHealthy()` (existing error/peer consultation flow).

Worker nodes skip this check entirely — their behavior is unchanged.

`canReachAnyPeer()` uses a lightweight TCP dial (not full gRPC) against up to
3 peers from the combined worker + CP peer list. TCP is sufficient because we
are testing "is the network up?", not "is the peer application healthy?" The
full gRPC peer health check still runs later inside `isConsideredHealthy()`
when the error threshold is reached.

**2. CP isolation escalation** (`isConsideredHealthy()`)

A secondary issue: when readyz *does* fail on a CP node (e.g., iptables DROP
rather than NIC down), the existing code path enters `isConsideredHealthy()`
but the CP peer isolation signal can be discarded if the worker error threshold
has not yet been reached. This delays remediation unnecessarily.

A new counter (`cpUnreachableCount`) tracks consecutive CP peer unreachability
independently of the worker threshold. When it reaches `MaxErrorsThreshold`,
the code bypasses the worker threshold and feeds an isolation signal directly
to `IsControlPlaneHealthy()`.

### What Does Not Change

- The `/readyz?exclude=shutdown` endpoint and how it is called
- Worker node behavior (no code path change for workers)
- `getWorkerPeersResponse()`, `getControlPlanePeersStatus()`,
  `IsControlPlaneHealthy()`, `isDiagnosticsPassed()`
- Watchdog feeding/stopping mechanics
- All existing configuration (no new CRD fields, no new env vars)
- `SafeTimeToAssumeNodeRebootedSeconds` calculation
- The OLM bundle

### Upgrade Path

- `ApiConnectivityCheckConfig.Peers` changes from `*peers.Peers` to a
  `PeerAddressProvider` interface. `*peers.Peers` satisfies this interface
  without modification — no changes needed in `cmd/main.go` or any consumer.
- On upgrade, CP nodes gain the peer reachability check. A healthy,
  well-connected CP node sees no behavioral difference because
  `canReachAnyPeer()` succeeds immediately.

## Time to Remediation

With defaults (CheckInterval=15s, MaxErrorsThreshold=3, ApiServerTimeout=5s,
peerDialTimeout=2s, MaxTimeForNoPeersResponse=30s):

```
3 × (15s + 5s + ~2s) + 30s + watchdog ≈ 96s + watchdog timeout
```

This is comparable to the existing worker-node remediation timeline.

## Edge Cases

| Scenario | Outcome |
|----------|---------|
| Single CP node, no peers | `canReachAnyPeer()` → true (no peers = cannot determine isolation), error count resets |
| Brief network blip (1–2 cycles) | Error count stays below threshold, recovers next cycle |
| Partial isolation (some peers reachable) | `canReachAnyPeer()` → true, error count resets |
| Full isolation, NIC down | `canReachAnyPeer()` fails repeatedly → remediation triggers |
| Full isolation, iptables DROP | readyz fails + `cpUnreachableCount` escalates → remediation triggers |
| Compact 3-node cluster | Peers deduplicated across worker/CP lists, works correctly |

## Out of Scope

PR #255 identified a related issue: when peers are consulted, kube-proxy may
route their API server connections back through the failing CP node (1/N
probability), causing false "cluster-wide API issue" conclusions. This is
pre-existing and orthogonal — with the fixes above, the isolated CP node no
longer depends solely on the worker peer consultation flow. Recommend filing
a separate issue for the kube-proxy routing concern.
