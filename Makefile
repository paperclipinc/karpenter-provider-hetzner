.PHONY: build test lint generate docker-build

BINARY := karpenter-provider-hetzner
IMAGE  := ghcr.io/paperclipinc/karpenter-provider-hetzner
TAG    ?= latest

build:
	go build -o bin/$(BINARY) ./cmd/controller

test:
	go test -race -count=1 ./...

lint:
	golangci-lint run ./...

generate:
	controller-gen object paths="./pkg/apis/..."
	controller-gen crd paths="./pkg/apis/..." output:crd:dir=charts/karpenter-provider-hetzner/crds

docker-build:
	docker build -t $(IMAGE):$(TAG) .

docker-push: docker-build
	docker push $(IMAGE):$(TAG)
