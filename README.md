# Self Node Remediation - Automatic Kubernetes Node Remediation 
<p align="center">
<img width="200" src="config/assets/snr_icon_blue.png">
</p>

Existing baremetal remediation strategies utilize BMC credentials to power-cycle and/or reprovision the host.
However there are also environments that either do not include BMCs, or there are policies
in place that prevent them from being utilized.  Such environments would also benefit from
the ability to safely recover affected workloads and restore cluster capacity (where possible).
This self node remediation controller is using an alternate mechanism for a node in a cluster to detect its health
status and take actions to remediate itself in case of a failure.  While not all remediation events can
result in the node returning to a healthy state, the proposal does allow surviving parts of the cluster
to assume the node has reached a safe state so that it’s workloads can be automatically recovered.
This work can also be useful for clusters with BMC credentials.


## More Info
https://www.medik8s.io/

## Project State
The operator is available in [operator hub](https://operatorhub.io/operator/self-node-remediation)

Self Node Remediation works best together with [Node Health Check Operator](https://operatorhub.io/operator/node-healthcheck-operator)

### Deploying the current source to OpenShift

For PR or branch testing, build the operator with the pinned source-deployment
toolchain, push temporary images to `ttl.sh`, and install the generated OLM
bundle with operator-sdk:

```bash
make deploy-olm
```

The temporary images expire after one hour by default. Override the duration
and deployment namespace when needed, for example:

```bash
TTL_DURATION=4h OLM_OPERATOR_NAMESPACE=openshift-workload-availability make deploy-olm
make undeploy-olm
```

## Help
Feel free to join our google group to get more info - https://groups.google.com/g/medik8s
