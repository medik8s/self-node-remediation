#!/usr/bin/env bash
# local-run.sh — Run the Kind E2E workflow on a developer laptop.
#
# This follows .github/workflows/kind-e2e.yaml: it creates a Kind cluster,
# installs NHC from its published bundle, builds and installs the local SNR
# bundle, applies the Kind watchdog safety settings, and runs ./e2e.
#
# Usage:
#   ./hack/local-run.sh              # Full setup, deployment, and E2E run
#   ./hack/local-run.sh --skip-setup # Reuse an existing Kind cluster
#   ./hack/local-run.sh --skip-build # Reuse existing operator installations
#   ./hack/local-run.sh --teardown   # Delete the Kind cluster
#
# The script intentionally leaves the cluster in place after a failure so that
# developers can inspect it. Use --teardown when finished.

set -Eeuo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
SNR_DIR="$(cd "${SCRIPT_DIR}/.." && pwd)"
TOOLS_DIR="${TOOLS_DIR:-${SNR_DIR}/../tools}"

export MEDIK8S_CLUSTER_NAME="${MEDIK8S_CLUSTER_NAME:-medik8s-ci}"
export MEDIK8S_REGISTRY_NAME="${MEDIK8S_REGISTRY_NAME:-kind-registry}"
export MEDIK8S_REGISTRY_PORT="${MEDIK8S_REGISTRY_PORT:-5000}"
export IMAGE_REGISTRY="${IMAGE_REGISTRY:-${MEDIK8S_REGISTRY_NAME}:${MEDIK8S_REGISTRY_PORT}}"
export OPM_RENDER_FLAGS="${OPM_RENDER_FLAGS:---skip-tls-verify}"
export DEPLOY_SNR_NAMESPACE="${DEPLOY_SNR_NAMESPACE:-snr-system}"
export DEPLOY_NHC_NAMESPACE="${DEPLOY_NHC_NAMESPACE:-k8s-test}"
export TOOLS_DIR

if [ -n "${CONTAINER_TOOL:-}" ]; then
    export CONTAINER_TOOL
elif command -v docker >/dev/null 2>&1; then
    export CONTAINER_TOOL=docker
elif command -v podman >/dev/null 2>&1; then
    export CONTAINER_TOOL=podman
else
    echo "Error: neither docker nor podman was found in PATH." >&2
    exit 1
fi

KUBECTL_BIN="${KUBECTL:-kubectl}"
export KUBECTL="${KUBECTL_BIN}"

NHC_BUNDLE="${NHC_BUNDLE:-quay.io/medik8s/node-healthcheck-operator-bundle:latest}"
SNR_IMG="${IMAGE_REGISTRY}/self-node-remediation-operator:latest"
SNR_BUNDLE="${IMAGE_REGISTRY}/self-node-remediation-operator-bundle:latest"
KIND_CONTEXT="kind-${MEDIK8S_CLUSTER_NAME}"
WATCHER_SCRIPT="${TOOLS_DIR}/dev/kind-reboot-watcher.sh"

SKIP_SETUP=false
SKIP_BUILD=false
TEARDOWN=false

while [[ $# -gt 0 ]]; do
    case "$1" in
        --skip-setup)
            SKIP_SETUP=true
            shift
            ;;
        --skip-build)
            SKIP_BUILD=true
            shift
            ;;
        --teardown)
            TEARDOWN=true
            shift
            ;;
        -h|--help)
            sed -n '2,18p' "$0"
            echo
            echo "Options:"
            echo "  --skip-setup   Reuse an existing Kind cluster"
            echo "  --skip-build   Reuse existing NHC and SNR installations"
            echo "  --teardown     Delete the Kind cluster and exit"
            echo
            echo "Environment variables:"
            echo "  MEDIK8S_CLUSTER_NAME   Kind cluster name (default: medik8s-ci)"
            echo "  CONTAINER_TOOL         docker or podman (auto-detected)"
            echo "  TOOLS_DIR              Shared medik8s tools directory"
            echo "  NHC_BUNDLE             NHC bundle image"
            echo "  DEPLOY_SNR_NAMESPACE   SNR namespace (default: snr-system)"
            echo "  DEPLOY_NHC_NAMESPACE   NHC namespace (default: k8s-test)"
            exit 0
            ;;
        *)
            echo "Unknown option: $1" >&2
            exit 1
            ;;
    esac
done

if [ ! -d "${TOOLS_DIR}" ]; then
    echo "Error: shared tools directory not found: ${TOOLS_DIR}" >&2
    echo "Set TOOLS_DIR to the medik8s/tools checkout." >&2
    exit 1
fi

for command_name in "${KUBECTL_BIN}" kind go "${CONTAINER_TOOL}"; do
    if ! command -v "${command_name}" >/dev/null 2>&1; then
        echo "Error: required command not found: ${command_name}" >&2
        exit 1
    fi
done

if [ ! -x "${WATCHER_SCRIPT}" ]; then
    echo "Error: reboot watcher not found or not executable: ${WATCHER_SCRIPT}" >&2
    exit 1
fi

step() {
    echo
    echo "========================================"
    echo "  $1"
    echo "========================================"
}

wait_for_resource() {
    local namespace="$1"
    local resource="$2"
    local timeout_seconds="$3"
    local elapsed=0

    echo "Waiting for ${resource} in ${namespace} (timeout ${timeout_seconds}s)..."
    while ! "${KUBECTL_BIN}" -n "${namespace}" get "${resource}" >/dev/null 2>&1; do
        if [ "${elapsed}" -ge "${timeout_seconds}" ]; then
            echo "Timed out waiting for ${resource} in ${namespace}." >&2
            return 1
        fi
        sleep 5
        elapsed=$((elapsed + 5))
    done
}

wait_for_cluster_resource() {
    local resource="$1"
    local timeout_seconds="$2"
    local elapsed=0

    echo "Waiting for ${resource} (timeout ${timeout_seconds}s)..."
    while ! "${KUBECTL_BIN}" get "${resource}" >/dev/null 2>&1; do
        if [ "${elapsed}" -ge "${timeout_seconds}" ]; then
            echo "Timed out waiting for ${resource}." >&2
            return 1
        fi
        sleep 5
        elapsed=$((elapsed + 5))
    done
}

wait_for_jsonpath() {
    local namespace="$1"
    local resource="$2"
    local jsonpath="$3"
    local expected="$4"
    local timeout_seconds="$5"
    local elapsed=0
    local actual

    echo "Waiting for ${resource} field to equal ${expected}..."
    while true; do
        actual=$("${KUBECTL_BIN}" -n "${namespace}" get "${resource}" -o "jsonpath=${jsonpath}" 2>/dev/null || true)
        if [ "${actual}" = "${expected}" ]; then
            return 0
        fi
        if [ "${elapsed}" -ge "${timeout_seconds}" ]; then
            echo "Timed out waiting for ${resource}: got '${actual}', expected '${expected}'." >&2
            return 1
        fi
        sleep 5
        elapsed=$((elapsed + 5))
    done
}

label_privileged_namespace() {
    local namespace="$1"
    "${KUBECTL_BIN}" create namespace "${namespace}" 2>/dev/null || true
    "${KUBECTL_BIN}" label --overwrite namespace "${namespace}" \
        pod-security.kubernetes.io/enforce=privileged \
        pod-security.kubernetes.io/audit=privileged \
        pod-security.kubernetes.io/warn=privileged
}

operator_sdk=""
watcher_pid=""
original_context=""
original_sysrq=""
sysrq_changed=false
debug_completed=false

debug_cluster() {
    if [ "${debug_completed}" = true ]; then
        return
    fi
    debug_completed=true

    step "Debugging failed run"
    "${KUBECTL_BIN}" get nodes -o wide 2>/dev/null || true
    echo ""
    echo "=== NHC Status ==="
    "${KUBECTL_BIN}" get nodehealthchecks -o yaml 2>/dev/null || true
    echo ""
    echo "=== SelfNodeRemediation CRs ==="
    "${KUBECTL_BIN}" get selfnoderemediations -A -o yaml 2>/dev/null || true
    echo ""
    echo "=== SNR Templates ==="
    "${KUBECTL_BIN}" get selfnoderemediationtemplates -A -o yaml 2>/dev/null || true
    echo ""
    echo "=== SNR Config ==="
    "${KUBECTL_BIN}" get selfnoderemediationconfig -n "${DEPLOY_SNR_NAMESPACE}" -o yaml 2>/dev/null || true
    echo ""
    echo "=== Pods ==="
    "${KUBECTL_BIN}" get pods -A -o wide 2>/dev/null || true
    echo ""
    echo "=== Recent Events ==="
    "${KUBECTL_BIN}" get events -A --sort-by=.lastTimestamp 2>/dev/null | tail -80 || true
    echo ""
    echo "=== SNR Controller Logs ==="
    "${KUBECTL_BIN}" logs -n "${DEPLOY_SNR_NAMESPACE}" \
        -l app.kubernetes.io/name=self-node-remediation \
        --all-containers --tail=150 2>/dev/null || true
    echo ""
    echo "=== NHC Controller Logs ==="
    "${KUBECTL_BIN}" logs -n "${DEPLOY_NHC_NAMESPACE}" \
        -l app.kubernetes.io/name=node-healthcheck-operator \
        --all-containers --tail=150 2>/dev/null || true
}

restore_host_safety() {
    if [ "${sysrq_changed}" = true ]; then
        echo "Restoring kernel.sysrq=${original_sysrq}."
        if [ "${EUID}" -eq 0 ]; then
            sysctl -w "kernel.sysrq=${original_sysrq}" >/dev/null || true
        else
            sudo sysctl -w "kernel.sysrq=${original_sysrq}" >/dev/null || true
        fi
    fi
}

cleanup() {
    local status=$?
    set +e

    if [ -n "${watcher_pid}" ] && kill -0 "${watcher_pid}" 2>/dev/null; then
        echo "Stopping Kind reboot watcher (PID ${watcher_pid})."
        kill "${watcher_pid}" 2>/dev/null || true
        wait "${watcher_pid}" 2>/dev/null || true
    fi

    if [ "${status}" -ne 0 ] && [ "${TEARDOWN}" = false ]; then
        debug_cluster
        echo "The cluster was left running for inspection. Use --teardown when finished." >&2
    fi

    restore_host_safety

    if [ -n "${original_context}" ] && [ "${original_context}" != "${KIND_CONTEXT}" ]; then
        "${KUBECTL_BIN}" config use-context "${original_context}" >/dev/null 2>&1 || true
    fi

    exit "${status}"
}
trap cleanup EXIT

if [ "${TEARDOWN}" = true ]; then
    step "Tearing down Kind cluster"
    cd "${SNR_DIR}"
    make dev-teardown
    exit 0
fi

original_context=$("${KUBECTL_BIN}" config current-context 2>/dev/null || true)

if [ "${SKIP_SETUP}" = false ]; then
    step "Installing operator-sdk"
    cd "${SNR_DIR}"
    make operator-sdk
else
    echo "Skipping Kind setup (--skip-setup)."
fi

if [ "${SKIP_BUILD}" = false ] && [ "${SKIP_SETUP}" = true ]; then
    step "Installing operator-sdk"
    cd "${SNR_DIR}"
    make operator-sdk
fi

operator_sdk_path=$(find "${SNR_DIR}/bin/operator-sdk" -type f -name operator-sdk -print -quit 2>/dev/null || true)
if [ "${SKIP_BUILD}" = false ] && [ -z "${operator_sdk_path}" ]; then
    echo "Error: operator-sdk binary was not found under ${SNR_DIR}/bin/operator-sdk." >&2
    exit 1
fi
if [ -n "${operator_sdk_path}" ]; then
    operator_sdk="${operator_sdk_path}"
    export PATH="$(dirname "${operator_sdk}"):${PATH}"
fi

step "Disabling host SysRq"
original_sysrq=$(sysctl -n kernel.sysrq)
if [ "${EUID}" -eq 0 ]; then
    sysctl -w kernel.sysrq=0 >/dev/null
else
    sudo sysctl -w kernel.sysrq=0 >/dev/null
fi
sysrq_changed=true
if [ "$(sysctl -n kernel.sysrq)" != "0" ]; then
    echo "Error: could not disable kernel.sysrq." >&2
    exit 1
fi

if [ "${SKIP_SETUP}" = false ]; then
    step "Creating Kind cluster with registry, OLM, and cert-manager"
    cd "${SNR_DIR}"
    # dev-setup reuses an existing cluster when present and then runs kubectl
    # against the current context. Select the Kind context first so a stale
    # OpenShift or other cluster context cannot trigger an auth prompt.
    if KIND_EXPERIMENTAL_PROVIDER="${CONTAINER_TOOL}" \
        kind get clusters 2>/dev/null | grep -qx "${MEDIK8S_CLUSTER_NAME}"; then
        "${KUBECTL_BIN}" config use-context "${KIND_CONTEXT}" >/dev/null
    fi
    make dev-setup
    make dev-cluster-info
fi

step "Selecting Kind context"
"${KUBECTL_BIN}" config use-context "${KIND_CONTEXT}"

if [ "${SKIP_BUILD}" = false ]; then
    step "Cleaning previous OLM installations"
    if "${KUBECTL_BIN}" get subscription -n "${DEPLOY_SNR_NAMESPACE}" \
        -o name 2>/dev/null | grep -q .; then
        "${operator_sdk}" -n "${DEPLOY_SNR_NAMESPACE}" cleanup self-node-remediation || true
    fi
    if "${KUBECTL_BIN}" get subscription -n "${DEPLOY_NHC_NAMESPACE}" \
        -o name 2>/dev/null | grep -q .; then
        "${operator_sdk}" -n "${DEPLOY_NHC_NAMESPACE}" cleanup node-healthcheck-operator --delete-all || true
    fi

    step "Deploying NHC via OLM bundle"
    label_privileged_namespace "${DEPLOY_NHC_NAMESPACE}"
    "${operator_sdk}" run bundle -n "${DEPLOY_NHC_NAMESPACE}" \
        --timeout 5m "${NHC_BUNDLE}"

    step "Building and pushing local SNR images"
    cd "${SNR_DIR}"
    IMG="${SNR_IMG}" BUNDLE_IMG="${SNR_BUNDLE}" make test
    IMG="${SNR_IMG}" BUNDLE_IMG="${SNR_BUNDLE}" make bundle
    "${CONTAINER_TOOL}" build -t "${SNR_IMG}" .
    "${CONTAINER_TOOL}" build -f bundle.Dockerfile -t "${SNR_BUNDLE}" .
    if [ "${CONTAINER_TOOL}" = podman ]; then
        "${CONTAINER_TOOL}" push --tls-verify=false "${SNR_IMG}"
        "${CONTAINER_TOOL}" push --tls-verify=false "${SNR_BUNDLE}"
    else
        "${CONTAINER_TOOL}" push "${SNR_IMG}"
        "${CONTAINER_TOOL}" push "${SNR_BUNDLE}"
    fi

    step "Deploying local SNR via OLM bundle"
    label_privileged_namespace "${DEPLOY_SNR_NAMESPACE}"
    "${operator_sdk}" run bundle -n "${DEPLOY_SNR_NAMESPACE}" --use-http \
        --timeout 5m "${SNR_BUNDLE}"

    # Safety net: explicit RBAC binding in case ClusterRole aggregation is slow.
    "${KUBECTL_BIN}" create clusterrolebinding nhc-snr-admin-binding \
        --clusterrole=self-node-remediation-ext-remediation \
        --serviceaccount="${DEPLOY_NHC_NAMESPACE}:node-healthcheck-controller-manager" \
        2>/dev/null || true
else
    echo "Skipping build and deployment (--skip-build)."
fi

step "Using SNR software reboot in Kind"
wait_for_resource "${DEPLOY_SNR_NAMESPACE}" \
    selfnoderemediationconfig/self-node-remediation-config 120
# Kind workers share the host kernel, so /dev/watchdog would be one softdog
# device shared by every SNR agent. Use a non-watchdog device so the agents
# exercise their software-reboot fallback instead.
"${KUBECTL_BIN}" -n "${DEPLOY_SNR_NAMESPACE}" patch selfnoderemediationconfig \
    self-node-remediation-config --type=merge \
    -p '{"spec":{"isSoftwareRebootEnabled":true,"watchdogFilePath":"/dev/null"}}'
wait_for_resource "${DEPLOY_SNR_NAMESPACE}" \
    daemonset/self-node-remediation-ds 120
wait_for_jsonpath "${DEPLOY_SNR_NAMESPACE}" daemonset/self-node-remediation-ds \
    '{.spec.template.spec.containers[0].env[?(@.name=="IS_SOFTWARE_REBOOT_ENABLED")].value}' \
    true 120
"${KUBECTL_BIN}" -n "${DEPLOY_SNR_NAMESPACE}" rollout status \
    daemonset/self-node-remediation-ds --timeout=120s

step "Waiting for operators and RBAC aggregation"
cd "${SNR_DIR}"
make dev-wait
wait_for_cluster_resource clusterrole/node-healthcheck-operator-aggregation 120
for i in $(seq 1 60); do
    rules=$("${KUBECTL_BIN}" get clusterrole/node-healthcheck-operator-aggregation \
        -o jsonpath='{.rules}' 2>/dev/null || true)
    if [ -n "${rules}" ] && [ "${rules}" != "null" ] && [ "${rules}" != "[]" ]; then
        echo "RBAC aggregation complete."
        break
    fi
    if [ "${i}" -eq 60 ]; then
        echo "RBAC aggregation did not populate rules within 60 seconds." >&2
        "${KUBECTL_BIN}" get clusterrole/node-healthcheck-operator-aggregation -o yaml || true
        exit 1
    fi
    sleep 1
done

step "Starting Kind reboot watcher"
MEDIK8S_REBOOT_DELAY="${MEDIK8S_REBOOT_DELAY:-30}" \
    "${WATCHER_SCRIPT}" --name "${MEDIK8S_CLUSTER_NAME}" &
watcher_pid=$!

step "Running SNR E2E tests"
OPERATOR_NS="${DEPLOY_SNR_NAMESPACE}" \
SNR_STRATEGY=OutOfServiceTaint \
LABEL_FILTER='!OCP-ONLY' \
make e2e-test

echo
echo "========================================"
echo "  All SNR E2E tests passed"
echo "========================================"
