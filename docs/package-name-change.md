# Package Name Change Migration Guide

## Summary

Beginning with version **5.9.0**, the Self Node Remediation (SNR) community operator's OLM
package name changed from `self-node-remediation` to **`medik8s-self-node-remediation`**.

## Why this changed

On an OpenShift cluster, there are multiple CatalogSources, each of which includes a different
catalog with a set of OLM operators. The Medik8s operators have been released to the Red Hat
catalog and the [Community OKD](https://okd.io/) catalog using the same OLM package name. OLM
and tooling such as `packagemanifests` cannot reliably distinguish the operators' variants from
different catalogs, even when they share the same name and CRDs. Prefixing the package name with
`medik8s-` makes it unique, so the community operator can always be resolved unambiguously,
regardless of which catalog source is queried.

## Breaking change

This is a **breaking change** for users of `self-node-remediation` through OLM. Because OLM
treats different package names as entirely separate operators, there is no automatic upgrade path
from `self-node-remediation` to `medik8s-self-node-remediation`: uninstall the old operator and
install the renamed package as a fresh installation.

Before starting this migration, make sure there is no active remediation in progress. Performing
the migration during remediation can leave a node in an inconsistent state.

## Migration steps

The examples below assume the operator is installed in the `openshift-workload-availability`
namespace. Adjust the namespace to match your installation.

### Step 1 — Remove the old Subscription

```bash
# Find the old Subscription (package: self-node-remediation)
kubectl get subscription -n openshift-workload-availability

# Delete it (replace <subscription-name> with the name from the output above)
kubectl delete subscription <subscription-name> -n openshift-workload-availability
```

### Step 2 — Remove the old ClusterServiceVersion (CSV)

Deleting the Subscription does not remove the installed CSV. Delete the CSV whose name starts
with `self-node-remediation.v...`:

```bash
# Find the old CSV
kubectl get csv -n openshift-workload-availability | grep self-node-remediation

# Delete it (replace <csv-name>, for example self-node-remediation.v5.8.0)
kubectl delete csv <csv-name> -n openshift-workload-availability
```

### Step 3 — Install the renamed package

**Option A — OpenShift Console:**

Go to **Operators → OperatorHub** (Software Catalog), search for **"Self Node Remediation"**,
and open the community entry. Before installing, confirm the package name is
**`medik8s-self-node-remediation`**, then install it into the
`openshift-workload-availability` namespace.

**Option B — CLI:**

Apply a new Subscription that references the renamed package:

```yaml
apiVersion: operators.coreos.com/v1alpha1
kind: Subscription
metadata:
  name: medik8s-self-node-remediation
  namespace: openshift-workload-availability
spec:
  channel: stable
  name: medik8s-self-node-remediation
  source: community-operators
  sourceNamespace: openshift-marketplace
  installPlanApproval: Automatic
```

```bash
kubectl apply -f subscription.yaml
```

### Step 4 — Verify the installation

```bash
# The new CSV should be present and in the Succeeded phase.
kubectl get csv -n openshift-workload-availability | grep medik8s-self-node-remediation

# The controller and agent pods should be Running.
kubectl get pods -n openshift-workload-availability
```
