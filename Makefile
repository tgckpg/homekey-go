GO ?= go
.PHONY: all build test race vet run clean
all: build
build:
	mkdir -p bin
	$(GO) build -buildvcs=false -trimpath -o bin/homekey ./cmd/homekey
test:
	$(GO) test ./...
race:
	$(GO) test -race ./...
vet:
	$(GO) vet ./...
run:
	$(GO) run -buildvcs=false ./cmd/homekey
clean:
	rm -rf bin
