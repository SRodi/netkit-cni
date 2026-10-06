#!/usr/bin/env bash
set -euo pipefail

if [[ ${EUID} -ne 0 ]]; then
  echo "run as root (for example: sudo make integration)" >&2
  exit 1
fi
if [[ $(uname -s) != Linux ]]; then
  echo "the netns integration test requires Linux" >&2
  exit 1
fi

ROOT=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
NS="netkit-cni-test-$$"
CID="integration-$$"
VRF="nk-vrf"
DATA_DIR=$(mktemp -d)
RESULT=$(mktemp)
CHECK_CONFIG="${DATA_DIR}/check.json"
current_step="initialization"

cleanup() {
  if [[ -f "${DATA_DIR}/config.json" ]]; then
    CNI_COMMAND=DEL \
    CNI_CONTAINERID="${CID}" \
    CNI_NETNS="/var/run/netns/${NS}" \
    CNI_IFNAME=eth0 \
    CNI_PATH="${ROOT}/bin" \
      "${ROOT}/bin/netkit-cni" <"${DATA_DIR}/config.json" >/dev/null 2>&1 || true
  fi
  ip netns del "${NS}" 2>/dev/null || true
  ip link del "${VRF}" 2>/dev/null || true
  rm -f "${RESULT}"
  rm -rf "${DATA_DIR}"
}

report_error() {
  local status=$1
  local line=$2
  local command=$3

  printf 'FAIL: integration step %q failed at line %s: %s\n' \
    "${current_step}" "${line}" "${command}" >&2
  if [[ -s "${RESULT}" ]]; then
    echo "CNI result captured before the failure:" >&2
    cat "${RESULT}" >&2
  fi
  exit "${status}"
}

trap cleanup EXIT
trap 'report_error "$?" "$LINENO" "$BASH_COMMAND"' ERR

current_step="creating the test VRF and network namespace"
ip link add "${VRF}" type vrf table 100
ip link set "${VRF}" up
ip netns add "${NS}"

cat >"${DATA_DIR}/config.json" <<EOF
{
  "cniVersion": "1.1.0",
  "name": "integration",
  "type": "netkit-cni",
  "vrfName": "${VRF}",
  "vrfTable": 100,
  "ipam": {
    "type": "host-local",
    "ranges": [[{"subnet": "10.250.0.0/24"}]],
    "dataDir": "${DATA_DIR}/ipam"
  }
}
EOF

current_step="invoking CNI ADD"
CNI_COMMAND=ADD \
CNI_CONTAINERID="${CID}" \
CNI_NETNS="/var/run/netns/${NS}" \
CNI_IFNAME=eth0 \
CNI_PATH="${ROOT}/bin" \
  "${ROOT}/bin/netkit-cni" <"${DATA_DIR}/config.json" >"${RESULT}"

current_step="parsing the CNI result"
HOST_IF=$(printf '%s' "${CID}" | sha256sum | cut -c1-12)
HOST_IF="nkh${HOST_IF}"
POD_IP=$(sed -n 's/.*"address"[[:space:]]*:[[:space:]]*"\([^/"]*\)\/32".*/\1/p' "${RESULT}")
if [[ -z ${POD_IP} ]]; then
  echo "CNI result does not contain one IPv4 /32 address:" >&2
  cat "${RESULT}" >&2
  exit 1
fi

current_step="checking the host netkit and VRF membership"
{
  sed '$d' "${DATA_DIR}/config.json"
  printf ',\n  "prevResult": '
  cat "${RESULT}"
  printf '}\n'
} >"${CHECK_CONFIG}"

current_step="validating the complete datapath through CNI CHECK"
CNI_COMMAND=CHECK \
CNI_CONTAINERID="${CID}" \
CNI_NETNS="/var/run/netns/${NS}" \
CNI_IFNAME=eth0 \
CNI_PATH="${ROOT}/bin" \
  "${ROOT}/bin/netkit-cni" <"${CHECK_CONFIG}"

current_step="checking the Pod netkit address and default route"
ip -n "${NS}" -4 address show dev eth0 | grep -q "${POD_IP}/32"
ip -n "${NS}" -4 route show default | grep -q '^default dev eth0'

current_step="checking the Pod route in VRF table 100"
ip -4 route show table 100 "${POD_IP}/32" | grep -q "dev ${HOST_IF}"

current_step="deleting a missing network namespace through CNI DEL"
ip netns del "${NS}"
CNI_COMMAND=DEL \
CNI_CONTAINERID="${CID}" \
CNI_NETNS="/var/run/netns/${NS}" \
CNI_IFNAME=eth0 \
CNI_PATH="${ROOT}/bin" \
  "${ROOT}/bin/netkit-cni" <"${DATA_DIR}/config.json"

current_step="checking host-local allocation cleanup"
if find "${DATA_DIR}/ipam" -type f \
  ! -name lock \
  ! -name 'last_reserved_ip.*' \
  -print -quit | grep -q .; then
  echo "host-local allocation remained after DEL with a missing netns" >&2
  exit 1
fi
echo "PASS: L3 netkit, /32 address, device default route, VRF /32 route, and missing-netns DEL"
