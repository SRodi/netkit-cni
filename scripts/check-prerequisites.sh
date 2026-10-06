#!/usr/bin/env bash
set -uo pipefail

MIN_KERNEL=6.8
MIN_GO=1.26
failed=0
netkit_link="nkpre$$"
vrf_link="vrfpre$$"
declare -A kernel_config

pass() {
  printf 'PASS: %s\n' "$1"
}

fail() {
  printf 'FAIL: %s\n' "$1" >&2
  failed=1
}

kernel_remediation() {
  local option=$1
  local module=$2
  local value=${kernel_config[${option}]:-unknown}

  case "${value}" in
    m)
      printf 'NOTE: %s=m, but the %s module could not provide the device.\n' "${option}" "${module}" >&2
      printf '      Try: sudo modprobe %s\n' "${module}" >&2
      if command -v apt-get >/dev/null 2>&1; then
        printf '      On Ubuntu, install the matching module package:\n' >&2
        printf '        sudo apt-get update\n' >&2
        printf '        sudo apt-get install -y "linux-modules-extra-$(uname -r)"\n' >&2
        printf '        sudo depmod -a && sudo modprobe %s\n' "${module}" >&2
      fi
      ;;
    y)
      printf 'NOTE: %s=y, so the feature should be built in; verify that this is the active kernel and inspect its boot configuration.\n' "${option}" >&2
      ;;
    *)
      printf 'NOTE: Install and boot a kernel with %s=y or %s=m, then rerun this check.\n' "${option}" "${option}" >&2
      ;;
  esac
}

version_at_least() {
  local actual=$1
  local minimum=$2
  [[ $(printf '%s\n%s\n' "${minimum}" "${actual}" | sort -V | head -n1) == "${minimum}" ]]
}

if [[ $(uname -s) != Linux ]]; then
  fail "Linux is required; found $(uname -s)"
  exit 1
fi

if [[ ${EUID} -eq 0 ]]; then
  privilege=()
elif command -v sudo >/dev/null 2>&1; then
  privilege=(sudo)
  if ! sudo -n true 2>/dev/null; then
    printf 'INFO: sudo authentication is required for the kernel feature probes.\n'
    if ! sudo -v; then
      fail "sudo authentication failed"
      exit 1
    fi
  fi
else
  fail "run as root or install sudo so temporary network devices can be created"
  exit 1
fi

cleanup() {
  "${privilege[@]}" ip link del "${netkit_link}" 2>/dev/null || true
  "${privilege[@]}" ip link del "${vrf_link}" 2>/dev/null || true
}
trap cleanup EXIT

kernel_release=$(uname -r)
kernel_version=${kernel_release%%-*}
if version_at_least "${kernel_version}" "${MIN_KERNEL}"; then
  pass "kernel ${kernel_release} is at least ${MIN_KERNEL}"
else
  fail "kernel ${kernel_release} is older than ${MIN_KERNEL}"
fi

config_file=
if [[ -r /proc/config.gz ]] && command -v gzip >/dev/null 2>&1; then
  config_file=/proc/config.gz
  config_output=$(gzip -cd /proc/config.gz)
elif [[ -r "/boot/config-${kernel_release}" ]]; then
  config_file="/boot/config-${kernel_release}"
  config_output=$(cat "${config_file}")
elif [[ -r "/lib/modules/${kernel_release}/build/.config" ]]; then
  config_file="/lib/modules/${kernel_release}/build/.config"
  config_output=$(cat "/lib/modules/${kernel_release}/build/.config")
else
  config_output=
  printf 'INFO: kernel configuration is not exposed through /proc/config.gz, /boot, or the module build tree.\n'
fi

if [[ -n "${config_file}" ]]; then
  printf 'INFO: reading kernel configuration from %s\n' "${config_file}"
  for option in CONFIG_NETKIT CONFIG_NET_VRF CONFIG_NET_L3_MASTER_DEV; do
    value=$(grep -E "^${option}=" <<<"${config_output}" | cut -d= -f2-)
    kernel_config[${option}]=${value}
    case "${value}" in
      y)
        pass "${option}=y"
        ;;
      m)
        pass "${option}=m"
        ;;
      *)
        fail "${option} is not enabled"
        ;;
    esac
  done
fi

printf '\nAuthoritative runtime tests (temporary devices are removed automatically):\n'

if command -v modprobe >/dev/null 2>&1; then
  if [[ ${kernel_config[CONFIG_NETKIT]:-unknown} == m ]]; then
    if module_error=$("${privilege[@]}" modprobe netkit 2>&1); then
      pass "loaded the netkit module"
    else
      printf 'INFO: modprobe netkit failed: %s\n' "${module_error}" >&2
    fi
  fi
  if [[ ${kernel_config[CONFIG_NET_VRF]:-unknown} == m ]]; then
    if module_error=$("${privilege[@]}" modprobe vrf 2>&1); then
      pass "loaded the vrf module"
    else
      printf 'INFO: modprobe vrf failed: %s\n' "${module_error}" >&2
    fi
  fi
fi

if ! command -v ip >/dev/null 2>&1; then
  fail "ip is missing; install the iproute2 package"
  printf 'NOTE: Install iproute2 and rerun this script; kernel configuration alone is not sufficient proof.\n' >&2
else
  if netkit_error=$("${privilege[@]}" ip link add "${netkit_link}" type netkit 2>&1); then
    pass "authoritative netkit device creation"
    "${privilege[@]}" ip link del "${netkit_link}"
  else
    fail "authoritative netkit device creation: ${netkit_error}"
    kernel_remediation CONFIG_NETKIT netkit
  fi

  if vrf_error=$("${privilege[@]}" ip link add "${vrf_link}" type vrf table 4242 2>&1); then
    pass "authoritative VRF device creation"
    "${privilege[@]}" ip link del "${vrf_link}"
  else
    fail "authoritative VRF device creation: ${vrf_error}"
    kernel_remediation CONFIG_NET_VRF vrf
  fi
fi

required_commands=(bash docker find go grep head kind kubectl make sed sha256sum sort)
for command_name in "${required_commands[@]}"; do
  if command -v "${command_name}" >/dev/null 2>&1; then
    pass "${command_name} is installed"
  else
    fail "${command_name} is missing"
  fi
done

if command -v go >/dev/null 2>&1; then
  go_version=$(go env GOVERSION 2>/dev/null | sed 's/^go//')
  if [[ -n "${go_version}" ]] && version_at_least "${go_version}" "${MIN_GO}"; then
    pass "Go ${go_version} is at least ${MIN_GO}"
  else
    fail "Go ${go_version:-unknown} is older than ${MIN_GO}"
  fi
fi

if command -v docker >/dev/null 2>&1; then
  if docker info >/dev/null 2>&1; then
    pass "Docker daemon is reachable"
  else
    fail "Docker daemon is not reachable by the current user"
  fi
fi

if ((failed)); then
  cat >&2 <<'EOF'

Prerequisite checks failed. Useful installation references:
  Docker:  https://docs.docker.com/engine/install/
  Go:      https://go.dev/doc/install
  kind:    https://kind.sigs.k8s.io/docs/user/quick-start/#installation
  kubectl: https://kubernetes.io/docs/tasks/tools/install-kubectl-linux/

For kernel support, install or boot a kernel that enables:
  CONFIG_NETKIT=y|m
  CONFIG_NET_VRF=y|m
  CONFIG_NET_L3_MASTER_DEV=y

The device-creation probes above are authoritative. A kernel version or
configuration entry by itself does not prove that a configured module is
installed and loadable.
EOF
  exit 1
fi

cat <<'EOF'

All prerequisites passed.

Next:
  make check
  sudo make integration
  make kind-up
  make e2e
  make kind-down
EOF
