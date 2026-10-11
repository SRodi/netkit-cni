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

request() {
  local pod=$1
  local url=$2
  local output
  for _ in $(seq 1 20); do
    if output=$(kubectl --context "${CONTEXT}" -n "${NS}" exec "${pod}" -- \
      python -c "import urllib.request; print(urllib.request.urlopen('${url}', timeout=3).read().decode())" 2>/dev/null); then
      printf '%s' "${output}"
      return 0
    fi
    sleep 1
  done
  return 1
}

assert_service_source() {
  local source=$1
  local service=$2
  local source_ip observed
  current_step="checking Service ${service} from ${source}"
  source_ip=$(pod_ip "${source}")
  observed=$(request "${source}" "http://${service}.${NS}.svc.cluster.local")
  if [[ "${observed}" != "${source_ip}" ]]; then
    echo "${source} -> ${service}: destination observed ${observed}, want ${source_ip}" >&2
    exit 1
  fi
  echo "PASS ${source} -> ${service}; DNS, ClusterIP, and source preservation"
}

assert_service_source same-a same-service
assert_service_source same-a cross-service

current_step="checking headless Service DNS and connectivity"
cross_b_ip=$(pod_ip cross-b)
headless_ip=$(kubectl --context "${CONTEXT}" -n "${NS}" exec same-a -- \
  python -c "import socket; print(socket.gethostbyname('cross-headless.${NS}.svc.cluster.local'))")
if [[ "${headless_ip}" != "${cross_b_ip}" ]]; then
  echo "headless Service resolved to ${headless_ip}, want ${cross_b_ip}" >&2
  exit 1
fi
headless_observed=$(request same-a "http://cross-headless.${NS}.svc.cluster.local:8080")
if [[ "${headless_observed}" != "$(pod_ip same-a)" ]]; then
  echo "headless Service endpoint observed ${headless_observed}, want $(pod_ip same-a)" >&2
  exit 1
fi
echo "PASS headless Service DNS and direct endpoint connectivity"

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
  docker exec "${node}" sh -ec "ip -details link show nk-vrf | grep -q 'vrf table 100'"
  docker exec "${node}" sh -ec "ip -o -4 address show dev nk-vrf | grep -Fq '169.254.100.1/32'"
  docker exec "${node}" sh -ec "! ip link show nk-main >/dev/null 2>&1"
  docker exec "${node}" sh -ec "! ip link show nk-vrf-x >/dev/null 2>&1"
  docker exec "${node}" sh -ec "test -z \"\$(ip -4 route show table 100 proto 98)\""
  docker exec "${node}" sh -ec "test \"\$(cat /proc/sys/net/ipv4/ip_forward)\" = 1"
  docker exec "${node}" sh -ec "test \"\$(cat /proc/sys/net/ipv4/tcp_l3mdev_accept)\" = 1"
  docker exec "${node}" sh -ec "test \"\$(cat /proc/sys/net/ipv4/udp_l3mdev_accept)\" = 1"

  pod_cidr=$(kubectl --context "${CONTEXT}" get node "${node}" -o jsonpath='{.spec.podCIDR}')
  docker exec "${node}" sh -ec "ip -4 rule show | grep -Fq 'to ${pod_cidr} lookup 100'"
  docker exec "${node}" sh -ec "test -z \"\$(ip -4 route show table main proto 98 ${pod_cidr})\""
  docker exec "${node}" iptables -t nat -C POSTROUTING -j NETKIT-CNI-EGRESS
  for destination_cidr in $(kubectl --context "${CONTEXT}" get nodes -o jsonpath='{range .items[*]}{.spec.podCIDR}{"\n"}{end}'); do
    docker exec "${node}" iptables -t nat -C NETKIT-CNI-EGRESS \
      -s "${pod_cidr}" -d "${destination_cidr}" -j RETURN
  done
  docker exec "${node}" iptables -t nat -C NETKIT-CNI-EGRESS \
    -s "${pod_cidr}" -j MASQUERADE
done

for node in "${CLUSTER}-worker" "${CLUSTER}-worker2"; do
  current_step="checking protocol-99 Pod host routes on ${node}"
  docker exec "${node}" sh -ec \
    "routes=\$(ip -o -4 route show table 100 proto 99); test -n \"\${routes}\"; printf '%s\n' \"\${routes}\" | awk '\$1 ~ /\\// { found_non_host=1 } END { exit found_non_host }'"
done

echo "All Pod, Service, DNS, route, netkit, VRF, source-preservation, and egress-NAT checks passed."
