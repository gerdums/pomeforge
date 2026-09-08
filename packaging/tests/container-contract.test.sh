#!/bin/sh
set -eu

repo_root=$(CDPATH= cd -- "$(dirname -- "$0")/../.." && pwd)
containerfile=$repo_root/packaging/Containerfile
entrypoint=$repo_root/packaging/container-entrypoint
context_probe=$repo_root/packaging/container-context-probe.sh
context_containerfile=$repo_root/packaging/tests/Containerfile.context
workflow=$repo_root/.github/workflows/ci.yml

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
sh -n "$context_probe"
test -s "$repo_root/THIRD_PARTY_NOTICES.md"
test -d "$repo_root/docs/third-party"

assert_contains 'golang:1.24.13-bookworm@sha256:1a6d4452c65dea36aac2e2d606b01b4a029ec90cc1ae53890540ce6173ea77ac' "$containerfile"
assert_contains 'swift:6.3.3@sha256:56ef1be2c1ca36f4c52440357dc1fcdfdb5e113587134fcadeef57c225c71b54' "$containerfile"
assert_contains 'ENV GOTOOLCHAIN=local' "$containerfile"
assert_contains 'CGO_ENABLED=0 GOOS=linux go build -trimpath -buildvcs=false' "$containerfile"
assert_contains '--disable-automatic-resolution' "$containerfile"
asset_stage=$(awk '/AS pomeforge-assets-build/{copy=1} /AS pomeforge-unxip-build/{copy=0} copy' "$containerfile")
asset_out_line=$(printf '%s\n' "$asset_stage" | grep -nF -- 'install -d /out' | cut -d: -f1)
asset_install_line=$(printf '%s\n' "$asset_stage" | grep -nF -- 'install -m 0755 tools/asset-compiler/.build/release/pomeforge-assets' | cut -d: -f1)
test "$asset_out_line" -lt "$asset_install_line"
assert_contains 'AS pomeforge-unxip-build' "$containerfile"
assert_contains 'UNXIP_REVISION=6c3990517fcc4c1db6952fccf4c562fb14097601' "$containerfile"
assert_contains 'liblzma-dev=5.6.1+really5.4.5-1ubuntu0.3' "$containerfile"
assert_contains 'zlib1g-dev=1:1.3.dfsg-3.1ubuntu2.2' "$containerfile"
assert_contains 'test "$(git rev-parse FETCH_HEAD)" = "${UNXIP_REVISION}"' "$containerfile"
assert_contains 'git archive --format=tar --prefix="unxip-${UNXIP_VERSION}/" "${UNXIP_REVISION}"' "$containerfile"
assert_contains 'gzip -n > "/out/unxip-${UNXIP_VERSION}.tar.gz"' "$containerfile"
unxip_archive_line=$(grep -nF -- 'git archive --format=tar' "$containerfile" | cut -d: -f1)
unxip_build_line=$(grep -nF -- '&& swift build --configuration release' "$containerfile" | cut -d: -f1)
test "$unxip_archive_line" -lt "$unxip_build_line"
assert_contains 'COPY --from=pomeforge-unxip-build /out/unxip /usr/local/bin/unxip' "$containerfile"
assert_contains 'COPY --from=pomeforge-unxip-build /out/unxip-3.3.0.tar.gz /usr/share/pomeforge/sources/unxip-3.3.0.tar.gz' "$containerfile"
assert_contains 'COPY --from=pomeforge-unxip-build /out/unxip-3.3.0.LICENSE /usr/share/pomeforge/sources/unxip-3.3.0.LICENSE' "$containerfile"
assert_contains 'COPY THIRD_PARTY_NOTICES.md /usr/share/doc/pomeforge/THIRD_PARTY_NOTICES.md' "$containerfile"
assert_contains 'COPY docs/third-party/ /usr/share/doc/pomeforge/docs/third-party/' "$containerfile"
assert_contains 'test "$(unxip --version)" = "unxip 3.3"' "$containerfile"
assert_contains 'ldd /usr/local/bin/unxip' "$containerfile"
assert_contains 'USER 10001:10001' "$containerfile"
assert_contains 'COPY --from=pomeforge-assets-build /out/pomeforge-assets /usr/local/bin/pomeforge-assets' "$containerfile"
assert_contains '!/*.go' "$repo_root/.dockerignore"
assert_contains '!catalog/**' "$repo_root/.dockerignore"
assert_contains '!THIRD_PARTY_NOTICES.md' "$repo_root/.dockerignore"
assert_contains '!docs/third-party/**' "$repo_root/.dockerignore"
assert_contains '**/.git/**' "$repo_root/.dockerignore"
assert_contains '**/.sandcastle/**' "$repo_root/.dockerignore"
assert_contains '**/.workflow/**' "$repo_root/.dockerignore"
assert_contains ': > "$probe_root/internal/.git/config"' "$context_probe"
assert_contains ': > "$probe_root/internal/.sandcastle/context-marker"' "$context_probe"
assert_contains ': > "$probe_root/internal/.workflow/context-marker"' "$context_probe"
assert_contains 'test ! -e /context/internal/.git/config' "$context_containerfile"
assert_contains 'test ! -e /context/internal/.sandcastle/context-marker' "$context_containerfile"
assert_contains 'test ! -e /context/internal/.workflow/context-marker' "$context_containerfile"
assert_contains '*.xip' "$repo_root/.dockerignore"
assert_contains '**/*.xip' "$repo_root/.dockerignore"
assert_contains '*.mobileprovision' "$repo_root/.dockerignore"
assert_contains '**/*.mobileprovision' "$repo_root/.dockerignore"
assert_contains '**/*.key' "$repo_root/.dockerignore"
assert_contains '**/*.pem' "$repo_root/.dockerignore"
assert_contains '**/.asc/**' "$repo_root/.dockerignore"
assert_contains '**/.pomeforge/**' "$repo_root/.dockerignore"
assert_contains '**/.orchard/**' "$repo_root/.dockerignore"
assert_contains ': > "$probe_root/internal/.orchard/state.json"' "$context_probe"
assert_contains 'test ! -e /context/internal/.orchard/state.json' "$context_containerfile"
assert_contains '**/SDKs/**' "$repo_root/.dockerignore"
assert_contains '**/apps/**' "$repo_root/.dockerignore"
assert_contains 'GOTOOLCHAIN: local' "$workflow"
assert_contains 'run: GOOS=linux go test ./...' "$workflow"
assert_contains 'run: GOOS=linux go vet ./...' "$workflow"
assert_contains 'run: CGO_ENABLED=0 GOOS=linux go build -trimpath -o /tmp/pomeforge ./cmd/pomeforge' "$workflow"
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
  POMEFORGE_STATE_ROOT=$test_root/private sh "$entrypoint" sh -c \
    'printf "%s|%s|%s|%s|%s" "$HOME" "$XDG_CONFIG_HOME" "$XDG_DATA_HOME" "$XDG_CACHE_HOME" "$XDG_STATE_HOME"'
)
expected="$test_root/private/home|$test_root/private/config|$test_root/private/data|$test_root/private/cache|$test_root/private/state"
test "$observed" = "$expected"
test "$(cat "$test_root/private/data/existing")" = preserved
for directory in "$test_root/private" "$test_root/private/home" "$test_root/private/config" "$test_root/private/data" "$test_root/private/cache" "$test_root/private/state"; do
  test "$(stat -c '%a' "$directory")" = 700
done

printf '%s\n' "Container contract and private-state entrypoint checks passed."
