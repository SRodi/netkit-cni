#!/usr/bin/env bash
set -uo pipefail

CLUSTER=${KIND_CLUSTER:-netkit-cni}
CONTEXT="kind-${CLUSTER}"
NS=netkit-test

section() {
  printf '\n===== %s =====\n' "$1"
}

section "test Pods"
kubectl --context "${CONTEXT}" -n "${NS}" get pods -o wide 2>&1 || true

section "test Pod events"
kubectl --context "${CONTEXT}" -n "${NS}" describe pods 2>&1 || true

section "test Pod logs and local server checks"
for pod in same-a same-b cross-a cross-b; do
  echo "--- ${pod}: logs ---"
  kubectl --context "${CONTEXT}" -n "${NS}" logs "${pod}" --tail=50 2>&1 || true
  echo "--- ${pod}: localhost:8080 ---"
  kubectl --context "${CONTEXT}" -n "${NS}" exec "${pod}" -- \
    python -c 'import urllib.request; print(urllib.request.urlopen("http://127.0.0.1:8080", timeout=2).read().decode())' \
    2>&1 || true
  echo "--- ${pod}: kernel route table ---"
  kubectl --context "${CONTEXT}" -n "${NS}" exec "${pod}" -- cat /proc/net/route 2>&1 || true
done

section "agent logs"
kubectl --context "${CONTEXT}" -n kube-system logs daemonset/netkit-agent -c agent \
  --tail=100 --prefix 2>&1 || true

for node in "${CLUSTER}-control-plane" "${CLUSTER}-worker" "${CLUSTER}-worker2"; do
  section "${node}: links and addresses"
  docker exec "${node}" ip -brief link 2>&1 || true
  docker exec "${node}" ip -brief -4 address 2>&1 || true

  section "${node}: routes"
  docker exec "${node}" ip -4 route show table main 2>&1 || true
  docker exec "${node}" ip -4 route show table 100 2>&1 || true
  docker exec "${node}" ip -4 rule show 2>&1 || true

  section "${node}: forwarding state"
  docker exec "${node}" sh -c \
    'for path in /proc/sys/net/ipv4/ip_forward /proc/sys/net/ipv4/tcp_l3mdev_accept /proc/sys/net/ipv4/udp_l3mdev_accept; do printf "%s=%s\n" "$path" "$(cat "$path")"; done; for dev in all default nk-vrf; do for setting in forwarding rp_filter; do path="/proc/sys/net/ipv4/conf/$dev/$setting"; test -e "$path" && printf "%s=%s\n" "$path" "$(cat "$path")"; done; done' \
    2>&1 || true

  section "${node}: route lookups for test Pods"
  for pod in same-a same-b cross-a cross-b; do
    pod_ip=$(kubectl --context "${CONTEXT}" -n "${NS}" get pod "${pod}" -o jsonpath='{.status.podIP}' 2>/dev/null)
    if [[ -n ${pod_ip} ]]; then
      printf '%s (%s):\n' "${pod}" "${pod_ip}"
      docker exec "${node}" ip -4 route get "${pod_ip}" 2>&1 || true
      docker exec "${node}" ip vrf exec nk-vrf ip -4 route get "${pod_ip}" 2>&1 || true
    fi
  done

  section "${node}: neighbors"
  docker exec "${node}" ip -4 neighbor show 2>&1 || true
done
