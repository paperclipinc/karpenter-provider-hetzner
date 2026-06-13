.PHONY: build test lint generate generate-verify docker-build

BINARY         := karpenter-provider-hetzner
IMAGE          := ghcr.io/paperclipinc/karpenter-provider-hetzner
TAG            ?= latest
CONTROLLER_GEN := go run sigs.k8s.io/controller-tools/cmd/controller-gen@v0.19.0

build:
	go build -o bin/$(BINARY) ./cmd/controller

test:
	go test -race -count=1 ./...

lint:
	golangci-lint run ./...

generate:
	$(CONTROLLER_GEN) object paths="./pkg/apis/..."
	$(CONTROLLER_GEN) crd paths="./pkg/apis/..." output:crd:dir=charts/karpenter-provider-hetzner/crds

generate-verify: generate
	@if [ -n "$$(git status --porcelain pkg/apis charts/karpenter-provider-hetzner/crds)" ]; then \
		echo "generated files are out of date; run 'make generate' and commit"; \
		git --no-pager diff -- pkg/apis charts/karpenter-provider-hetzner/crds; \
		exit 1; \
	fi

docker-build:
	docker build -t $(IMAGE):$(TAG) .

docker-push: docker-build
	docker push $(IMAGE):$(TAG)
