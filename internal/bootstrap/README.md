# Orchard bootstrap package

`bootstrap` is a standard-library-only Go package for reading Orchard's pinned
tool catalog and installing supported Linux tools without sudo, shell commands,
remote install scripts, or changes to `PATH`.

## Public API

- `Load(io.Reader) (Catalog, error)` decodes the exact catalog schema, rejects
  unknown fields, and validates pins, URLs, sizes, formats, IDs, versions, and
  duplicate platform assets.
- `DefaultPaths() (Paths, error)` resolves XDG locations. Managed versions live
  below `$XDG_DATA_HOME/orchard/tools`, downloads below
  `$XDG_CACHE_HOME/orchard/downloads`, and active links below
  `$XDG_DATA_HOME/orchard/bin`. Missing or relative XDG values use the standard
  `$HOME/.local/share` and `$HOME/.cache` fallbacks.
- `PlanInstall(Catalog, id, goos, goarch, Paths) (Plan, error)` selects one
  catalog asset without writing. `Plan` is immutable outside this package and
  exposes `Tool`, `Asset`, `Paths`, `Platform`, and `ExecutablePath` accessors.
- `Installer.Install(context.Context, Plan) (Receipt, error)` downloads and
  activates the plan. `Installer.Client` and `Installer.Runner` are injectable
  for tests; nil values use an HTTPS-only `http.Client` and
  `exec.CommandContext` respectively.
- `VerifyInstalled(context.Context, Plan) (Receipt, error)` is a read-only
  lookup. It validates the requested catalog pin, the complete installed tree,
  and the active link to that exact version. `ErrInstallAbsent` and
  `ErrInstallInvalid` distinguish a missing version from damaged or mismatched
  managed state.
- `Receipt` records tool, version, OS, architecture, source asset URL, SHA-256,
  active executable path, and installation time.

The installer re-hashes cached and installed artifacts, enforces exact transfer
sizes, rejects unsafe tar entries, and serializes same-tool installs with a lock.
It builds a complete version directory before atomically replacing the active
symlink. Existing regular files and links outside Orchard's tool root are never
overwritten.

Each completed version has a bounded, deterministic manifest covering every
installed directory, regular file, and symbolic link (including modes, file
digests, and link targets). Reuse and `VerifyInstalled` compare the entire tree
to this manifest; `.artifact` is also checked independently against the
authoritative catalog pin. This local manifest detects accidental damage and
partial replacement. It is not an authentication boundary against an attacker
with the same user account, who can rewrite both installed files and their
manifest.

For xtool, the verified AppImage is executed directly with
`--appimage-extract`. The extracted tree is retained. A static POSIX launcher
uses Linux `readlink -f` to resolve Orchard's active symlink and then execs the
version-local `squashfs-root/AppRun`. The pinned xtool 1.19.0 AppRun is a
relative symlink to `usr/bin/xtool`; both it and its resolved executable are
validated as staying inside the version directory. No catalog or user path is
interpolated into the launcher.

## Linux tests

With Go 1.27.1 on Linux, run:

```sh
GO111MODULE=off go test ./internal/bootstrap
GO111MODULE=off go test -race ./internal/bootstrap
GO111MODULE=off go vet ./internal/bootstrap
```

An optional real-artifact integration test uses the immutable root catalog and
locally retained downloads; it performs no Apple operations. Set
`ORCHARD_REAL_INSTALL_ARTIFACT_DIR` to a directory containing files named by
the install cache convention before running the package test.
