SHELL := /bin/bash

BIN_DIR ?= bin
IMAGE ?= netkit-cni:dev
KIND_CLUSTER ?= netkit-cni

.PHONY: all install-prerequisites prerequisites build test vet check image kind-up diagnose e2e kind-down integration clean

all: check build

install-prerequisites:
	./scripts/install-prerequisites-ubuntu.sh

prerequisites:
	./scripts/check-prerequisites.sh

$(BIN_DIR):
	mkdir -p $(BIN_DIR)

build: | $(BIN_DIR)
	CGO_ENABLED=0 GOOS=linux go build -trimpath -o $(BIN_DIR)/netkit-cni ./cmd/netkit-cni
	CGO_ENABLED=0 GOOS=linux go build -trimpath -o $(BIN_DIR)/netkit-agent ./cmd/netkit-agent
	CGO_ENABLED=0 GOOS=linux go build -trimpath -o $(BIN_DIR)/host-local github.com/containernetworking/plugins/plugins/ipam/host-local

test:
	go test ./...

vet:
	go vet ./...

check: test vet

image:
	docker build -t $(IMAGE) .

kind-up:
	IMAGE=$(IMAGE) KIND_CLUSTER=$(KIND_CLUSTER) ./scripts/kind-up.sh

diagnose:
	KIND_CLUSTER=$(KIND_CLUSTER) ./scripts/diagnose.sh

e2e:
	IMAGE=$(IMAGE) KIND_CLUSTER=$(KIND_CLUSTER) ./scripts/e2e.sh

kind-down:
	KIND_CLUSTER=$(KIND_CLUSTER) ./scripts/kind-down.sh

integration: build
	sudo ./scripts/integration-netns.sh

clean:
	rm -rf $(BIN_DIR)
