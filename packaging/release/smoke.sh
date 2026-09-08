#!/bin/sh
# Exercise release packages in disposable Linux distributions, without Apple credentials.
set -eu
if [ "$#" -ne 3 ]; then
  printf '%s\n' 'Usage: smoke.sh ubuntu|debian|debian13|fedora|arch VERSION ASSET_DIRECTORY' >&2
  exit 2
fi
pomeforge_distro=$1
pomeforge_version=$2
pomeforge_assets=$(CDPATH= cd -- "$3" && pwd)
pomeforge_script=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)/smoke.sh
case $pomeforge_version in
  0.1.0) ;;
  *) printf '%s\n' 'This release smoke contract currently verifies 0.1.0.' >&2; exit 2 ;;
esac
case $pomeforge_distro in
  ubuntu) pomeforge_image=docker.io/library/ubuntu:22.04@sha256:2edbbc5dc405e9612ba3584ce95480277e3eb374407b5505fe26f17df77c7dbc ;;
  debian) pomeforge_image=docker.io/library/debian:12-slim@sha256:88200866dfff7ea7f5cbcb6ec7c8a701889efe6fe859fe64d6990e4b07ea4171 ;;
  debian13) pomeforge_image=docker.io/library/debian:13-slim@sha256:d7e12182ce18b85b93007c1dedf31f2d29e01ccf3182cc4017c709b6259bc132 ;;
  fedora) pomeforge_image=docker.io/library/fedora:44@sha256:43b29f65a41eb9c35e1cd5323e3bdf3b655c2357a9f4f1ff2f9c2798e5045d80 ;;
  arch) pomeforge_image=docker.io/library/archlinux:latest@sha256:b944cc65c5f28665dfd5fdbf5ed2997c88f5bb4a0aefac7ee8a7ef01893e5ed9 ;;
  *) printf '%s\n' 'Unknown smoke distribution.' >&2; exit 2 ;;
esac
if [ "${POMEFORGE_SMOKE_INNER:-}" != 1 ]; then
  pomeforge_engine=${POMEFORGE_CONTAINER_ENGINE:-docker}
  exec "$pomeforge_engine" run --rm \
    --volume "$pomeforge_assets:/packages:ro" \
    --volume "$pomeforge_script:/smoke.sh:ro" \
    --env POMEFORGE_SMOKE_INNER=1 \
    "$pomeforge_image" sh /smoke.sh "$pomeforge_distro" "$pomeforge_version" /packages
fi
[ "$(uname -s)" = Linux ]
case $(uname -m) in
  x86_64) pomeforge_arch=amd64; pomeforge_machine=x86_64 ;;
  aarch64) pomeforge_arch=arm64; pomeforge_machine=aarch64 ;;
  *) exit 2 ;;
esac
cd /packages
sha256sum --check "SHA256SUMS-$pomeforge_arch"
case $pomeforge_distro in
  ubuntu|debian|debian13)
    export DEBIAN_FRONTEND=noninteractive
    apt-get update
    apt-get install --yes "/packages/pomeforge_${pomeforge_version}_${pomeforge_arch}.deb"
    ;;
  fedora)
    dnf install --assumeyes "/packages/pomeforge-${pomeforge_version}-1.${pomeforge_machine}.rpm"
    ;;
  arch)
    pacman -Syu --noconfirm
    pacman -U --noconfirm "/packages/pomeforge-${pomeforge_version}-1-${pomeforge_machine}.pkg.tar.zst"
    ;;
esac
useradd --create-home --shell /bin/sh pomeforge-smoke
runuser -u pomeforge-smoke -- sh -eu <<'USER_CHECKS'
cd "$HOME"
pomeforge version
pomeforge quickstart --help
pomeforge-runtime install
# The package has no Apple SDK and does not authenticate to Apple.
pomeforge tools register unxip --path /opt/pomeforge/libexec/unxip \
  --source-revision 6c3990517fcc4c1db6952fccf4c562fb14097601 --execute
pomeforge tools register pomeforge-assets --path /opt/pomeforge/libexec/pomeforge-assets \
  --assetkit-revision e763558b55fcbb5a443b1d7b2c6f0972d8bd14f7 --execute
pomeforge tools install xtool --execute
pomeforge init HelloWorld --dir "$HOME/HelloWorld" --bundle-id com.example.pomeforge.smoke
pomeforge doctor --json > "$HOME/doctor.json"
python3 - <<'PY'
import json, os, subprocess
from pathlib import Path
report = json.loads((Path.home() / 'doctor.json').read_text())
assert report['ok'], report
by_id = {tool['id']: tool for tool in report['data']['tools']}
for tool in ('swift', 'clang', 'xtool', 'pomeforge-assets', 'unxip'):
    assert by_id[tool]['status'] == 'available', by_id[tool]
# SwiftPM must compile a host manifest on the fresh distribution. This is
# separate from compiling iOS code, which additionally requires the user's SDK.
result = subprocess.run([by_id['swift']['path'], 'package', '--package-path', str(Path.home() / 'HelloWorld'), 'dump-package'], capture_output=True, text=True, timeout=120)
assert result.returncode == 0, result.stderr
assert json.loads(result.stdout)['name'] == 'HelloWorld'
# An AI caller receives a structured error, never an unattended prompt.
result = subprocess.run(['pomeforge', 'quickstart', '--json'], capture_output=True, text=True, timeout=10)
assert result.returncode != 0
assert json.loads(result.stdout)['error']['code'] == 'interactive_required'
print('PASS: native package, first-run Swift download, helper registration, pinned xtool, project generation, SwiftPM host manifest, noninteractive boundary')
PY
USER_CHECKS
