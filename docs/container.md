# Local Linux container

Orchard's local container is a compatibility fallback for Linux hosts where the
native binaries do not match the host libc, including musl systems and older
glibc distributions. The build and runtime remain on the developer's own Linux
host under Podman or Docker. There is no macOS, remote Xcode, hosted build, or
published-image fallback.

The image is built from pinned multi-architecture Go 1.24.13 and Swift 6.3.3
image indexes. It contains Swift, `/usr/local/bin/orchard`, and
`/usr/local/bin/orchard-assets`. It also contains the standalone Linux
`/usr/local/bin/unxip` 3.3 executable, built without source changes at upstream
revision `6c3990517fcc4c1db6952fccf4c562fb14097601`. It does not contain an Apple
SDK, Xcode XIP, xtool, ASC, zsign, credentials, signing material, provisioning
profiles, pairing records, or developer state.

## Build from a clean checkout

Review `git status --short` and build from a clean source checkout so the local
tag identifies known source. Both container engines select the correct child of
the pinned image indexes for the host architecture.

```sh
podman build --file packaging/Containerfile --tag localhost/orchard:local .
```

or:

```sh
docker build --file packaging/Containerfile --tag orchard:local .
```

The build context uses a positive allowlist for the Go module, embedded web
assets, AssetKit bridge, entrypoint, and the small root catalog package expected
by the final integration. It narrowly admits the root third-party notice and
third-party license directory as runtime notice inputs. Final recursive deny
rules exclude repository metadata, local Orchard and ASC state, caches,
Apple/Xcode/SDK and archive material, credentials, profiles, pairing records,
and conventional app/workspace directories even when those appear below an
otherwise allowlisted source directory. `.dockerignore` and `.containerignore`
are intentionally identical.

An engine-backed probe constructs a temporary context containing only harmless
sentinel files at the root and below an otherwise allowlisted `internal/`
directory. It verifies that expected source and notice inputs are present after
`COPY .` while private state, credentials, SDKs, archives, and application trees
are absent:

```sh
./packaging/container-context-probe.sh podman
./packaging/container-context-probe.sh docker
```

The probe pulls the same pinned Go base when it is not already local, creates a
temporary probe image, and removes that exact image afterward. Run the command
for the engine used to build Orchard.

## Private state and projects

Create separate host directories for private state and projects. Do not mount
the host home directory.

```sh
state_dir=$HOME/.local/share/orchard-container
workspace_dir=$HOME/OrchardProjects
install -d -m 0700 "$state_dir" "$workspace_dir"
```

The entrypoint sets `umask 077` and uses `/var/lib/orchard` for a private
`HOME`, XDG config, data, cache, and state hierarchy. It preserves existing
contents and makes those dedicated directories mode 0700. The project mount is
`/workspace`. If a bind mount reports a permission error, make the host
directories owned and writable by the invoking user; the entrypoint does not
use sudo or conceal ownership failures.

For rootless Podman, keep the caller's identity and apply a private SELinux
label when supported:

```sh
image=localhost/orchard:local
podman run --rm --userns=keep-id --user "$(id -u):$(id -g)" \
  --volume "$state_dir:/var/lib/orchard:Z" \
  --volume "$workspace_dir:/workspace:Z" \
  "$image" orchard version
```

For Docker, use the caller's numeric identity:

```sh
image=orchard:local
docker run --rm --user "$(id -u):$(id -g)" \
  --volume "$state_dir:/var/lib/orchard" \
  --volume "$workspace_dir:/workspace" \
  "$image" orchard version
```

The image also defaults to the unprivileged numeric user `10001:10001`, so a
plain launch is non-root. Supplying the host identity is what gives bind-mounted
files predictable host ownership when the host user has another numeric ID.

## CLI workflow

The following helper keeps every invocation on the same private state and
workspace mounts. It does not alter the host `PATH`.

```sh
orchard_container() {
  podman run --rm --userns=keep-id --user "$(id -u):$(id -g)" \
    --volume "$state_dir:/var/lib/orchard:Z" \
    --volume "$workspace_dir:/workspace:Z" \
    localhost/orchard:local orchard "$@"
}

orchard_container version
orchard_container schema --json
orchard_container init Garden --dir /workspace/Garden --bundle-id com.example.Garden
orchard_container doctor
```

`doctor` reports detected executables and capabilities. Success does not prove
that an iOS SDK is installed, a native iOS build works, a physical device can be
used, signing is valid, or Apple accepts an artifact.

Install each managed tool with a reviewed JSON preview followed by explicit
execution. Each invocation uses the same private state mount, so installed tool
metadata and binaries persist across container runs:

```sh
orchard_container tools install xtool --json
orchard_container tools install xtool --execute
orchard_container tools install asc --json
orchard_container tools install asc --execute
orchard_container tools install zsign --json
orchard_container tools install zsign --execute
```

The image does not preinstall xtool, ASC, or zsign. `tools install` downloads the
architecture-appropriate release selected by Orchard's pinned tool manifest,
verifies it, and installs it into user-owned persisted state; it does not invoke
`sudo` or mutate the host installation.

The image does include the Orchard AssetKit bridge and unxip helper. Register
their absolute container paths with the revisions that identify their reviewed
dependencies, again previewing each local state change before execution:

```sh
orchard_container tools register orchard-assets \
  --path /usr/local/bin/orchard-assets \
  --assetkit-revision e763558b55fcbb5a443b1d7b2c6f0972d8bd14f7 --json
orchard_container tools register orchard-assets \
  --path /usr/local/bin/orchard-assets \
  --assetkit-revision e763558b55fcbb5a443b1d7b2c6f0972d8bd14f7 --execute
orchard_container tools register unxip \
  --path /usr/local/bin/unxip \
  --source-revision 6c3990517fcc4c1db6952fccf4c562fb14097601 --json
orchard_container tools register unxip \
  --path /usr/local/bin/unxip \
  --source-revision 6c3990517fcc4c1db6952fccf4c562fb14097601 --execute
```

The AssetKit revision is the exact `Package.resolved` pin. The registration
deliberately omits an Orchard bridge `--source-revision`: a source checkout path
does not establish the commit used to build the image. Helper usage is exposed
with `--help`; `orchard-assets` does not provide a `--version` contract.

The `unxip` executable is a separate upstream program, not code linked into
Orchard. Its `--version` and `--help` contracts and dynamic libraries are checked
inside the final Swift runtime during the image build.

## Graphical workspace

Use Linux host networking so Orchard's authenticated HTTP service remains on
the host loopback interface and its Host and Origin checks are unchanged. Never
replace the listen address with `0.0.0.0` and do not put a proxy in front of it.

```sh
podman run --rm --network host \
  --userns=keep-id --user "$(id -u):$(id -g)" \
  --volume "$state_dir:/var/lib/orchard:Z" \
  --volume "$workspace_dir:/workspace:Z" \
  localhost/orchard:local \
  orchard app --workspace /workspace --listen 127.0.0.1:0
```

For Docker, use the same mounts and command with `docker run --network host` and
omit `--userns=keep-id` and the `:Z` suffixes. There is no browser in the image.
Read the actual `Orchard app: http://127.0.0.1:.../#token=...` startup line from
the attached terminal and open that exact URL in the host browser. Do not save,
parse, or copy token-bearing output into logs. Stop the attached container with
Ctrl-C when finished.

## SDK and signing inputs

Apple SDK and signing configuration belong only in the private state mount.
Keep every operator-supplied Xcode archive, SDK source, private key,
certificate, profile, and pairing record outside the Orchard source and project
directories, and never copy it into an image layer. The [native Linux setup
guide](setup.md) covers the licensing and prerequisite boundaries.

Select only the directory containing the permitted, separately authenticated
SDK source and expose it read-only at `/input`. This second wrapper uses the
same absolute state and workspace mounts as `orchard_container`, while adding
only that narrow input mount:

```sh
input_dir=/absolute/path/to/reviewed-sdk-input
test "${input_dir#/}" != "$input_dir"
test -d "$input_dir"

orchard_container_with_input() {
  podman run --rm --userns=keep-id --user "$(id -u):$(id -g)" \
    --volume "$state_dir:/var/lib/orchard:Z" \
    --volume "$workspace_dir:/workspace:Z" \
    --volume "$input_dir:/input:ro,Z" \
    localhost/orchard:local orchard "$@"
}
```

Choose the architecture matching the running Linux image (`arm64` or
`x86_64`). Preview the complete import before executing it; replace the example
input name with the actual path beneath the read-only mount:

```sh
sdk_arch=arm64
orchard_container_with_input sdk import \
  --input /input/Xcode.xip --arch "$sdk_arch" --json
orchard_container_with_input sdk import \
  --input /input/Xcode.xip --arch "$sdk_arch" --execute
```

The entrypoint sets the absolute `XDG_CONFIG_HOME` beneath
`/var/lib/orchard`, so preview, import, SDK discovery, and later builds use the
same persisted configuration. Execution extracts into fresh user-owned state,
builds a fresh `darwin.artifactbundle`, stages the selected host Clang resource
headers as user-owned files, and installs the result with native
`swift sdk install`. It preserves both the original read-only Apple input and
existing private state, and refuses to replace an installed SDK. Do not run
`xtool sdk install` or `sudo` for this workflow.

For Docker, define the same wrapper with `docker run`, omit
`--userns=keep-id` and the `:Z` suffixes, and retain the same three mount paths.
Keep the selected host input directory mode 0700. An SDK import establishes
local setup only; it does not prove a native app build, device operation,
signing validity, or Apple acceptance.

## unxip source and rebuilding

The image retains the exact unmodified upstream source archive at
`/usr/share/orchard/sources/unxip-3.3.0.tar.gz` and its upstream `LICENSE` as the
adjacent `unxip-3.3.0.LICENSE`. The archive is produced before compilation with
`git archive` at the verified revision, a stable `unxip-3.3.0/` prefix, and
`gzip -n`; it contains neither `.git` nor build output. Orchard's complete
third-party notice and license directory is installed under
`/usr/share/doc/orchard`.

Copy and extract the retained source without mounting the host home directory:

```sh
source_dir=$PWD/orchard-container-sources
install -d -m 0700 "$source_dir"
podman run --rm --userns=keep-id --user "$(id -u):$(id -g)" \
  --volume "$source_dir:/workspace:Z" \
  localhost/orchard:local \
  cp /usr/share/orchard/sources/unxip-3.3.0.tar.gz \
     /usr/share/orchard/sources/unxip-3.3.0.LICENSE /workspace/
gzip -dc "$source_dir/unxip-3.3.0.tar.gz" | tar -xf - -C "$source_dir"
```

To reproduce the Linux build stage, build its named target from the same clean
Orchard checkout:

```sh
podman build --file packaging/Containerfile \
  --target orchard-unxip-build \
  --tag localhost/orchard-unxip-build:local .
```

That stage uses the pinned Swift 6.3.3 image, verifies the fetched commit, and
installs only `liblzma-dev=5.6.1+really5.4.5-1ubuntu0.3` and
`zlib1g-dev=1:1.3.dfsg-3.1ubuntu2.2` as Ubuntu noble build dependencies. They
are needed for unxip's Linux LZMA and zlib system-library targets and are not
installed into the final runtime stage. The extracted archive can be rebuilt
with `swift build --configuration release` in that same pinned Swift image after
installing those exact packages.

## Physical USB devices

Prefer the host's `usbmuxd`. After installing the device tooling explicitly,
mount only its Unix socket when the host exposes one:

```sh
test -S /var/run/usbmuxd
podman run --rm --userns=keep-id --user "$(id -u):$(id -g)" \
  --volume "$state_dir:/var/lib/orchard:Z" \
  --volume "$workspace_dir:/workspace:Z" \
  --volume /var/run/usbmuxd:/var/run/usbmuxd \
  localhost/orchard:local orchard doctor
```

The host user must be permitted to open the socket. Device trust, Developer
Mode, and host udev permissions remain host/device setup. When using the host
socket, the host daemon manages pairing records, normally in
`/var/lib/lockdown` on Linux, as described in the [usbmuxd usage
guide](https://github.com/libimobiledevice/usbmuxd#usage). Orchard's private
state mount does not relocate those records. Do not mount or commit the host
pairing directory.

If a tool cannot use the host socket, direct passthrough is a narrower fallback.
Identify the current device node, grant only its host group, and pass only that
node, for example `--device /dev/bus/usb/001/002` with
`--group-add "$(stat -c %g /dev/bus/usb/001/002)"`. Never use `--privileged` or
mount all of `/dev`. Rootless engines may reject device access despite the group
mapping. USB bus/device numbers can change after reconnect, so stop the
container, review the new node and permissions, and restart with the new scoped
mapping.

## Host compatibility boundary

- Arch and Omarchy can use rootless Podman or Docker plus the host usbmuxd and
  udev packages available from their repositories.
- Debian and Ubuntu can use their Podman or Docker packages; `usbmuxd` and its
  socket permissions are separate host prerequisites.
- Fedora commonly provides rootless Podman and SELinux labeling, hence the
  `:Z` examples; USB group and policy setup remains host-specific.
- A musl host such as Alpine runs the pinned Ubuntu-based Swift userspace inside
  the OCI container, avoiding linkage against the host libc. The host still
  needs a compatible container engine, architecture, storage, networking, and
  any requested USB access.

These are compatibility instructions, not claims that every listed host has
passed the image build, native iOS build, device, signing, or Apple-processing
gates. Exact Podman image build and runtime smoke evidence is collected by the
host proof executor after source export and repeated against the final integrated
source.
