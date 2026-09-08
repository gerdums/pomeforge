GO ?= go
HOSTS ?= linux

.PHONY: all ensure-linux test vet build cross-check check clean

all: check

ensure-linux:
	@test "$(HOSTS)" = "linux" || { echo "Pomeforge build workflows support only HOSTS=linux" >&2; exit 2; }
	@test "$$(uname -s)" = "Linux" || { echo "Pomeforge build workflows must run on Linux" >&2; exit 2; }

test: ensure-linux
	GOOS=linux $(GO) test ./...

vet: ensure-linux
	GOOS=linux $(GO) vet ./...

build: ensure-linux
	mkdir -p bin
	CGO_ENABLED=0 GOOS=linux $(GO) build -trimpath -o bin/pomeforge ./cmd/pomeforge

cross-check: ensure-linux
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 $(GO) build -trimpath -o /tmp/pomeforge-linux-amd64 ./cmd/pomeforge
	CGO_ENABLED=0 GOOS=linux GOARCH=arm64 $(GO) build -trimpath -o /tmp/pomeforge-linux-arm64 ./cmd/pomeforge

check: test vet build cross-check

clean:
	$(GO) clean
