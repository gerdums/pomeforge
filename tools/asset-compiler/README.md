# Orchard Asset Compiler

`orchard-assets` is Orchard's small Linux bridge to
[AssetKit](https://github.com/xtool-org/AssetKit). The package pins AssetKit to
revision `e763558b55fcbb5a443b1d7b2c6f0972d8bd14f7`; it does not duplicate the
compiler or bundle any Apple SDK files.

## Build and test

Swift 6.3 and Linux are required, matching the pinned AssetKit manifest. The
compile action explicitly refuses non-Linux hosts.

```sh
swift build --package-path tools/asset-compiler -c release
swift test --package-path tools/asset-compiler
```

## Usage

```sh
orchard-assets compile \
  --catalog /path/to/Assets.xcassets \
  --app /path/to/Unsigned.app \
  --minimum-ios 17.0 \
  --json
```

`--help` does not inspect assets. The input `.app` must already contain a valid
`Info.plist`. The catalog uses normal asset-catalog `Contents.json` files; a
PNG app icon lives in an `.appiconset`, and each image entry supplies `idiom`,
`size`, `scale`, and `filename`.

The command compiles the complete catalog in memory, stages all outputs, writes
`Assets.car`, writes AssetKit's loose icon PNGs, and merges its icon additions
into the existing plist without removing unrelated keys. Output files and the
plist are committed with per-file renames and rollback on ordinary filesystem
errors. A host crash or storage failure during the short multi-file commit is
not claimed to be a filesystem-wide atomic transaction.

Catalog and app paths may not overlap. Direct symlink inputs, symlink output
destinations (including dangling links), nested catalog symlinks/special files,
catalog filename traversal, unsafe loose filenames, `_CodeSignature`,
`CodeResources`, and `embedded.mobileprovision` are rejected. Byte-identical
loose icon outputs with the same AssetKit filename are coalesced. Conflicting
idiom images that map to the same upstream loose filename are rejected with an
explicit error. Run this command after `xtool dev
build` creates an unsigned bundle and before zsign. It compiles resources only;
it does not sign, authenticate with Apple, operate devices, upload, publish, or
establish App Store acceptance.
