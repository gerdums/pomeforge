GO ?= go
HOSTS ?= linux

.PHONY: all ensure-linux test vet build cross-check check clean

all: check

ensure-linux:
	@test "$(HOSTS)" = "linux" || { echo "Orchard build workflows support only HOSTS=linux" >&2; exit 2; }
	@test "$$(uname -s)" = "Linux" || { echo "Orchard build workflows must run on Linux" >&2; exit 2; }

test: ensure-linux
	GOOS=linux $(GO) test ./...

vet: ensure-linux
	GOOS=linux $(GO) vet ./...

build: ensure-linux
	mkdir -p bin
	CGO_ENABLED=0 GOOS=linux $(GO) build -trimpath -o bin/orchard ./cmd/orchard

cross-check: ensure-linux
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 $(GO) build -trimpath -o /tmp/orchard-linux-amd64 ./cmd/orchard
	CGO_ENABLED=0 GOOS=linux GOARCH=arm64 $(GO) build -trimpath -o /tmp/orchard-linux-arm64 ./cmd/orchard

check: test vet build cross-check

clean:
	$(GO) clean
