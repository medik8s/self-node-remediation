#!/usr/bin/env bash
# kind-reboot-watcher.sh — Simulate a node reboot for the local Kind E2E run.
#
# A Kind node is a container sharing the Podman machine's kernel, so the SNR
# software reboot cannot reboot the node kernel. Watch for SNR reaching the
# pre-reboot state, then restart the corresponding Kind node container.

set -Eeuo pipefail

CLUSTER_NAME="${MEDIK8S_CLUSTER_NAME:-medik8s-ci}"
SNR_NAMESPACE="${DEPLOY_SNR_NAMESPACE:-snr-system}"
CONTAINER_TOOL="${CONTAINER_TOOL:-podman}"
KUBECTL="${KUBECTL:-kubectl}"
REBOOT_DELAY="${MEDIK8S_REBOOT_DELAY:-30}"
POLL_INTERVAL=5

while [[ $# -gt 0 ]]; do
    case "$1" in
        --name)
            CLUSTER_NAME="$2"
            shift 2
            ;;
        --delay)
            REBOOT_DELAY="$2"
            shift 2
            ;;
        -h|--help)
            echo "Usage: $0 [--name <cluster>] [--delay <seconds>]"
            echo ""
            echo "Restarts a Kind node container when SNR reaches its pre-reboot state."
            exit 0
            ;;
        *)
            echo "Unknown option: $1" >&2
            exit 1
            ;;
    esac
done

declare -A HANDLED

get_phase() {
    local node="$1"
    "${KUBECTL}" -n "${SNR_NAMESPACE}" get selfnoderemediation "${node}" \
        -o jsonpath='{.status.phase}' 2>/dev/null || true
}

get_assumed_reboot_time() {
    local node="$1"
    "${KUBECTL}" -n "${SNR_NAMESPACE}" get selfnoderemediation "${node}" \
        -o jsonpath='{.status.timeAssumedRebooted}' 2>/dev/null || true
}

is_kind_node() {
    local node="$1"
    KIND_EXPERIMENTAL_PROVIDER="${CONTAINER_TOOL}" \
        kind get nodes --name "${CLUSTER_NAME}" 2>/dev/null | grep -Fxq "${node}"
}

echo "[reboot-watcher] Watching SNR reboot state for Kind cluster '${CLUSTER_NAME}' (delay: ${REBOOT_DELAY}s)"
echo "[reboot-watcher] Container tool: ${CONTAINER_TOOL}"

while true; do
    if ! KIND_EXPERIMENTAL_PROVIDER="${CONTAINER_TOOL}" \
        kind get clusters 2>/dev/null | grep -Fxq "${CLUSTER_NAME}"; then
        echo "[reboot-watcher] Cluster '${CLUSTER_NAME}' gone, exiting."
        exit 0
    fi

    nodes=$("${KUBECTL}" -n "${SNR_NAMESPACE}" get selfnoderemediation \
        -o jsonpath='{range .items[*]}{.metadata.name}{"\n"}{end}' 2>/dev/null || true)
    for node in ${nodes}; do
        phase=$(get_phase "${node}")
        assumed_reboot_time=$(get_assumed_reboot_time "${node}")

        if [ "${phase}" != "Pre-Reboot-Completed" ] || [ -z "${assumed_reboot_time}" ]; then
            unset "HANDLED[${node}]"
            continue
        fi
        if [[ -n "${HANDLED[$node]:-}" ]]; then
            continue
        fi
        if ! is_kind_node "${node}" || ! "${CONTAINER_TOOL}" inspect "${node}" >/dev/null 2>&1; then
            echo "[reboot-watcher] Warning: SNR requested reboot for '${node}', but no Kind container was found." >&2
            HANDLED["${node}"]=1
            continue
        fi

        HANDLED["${node}"]=1
        echo "[reboot-watcher] ${node}: SNR reached pre-reboot state; waiting ${REBOOT_DELAY}s before simulated reboot..."
        sleep "${REBOOT_DELAY}"

        phase=$(get_phase "${node}")
        if [ "${phase}" = "Pre-Reboot-Completed" ]; then
            echo "[reboot-watcher] ${node}: restarting container (simulated reboot)."
            "${CONTAINER_TOOL}" restart "${node}"
            echo "[reboot-watcher] ${node}: container restarted."
        else
            echo "[reboot-watcher] ${node}: SNR state changed to '${phase}', skipping restart."
        fi
    done

    sleep "${POLL_INTERVAL}"
done
