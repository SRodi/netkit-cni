#!/usr/bin/env bash
set -euo pipefail

ROOT=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
CLUSTER=${KIND_CLUSTER:-netkit-cni}
CONTEXT="kind-${CLUSTER}"
NS=netkit-test
current_step="applying test workloads"

trap 'status=$?; printf "FAIL: E2E step %q failed at line %s\n" "${current_step}" "${BASH_LINENO[0]}" >&2; exit "${status}"' ERR

kubectl --context "${CONTEXT}" apply -f "${ROOT}/deploy/test-workloads.yaml"
current_step="waiting for test Pods"
if ! kubectl --context "${CONTEXT}" -n "${NS}" wait pod --all --for=condition=Ready --timeout=240s; then
  echo "Pods did not become Ready; collecting diagnostics..." >&2
  KIND_CLUSTER="${CLUSTER}" "${ROOT}/scripts/diagnose.sh" >&2
  exit 1
fi

pod_ip() {
  kubectl --context "${CONTEXT}" -n "${NS}" get pod "$1" -o jsonpath='{.status.podIP}'
}

assert_source_ip() {
  local source=$1
  local destination=$2
  local source_ip destination_ip observed
  current_step="checking source preservation from ${source} to ${destination}"
  source_ip=$(pod_ip "${source}")
  destination_ip=$(pod_ip "${destination}")
  observed=$(kubectl --context "${CONTEXT}" -n "${NS}" exec "${source}" -- \
    python -c "import urllib.request; print(urllib.request.urlopen('http://${destination_ip}:8080', timeout=5).read().decode())")
  if [[ "${observed}" != "${source_ip}" ]]; then
    echo "${source} -> ${destination}: destination observed ${observed}, want ${source_ip}; SNAT or routing is incorrect" >&2
    exit 1
  fi
  echo "PASS ${source} (${source_ip}) -> ${destination} (${destination_ip}); source preserved"
}

assert_source_ip same-a same-b
assert_source_ip same-b same-a
assert_source_ip cross-a cross-b
assert_source_ip cross-b cross-a

for node in "${CLUSTER}-control-plane" "${CLUSTER}-worker" "${CLUSTER}-worker2"; do
  current_step="inspecting ${node}"
  echo "=== ${node}: netkit ==="
  docker exec "${node}" ip -details link show type netkit
  echo "=== ${node}: VRF ==="
  docker exec "${node}" ip -details link show type vrf
  echo "=== ${node}: main protocol 98 routes ==="
  docker exec "${node}" ip -4 route show table main proto 98
  echo "=== ${node}: VRF table 100 ==="
  docker exec "${node}" ip -4 route show table 100
  docker exec "${node}" sh -ec "ip -details link show nk-main | grep -q 'netkit mode l3'"
  docker exec "${node}" sh -ec "ip -details link show nk-vrf-x | grep -q 'netkit mode l3'"
  docker exec "${node}" sh -ec "ip -details link show nk-vrf | grep -q 'vrf table 100'"
  docker exec "${node}" sh -ec "test \"\$(cat /proc/sys/net/ipv4/ip_forward)\" = 1"
  docker exec "${node}" sh -ec "test \"\$(cat /proc/sys/net/ipv4/tcp_l3mdev_accept)\" = 1"
  docker exec "${node}" sh -ec "test \"\$(cat /proc/sys/net/ipv4/udp_l3mdev_accept)\" = 1"

  pod_cidr=$(kubectl --context "${CONTEXT}" get node "${node}" -o jsonpath='{.spec.podCIDR}')
  docker exec "${node}" sh -ec "ip -4 rule show | grep -Fq 'to ${pod_cidr} lookup 100'"
done

for node in "${CLUSTER}-worker" "${CLUSTER}-worker2"; do
  current_step="checking protocol-99 Pod host routes on ${node}"
  docker exec "${node}" sh -ec \
    "routes=\$(ip -o -4 route show table 100 proto 99); test -n \"\${routes}\"; printf '%s\n' \"\${routes}\" | awk '\$1 ~ /\\// { found_non_host=1 } END { exit found_non_host }'"
done

echo "All same-node, cross-node, route, netkit, VRF, and no-SNAT checks passed."
