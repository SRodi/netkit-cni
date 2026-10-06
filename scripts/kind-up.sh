#!/usr/bin/env bash
set -euo pipefail

ROOT=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
CLUSTER=${KIND_CLUSTER:-netkit-cni}
IMAGE=${IMAGE:-netkit-cni:dev}
NODE_IMAGE=${KIND_NODE_IMAGE:-kindest/node:v1.37.0@sha256:a1ed56cfb0e7b93589bdf97c8cd566405a265939e3620fc4f5de89adff580ae5}

for command in docker kind kubectl; do
  command -v "${command}" >/dev/null || {
    echo "required command not found: ${command}" >&2
    exit 1
  }
done
if ! docker info >/dev/null 2>&1; then
  echo "Docker is installed, but the current user cannot reach its daemon." >&2
  if getent group docker | cut -d: -f4 | tr ',' '\n' | grep -Fxq "${USER}"; then
    echo "User ${USER} belongs to the docker group, but this login session has not picked it up." >&2
    echo "Log out and back in, or start a temporary group-aware shell with:" >&2
    echo "  newgrp docker" >&2
  else
    echo "Add ${USER} to the docker group, then log out and back in:" >&2
    echo "  sudo usermod -aG docker ${USER}" >&2
  fi
  echo "Verify access before retrying:" >&2
  echo "  docker info" >&2
  exit 1
fi

docker build -t "${IMAGE}" "${ROOT}"
kind create cluster --name "${CLUSTER}" --image "${NODE_IMAGE}" --config "${ROOT}/deploy/kind.yaml"
kind load docker-image --name "${CLUSTER}" "${IMAGE}"

sed "s|image: netkit-cni:dev|image: ${IMAGE}|g" "${ROOT}/deploy/netkit-cni.yaml" |
  kubectl --context "kind-${CLUSTER}" apply -f -
kubectl --context "kind-${CLUSTER}" -n kube-system rollout status daemonset/netkit-agent --timeout=180s
kubectl --context "kind-${CLUSTER}" wait node --all --for=condition=Ready --timeout=180s
