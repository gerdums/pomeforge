#!/bin/sh
set -eu

repo_root=$(CDPATH= cd -- "$(dirname -- "$0")/../.." && pwd)
containerfile=$repo_root/packaging/Containerfile
entrypoint=$repo_root/packaging/container-entrypoint

assert_contains() {
  pattern=$1
  file=$2
  if ! grep -F -- "$pattern" "$file" >/dev/null; then
    printf '%s\n' "Missing required contract in $file: $pattern" >&2
    exit 1
  fi
}

cmp "$repo_root/.dockerignore" "$repo_root/.containerignore"
sh -n "$entrypoint"
sh -n "$repo_root/packaging/container-context-probe.sh"

assert_contains 'golang:1.24.13-bookworm@sha256:1a6d4452c65dea36aac2e2d606b01b4a029ec90cc1ae53890540ce6173ea77ac' "$containerfile"
assert_contains 'swift:6.3.3@sha256:56ef1be2c1ca36f4c52440357dc1fcdfdb5e113587134fcadeef57c225c71b54' "$containerfile"
assert_contains 'CGO_ENABLED=0 GOOS=linux go build -trimpath -buildvcs=false' "$containerfile"
assert_contains '--disable-automatic-resolution' "$containerfile"
assert_contains 'USER 10001:10001' "$containerfile"
assert_contains 'COPY --from=orchard-assets-build /out/orchard-assets /usr/local/bin/orchard-assets' "$containerfile"
assert_contains '!/*.go' "$repo_root/.dockerignore"
assert_contains '!catalog/**' "$repo_root/.dockerignore"
assert_contains '*.xip' "$repo_root/.dockerignore"
assert_contains '*.mobileprovision' "$repo_root/.dockerignore"
assert_contains '**/.orchard/**' "$repo_root/.dockerignore"
if grep -E '(^|[^[:alnum:]_])eval([^[:alnum:]_]|$)' "$entrypoint" >/dev/null; then
  printf '%s\n' "Container entrypoint must not use eval." >&2
  exit 1
fi
assert_contains 'exec "$@"' "$entrypoint"

test_root=$(mktemp -d)
cleanup() {
  rm -rf -- "$test_root"
}
trap cleanup EXIT HUP INT TERM
mkdir -p "$test_root/private/data"
printf '%s\n' preserved > "$test_root/private/data/existing"

observed=$(
  ORCHARD_STATE_ROOT=$test_root/private sh "$entrypoint" sh -c \
    'printf "%s|%s|%s|%s|%s" "$HOME" "$XDG_CONFIG_HOME" "$XDG_DATA_HOME" "$XDG_CACHE_HOME" "$XDG_STATE_HOME"'
)
expected="$test_root/private/home|$test_root/private/config|$test_root/private/data|$test_root/private/cache|$test_root/private/state"
test "$observed" = "$expected"
test "$(cat "$test_root/private/data/existing")" = preserved
for directory in "$test_root/private" "$test_root/private/home" "$test_root/private/config" "$test_root/private/data" "$test_root/private/cache" "$test_root/private/state"; do
  test "$(stat -c '%a' "$directory")" = 700
done

printf '%s\n' "Container contract and private-state entrypoint checks passed."
