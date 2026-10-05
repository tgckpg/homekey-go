include build.env
-include build.env.work
export

OUT_DIR := out
BIN_DIR := bin

IMAGE_NAME ?= ghcr.io/tgckpg/homekey-go
IMAGE_TAG ?= dev

BUILDX_BUILDER ?= container-builder

GO ?= go

BUILDINFO_FILE := internal/buildinfo/buildinfo_gen.go
BUILDX_BUILDER ?= container-builder

all: test build

ensure-buildx:
	@if ! docker buildx inspect $(BUILDX_BUILDER) >/dev/null 2>&1; then \
		echo "Creating buildx builder $(BUILDX_BUILDER)..."; \
		docker buildx create \
			--name $(BUILDX_BUILDER) \
			--driver docker-container \
			--driver-opt network=host \
			--bootstrap --use; \
	else \
		echo "Using existing buildx builder $(BUILDX_BUILDER)"; \
		docker buildx use $(BUILDX_BUILDER); \
	fi

all: build
build:
	mkdir -p $(BIN_DIR)
	$(GO) build -buildvcs=false -trimpath -o $(BIN_DIR)/homekey ./cmd/homekey
test:
	$(GO) test ./...
race:
	$(GO) test -race ./...
vet:
	$(GO) vet ./...
run:
	$(GO) run -buildvcs=false ./cmd/homekey

push:
	docker buildx build \
		--platform linux/amd64,linux/arm64 \
		--build-arg APT_PROXY=$(APT_PROXY) \
		-f Dockerfile \
		-t $(IMAGE_NAME):$(IMAGE_TAG) \
		--push .

clean:
	rm -rf bin

.PHONY: all build test race vet run clean push ensure-buildx
