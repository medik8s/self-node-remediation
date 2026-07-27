# Fix: SNR Cannot Remediate Isolated Control Plane Nodes

## The Problem

SNR's health check loop calls `/readyz?exclude=shutdown` through the Kubernetes
ClusterIP service to determine whether the API server is reachable. On worker
nodes this works correctly — when a worker is network-isolated, the request
fails, the error count climbs, peers are consulted, and remediation proceeds.

On control plane nodes it does not work. When a CP node loses network
connectivity, the ClusterIP request can be routed to the local kube-apiserver
endpoint running on that same node. In this failure mode, the local API server
continues responding even though the node is isolated from the rest of the
cluster. That local API server returns HTTP 200 because the `/readyz` checks
do not provide a signal that detects loss of connectivity to the rest of the
cluster. The result: `errorCount` never increments, peer consultation is
never triggered, the watchdog is fed indefinitely, and the node never reboots.

### Why This Matters

In a 2+1 arbiter topology (or standard 3-CP clusters), when a CP node is
network-isolated, the remaining nodes maintain etcd quorum and the cluster
continues operating in degraded mode. However, workloads on the isolated node
keep running with stale state and are not rescheduled, because SNR never
detects the isolation and never fences the node.

## Root Cause

SNR calls the kube-apiserver `/readyz` endpoint through the ClusterIP service.
In the failure mode observed here, the local API server handles the request.
The `/readyz` endpoint runs multiple sub-checks (informer sync, post-start
hooks, etc.) but does not provide a signal that detects loss of connectivity
to the rest of the cluster. The relevant `/readyz` sub-checks pass even on an
isolated node, so SNR has no signal to distinguish "healthy and connected"
from "healthy but isolated."

## The Fix

Add a second signal: **peer reachability**. On a CP node, after readyz
returns 200, verify that at least one peer node is reachable via TCP. If no
peer is reachable, treat the check as failed and enter the existing error
threshold / peer consultation flow.

This is the minimal change that prevents SNR from treating a local API
availability signal as proof of cluster connectivity. It requires no new CRD
fields, no configuration changes, and no modifications to worker node behavior.

### What Changes

**Peer reachability gate on CP nodes** (`Start()` in `internal/apicheck/check.go`)

After readyz returns 200 on a CP node, call `canReachAnyPeer()`:
- If any peer responds to a TCP dial (or if the check is bypassed due to 0
  configured peers) → reset error count (existing behavior).
- If no peer responds → treat this cycle as a failure → enter
  `isConsideredHealthy()` (existing error/peer consultation flow).

Worker nodes skip this check entirely — their behavior is unchanged.

`canReachAnyPeer()` uses a lightweight TCP dial (not full gRPC) against a
random sample of up to 3 peers from the combined worker + CP peer list. TCP
reachability is sufficient because this check only needs an additional signal
that the node is not completely isolated from the cluster. It intentionally
does not validate peer application health; the full gRPC peer health check
still runs later inside `isConsideredHealthy()` when the error threshold is
reached.

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
peerReachabilityTimeout=2s, MaxTimeForNoPeersResponse=30s).

Worst-case approximation (peer dials are concurrent; ~2s reflects a single
timeout when all sampled peers are unreachable):

```text
3 × (15s + 5s + ~2s) + 30s + watchdog ≈ 96s + watchdog timeout
```

This is comparable to the existing worker-node remediation timeline.

## Edge Cases

| Scenario | Outcome |
|----------|---------|
| Single CP node, no peers (e.g. SNO) | `canReachAnyPeer()` returns true (no peers available → bypass the isolation signal), error count resets. No false-positive self-reboot. |
| Single CP node + multiple workers | Worker peers are included via `getAllPeerAddresses()`. If any worker responds → healthy. If all unreachable → enters peer consultation with `MinPeersForRemediation` safety threshold preventing false-positive self-reboot. |
| Brief network blip (1–2 cycles) | Error count stays below threshold, recovers next cycle |
| Partial isolation (some peers reachable) | `canReachAnyPeer()` → true, error count resets |
| Full isolation, NIC down | `canReachAnyPeer()` fails repeatedly → remediation triggers |
| Compact 3-node cluster | Peers deduplicated across worker/CP lists, works correctly |

## Considered Alternatives

### 1. Switch from `/readyz` to `/healthz` or `/livez`

Testing on the affected OCP environment showed that `/healthz` includes an etcd
sub-check that reports failure (`[-]etcd failed: reason withheld`) when the
node is network-isolated. Switching the endpoint would be a one-line fix.

**Why not chosen as primary fix:**
- `/healthz` is deprecated since Kubernetes v1.16
- `/livez` may include the etcd check (version-dependent) but needs empirical
  validation on isolated nodes
- Switching the endpoint changes behavior for **all nodes** (workers too),
  not just CP nodes. Any sub-check difference between `/readyz` and
  `/healthz` or `/livez` could introduce false positives in non-isolation
  scenarios (e.g., transient etcd issues, checks present in one endpoint but
  not the other)
- The existing >50% `ApiError` threshold in the peer check provides protection
  against cluster-wide etcd failures, but this interaction needs careful
  validation

This approach may be viable as a complementary or alternative fix after
further testing. If `/livez` reliably catches isolation without false
positives, it could replace or complement the peer reachability approach.

### 2. Use node IP of another CP node instead of ClusterIP

Instead of calling the API server through ClusterIP (which in the affected
failure mode can resolve to the local server), call a specific remote CP node's
API server directly.

**Why not chosen:**
- If the target CP node is down, the check fails for the wrong reason
- Adds coupling to a specific peer node's availability
- Requires additional logic to select which CP node to target and handle
  failover
- Higher risk of unintended side effects on the existing peer check flow

### 3. Add etcd cluster health/membership validation directly

Query etcd cluster health and membership state directly and include it in the
health check.

**Why not chosen:**
- Determining etcd cluster health requires etcd client credentials and direct
  API interaction, adding operational complexity
- The peer reachability approach detects the network isolation scenario —
  the primary trigger for this bug — without coupling to etcd internals

## Out of Scope

PR #255 identified a related issue: when peers are consulted, service routing
may select the failing CP node as the API server endpoint, causing false
"cluster-wide API issue" conclusions. This is pre-existing and orthogonal —
with the fix above, the isolated CP node no longer depends solely on the
worker peer consultation flow. Recommend filing a separate issue for the
service routing concern.
