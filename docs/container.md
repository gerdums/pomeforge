# Local Linux container

Orchard's local container is a compatibility fallback for Linux hosts where the
native binaries do not match the host libc, including musl systems and older
glibc distributions. The build and runtime remain on the developer's own Linux
host under Podman or Docker. There is no macOS, remote Xcode, hosted build, or
published-image fallback.

The image is built from pinned multi-architecture Go 1.24.13 and Swift 6.3.3
image indexes. It contains Swift, `/usr/local/bin/orchard`, and
`/usr/local/bin/orchard-assets`. It does not contain an Apple SDK, xtool, ASC,
zsign, credentials, signing material, provisioning profiles, pairing records,
or developer state.

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
by the final integration. Final deny rules exclude repository metadata, local
Orchard state, caches, Apple/Xcode/SDK material, credentials, profiles, pairing
records, and conventional app/workspace directories. `.dockerignore` and
`.containerignore` are intentionally identical.

An engine-backed probe constructs a temporary context containing only harmless
sentinel files outside the source allowlist and verifies they are absent after
`COPY .`:

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

This packet deliberately does not invent the future tool-install integration.
After the final CLI schema advertises `tools install`, the concrete onboarding
flow will be a reviewed preview followed by explicit execution in the same
mounted state, for example:

```sh
# Available only after tools-install integration; confirm with `orchard schema --json`.
orchard_container tools install xtool --json
orchard_container tools install xtool --execute
```

Until that command exists in the integrated CLI, follow the HTTPS guidance from
`doctor` and do not treat the example as an implemented command. The image does
not preinstall xtool, ASC, or zsign.

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
Operator-supplied source material should enter through a separate, narrowly
selected read-only mount rather than through the image build or host home:

```sh
input_dir=/absolute/path/to/reviewed-inputs
podman run --rm --userns=keep-id --user "$(id -u):$(id -g)" \
  --volume "$state_dir:/var/lib/orchard:Z" \
  --volume "$workspace_dir:/workspace:Z" \
  --volume "$input_dir:/input:ro,Z" \
  localhost/orchard:local orchard schema --json
```

Replace the final command only with a reviewed, implemented import/setup
operation. Keep the selected input directory mode 0700. Never copy an Xcode XIP,
SDK, private key, certificate, profile, or pairing record into the source tree
or image. Swift remains installed in the runtime for Linux-native iOS
cross-compilation after the operator explicitly configures a permitted SDK.

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
Mode, and host udev permissions remain host/device setup. Pairing records stay
in the private state mount; do not share a host pairing directory or commit it.

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
