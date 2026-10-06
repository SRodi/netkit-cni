#!/usr/bin/env bash
set -euo pipefail

GO_VERSION=${GO_VERSION:-1.26.2}
KIND_VERSION=${KIND_VERSION:-0.33.0}
KUBECTL_VERSION=${KUBECTL_VERSION:-1.37.0}

version_at_least() {
  local actual=$1
  local minimum=$2
  [[ $(printf '%s\n%s\n' "${minimum}" "${actual}" | sort -V | head -n1) == "${minimum}" ]]
}

if [[ $(uname -s) != Linux ]]; then
  echo "this installer requires Linux" >&2
  exit 1
fi

if [[ ! -r /etc/os-release ]]; then
  echo "cannot identify the Linux distribution: /etc/os-release is missing" >&2
  exit 1
fi

. /etc/os-release
if [[ ${ID:-} != ubuntu ]]; then
  echo "this installer supports Ubuntu only; found ${ID:-unknown}" >&2
  exit 1
fi

if [[ ${EUID} -ne 0 ]]; then
  echo "run this installer with sudo:" >&2
  echo "  sudo ./scripts/install-prerequisites-ubuntu.sh" >&2
  exit 1
fi
privilege=()
target_user=${SUDO_USER:-root}

case $(uname -m) in
  x86_64)
    arch=amd64
    ;;
  aarch64 | arm64)
    arch=arm64
    ;;
  *)
    echo "unsupported architecture: $(uname -m)" >&2
    exit 1
    ;;
esac

workdir=$(mktemp -d)
cleanup() {
  rm -rf "${workdir}"
}
trap cleanup EXIT

download_verified() {
  local url=$1
  local checksum_url=$2
  local output=$3
  local expected

  curl -fsSL "${url}" -o "${output}"
  expected=$(curl -fsSL "${checksum_url}" | awk '{print $1}')
  if [[ ! ${expected} =~ ^[0-9a-fA-F]{64}$ ]]; then
    echo "invalid checksum received from ${checksum_url}" >&2
    exit 1
  fi
  printf '%s  %s\n' "${expected}" "${output}" | sha256sum --check -
}

download_with_checksum() {
  local url=$1
  local expected=$2
  local output=$3

  if [[ ! ${expected} =~ ^[0-9a-fA-F]{64}$ ]]; then
    echo "invalid checksum for ${url}" >&2
    exit 1
  fi
  curl -fsSL "${url}" -o "${output}"
  printf '%s  %s\n' "${expected}" "${output}" | sha256sum --check -
}

echo "Installing base packages, including make..."
"${privilege[@]}" apt-get update
"${privilege[@]}" apt-get install -y \
  ca-certificates \
  coreutils \
  curl \
  findutils \
  git \
  gnupg \
  gzip \
  iproute2 \
  kmod \
  make

if [[ $(grep -E '^CONFIG_NET_VRF=' "/boot/config-$(uname -r)" 2>/dev/null | cut -d= -f2-) == m ]] &&
  ! "${privilege[@]}" modprobe vrf 2>/dev/null; then
  modules_extra="linux-modules-extra-$(uname -r)"
  if apt-cache show "${modules_extra}" >/dev/null 2>&1; then
    echo "Installing the matching VRF module package..."
    "${privilege[@]}" apt-get install -y "${modules_extra}"
    "${privilege[@]}" depmod -a
    "${privilege[@]}" modprobe vrf
  else
    echo "VRF is modular but ${modules_extra} is unavailable." >&2
    echo "Install and boot an Ubuntu kernel that provides its matching extra-modules package." >&2
    exit 1
  fi
fi

if ! command -v docker >/dev/null 2>&1; then
  echo "Installing Docker Engine from Docker's Ubuntu repository..."
  "${privilege[@]}" install -m 0755 -d /etc/apt/keyrings
  curl -fsSL https://download.docker.com/linux/ubuntu/gpg |
    "${privilege[@]}" tee /etc/apt/keyrings/docker.asc >/dev/null
  "${privilege[@]}" chmod a+r /etc/apt/keyrings/docker.asc
  printf 'deb [arch=%s signed-by=/etc/apt/keyrings/docker.asc] https://download.docker.com/linux/ubuntu %s stable\n' \
    "$(dpkg --print-architecture)" "${VERSION_CODENAME}" |
    "${privilege[@]}" tee /etc/apt/sources.list.d/docker.list >/dev/null
  "${privilege[@]}" apt-get update
  "${privilege[@]}" apt-get install -y \
    containerd.io \
    docker-buildx-plugin \
    docker-ce \
    docker-ce-cli \
    docker-compose-plugin
fi

"${privilege[@]}" systemctl enable --now docker
if [[ ${target_user} != root ]] && ! id -nG "${target_user}" | grep -qw docker; then
  "${privilege[@]}" usermod -aG docker "${target_user}"
  docker_group_changed=1
else
  docker_group_changed=0
fi

installed_go_version=
if command -v go >/dev/null 2>&1; then
  installed_go_version=$(go env GOVERSION 2>/dev/null | sed 's/^go//')
fi
if [[ -z ${installed_go_version} ]] || ! version_at_least "${installed_go_version}" "${GO_VERSION}"; then
  echo "Installing Go ${GO_VERSION}..."
  go_archive="${workdir}/go.tar.gz"
  go_metadata="${workdir}/go-downloads.json"
  go_filename="go${GO_VERSION}.linux-${arch}.tar.gz"
  go_url="https://go.dev/dl/${go_filename}"
  curl -fsSL 'https://go.dev/dl/?mode=json&include=all' -o "${go_metadata}"
  go_checksum=$(awk -v filename="${go_filename}" '
      index($0, "\"filename\": \"" filename "\"") { found = 1 }
      found && !printed && /"sha256":/ {
        gsub(/[",]/, "", $2)
        print $2
        printed = 1
      }
    ' "${go_metadata}")
  download_with_checksum "${go_url}" "${go_checksum}" "${go_archive}"
  "${privilege[@]}" rm -rf /usr/local/go
  "${privilege[@]}" tar -C /usr/local -xzf "${go_archive}"
  "${privilege[@]}" ln -sf /usr/local/go/bin/go /usr/local/bin/go
  "${privilege[@]}" ln -sf /usr/local/go/bin/gofmt /usr/local/bin/gofmt
fi

echo "Installing kind ${KIND_VERSION}..."
kind_binary="${workdir}/kind"
kind_url="https://github.com/kubernetes-sigs/kind/releases/download/v${KIND_VERSION}/kind-linux-${arch}"
download_verified "${kind_url}" "${kind_url}.sha256sum" "${kind_binary}"
"${privilege[@]}" install -m 0755 "${kind_binary}" /usr/local/bin/kind

echo "Installing kubectl ${KUBECTL_VERSION}..."
kubectl_binary="${workdir}/kubectl"
kubectl_url="https://dl.k8s.io/release/v${KUBECTL_VERSION}/bin/linux/${arch}/kubectl"
download_verified "${kubectl_url}" "${kubectl_url}.sha256" "${kubectl_binary}"
"${privilege[@]}" install -m 0755 "${kubectl_binary}" /usr/local/bin/kubectl

echo "Running the complete prerequisite check as root..."
"${privilege[@]}" "$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/check-prerequisites.sh"

if ((docker_group_changed)); then
  cat <<EOF

Installation completed. User ${target_user} was added to the docker group.
Log out and back in before running Docker without sudo, then run:

  make prerequisites
  make check
  sudo make integration
  make kind-up
  make e2e
EOF
else
  cat <<'EOF'

Installation completed.

Next:
  make prerequisites
  make check
  sudo make integration
  make kind-up
  make e2e
EOF
fi
