#!/usr/bin/env bash
set -euo pipefail

kind delete cluster --name "${KIND_CLUSTER:-netkit-cni}"
