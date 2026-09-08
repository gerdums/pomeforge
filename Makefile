GO ?= go

.PHONY: all test vet build cross-check check clean

all: check

test:
	$(GO) test ./...

vet:
	$(GO) vet ./...

build:
	mkdir -p bin
	CGO_ENABLED=0 $(GO) build -trimpath -o bin/orchard ./cmd/orchard

cross-check:
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 $(GO) build -trimpath -o /tmp/orchard-linux-amd64 ./cmd/orchard
	CGO_ENABLED=0 GOOS=linux GOARCH=arm64 $(GO) build -trimpath -o /tmp/orchard-linux-arm64 ./cmd/orchard

check: test vet build cross-check

clean:
	$(GO) clean
