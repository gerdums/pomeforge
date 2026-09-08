#!/bin/sh
set -eu

repo_root=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
engine=${1:-}
if [ -z "$engine" ]; then
  if command -v podman >/dev/null 2>&1; then
    engine=podman
  elif command -v docker >/dev/null 2>&1; then
    engine=docker
  else
    printf '%s\n' "No Podman or Docker executable is available for the context probe." >&2
    exit 77
  fi
fi
case $engine in
  podman | docker) ;;
  *)
    printf '%s\n' "Usage: $0 [podman|docker]" >&2
    exit 2
    ;;
esac
if ! command -v "$engine" >/dev/null 2>&1; then
  printf '%s\n' "$engine is not installed." >&2
  exit 77
fi

probe_root=$(mktemp -d)
iid_file=$probe_root/image-id
cleanup() {
  if [ -s "$iid_file" ]; then
    "$engine" image rm "$(cat "$iid_file")" >/dev/null 2>&1 || true
  fi
  rm -rf -- "$probe_root"
}
trap cleanup EXIT HUP INT TERM

cp "$repo_root/.dockerignore" "$probe_root/.dockerignore"
cp "$repo_root/.containerignore" "$probe_root/.containerignore"
cp "$repo_root/packaging/tests/Containerfile.context" "$probe_root/Containerfile.context"
cp "$repo_root/go.mod" "$probe_root/go.mod"
mkdir -p -- "$probe_root/docs/third-party" "$probe_root/internal/.asc" \
  "$probe_root/internal/.orchard" "$probe_root/internal/apps/demo" \
  "$probe_root/internal/SDKs/iPhoneOS.sdk"
: > "$probe_root/THIRD_PARTY_NOTICES.md"
: > "$probe_root/docs/third-party/context-license.txt"
: > "$probe_root/internal/context-allowed.go"
: > "$probe_root/internal/demo.key"
: > "$probe_root/internal/demo.pem"
: > "$probe_root/internal/demo.xip"
: > "$probe_root/internal/.asc/config.json"
: > "$probe_root/internal/.orchard/state.json"
: > "$probe_root/internal/apps/demo/marker"
: > "$probe_root/internal/SDKs/iPhoneOS.sdk/marker"

for directory in .git .sandcastle .orchard .cache .swiftpm .build SDKs workspace workspaces projects apps; do
  mkdir -p -- "$probe_root/$directory"
  : > "$probe_root/$directory/context-sentinel"
done
for suffix in xip ipa p8 key pem cer mobileprovision pairing-record; do
  : > "$probe_root/context-sentinel.$suffix"
done

"$engine" build --no-cache --iidfile "$iid_file" --file "$probe_root/Containerfile.context" "$probe_root"
printf '%s\n' "Container build context exclusions passed with $engine."
