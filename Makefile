BINARY := bin/costume-tree
IMAGE := costume-tree:dev
CONTAINER_ENGINE ?= docker

.PHONY: build test run container-build fmt

build:
	mkdir -p bin
	go build -trimpath -o $(BINARY) ./cmd/costume-tree

test:
	go test ./...

run:
	go run ./cmd/costume-tree

container-build:
	$(CONTAINER_ENGINE) build --tag $(IMAGE) .

fmt:
	gofmt -w cmd internal
