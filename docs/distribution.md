# Native Linux distribution library

`internal/distribution` is the reusable local library for signing-identity inspection, `.app`/IPA inspection, and staged App Store export. It is independent of Pomeforge's CLI, HTTP server, planner, bootstrap code, and graphical workspace. Export and export planning refuse non-Linux hosts at runtime; there is no macOS or hosted fallback.

## Trust and identity configuration

A signing config is a versioned JSON file created at a caller-selected private location outside all project trees. `CreateSigningConfig` creates only a new regular file with mode `0600`; it rejects symlink traversal and never replaces an existing file. `LoadSigningConfig` rejects nonregular files, symlinks, group/world permissions, unknown JSON fields, invalid versions, non-absolute paths, and oversized data.

Version 1 references, but never copies, these files:

```json
{
  "version": 1,
  "privateKeyPath": "/private/pomeforge/distribution.key",
  "certificatePath": "/private/pomeforge/distribution.pem",
  "provisioningProfilePath": "/private/pomeforge/app.mobileprovision",
  "trustedRootPaths": ["/private/pomeforge/trust/apple-root.pem"],
  "metadata": {"label": "team store identity"}
}
```

Keys must be unencrypted PEM PKCS#8, RSA PKCS#1, or EC SEC1 using RSA or ECDSA. Encrypted keys are rejected with no password-on-argv alternative. Certificates are single PEM certificates. Public reports contain certificate fingerprints and profile facts, never any of these private paths or key bytes.

Provisioning profiles are parsed as CMS SignedData and their embedded XML or binary plist is decoded. `InspectProfile` and `InspectIdentity` first call the PKCS#7 integrity verifier. Trust is reported separately as:

- `no_explicit_roots`: signature integrity was considered without an operator trust pool;
- `verified_against_explicit_roots`: the signer chain verified at the caller's time against the configured roots; or
- `untrusted`: explicit chain verification failed.

An embedded signer, an embedded intermediate, or any certificate carried inside a profile is never promoted to a trust anchor. Pomeforge bundles no Apple root and does not call an arbitrary configured root “Apple verified.” Operators should obtain the applicable Apple PKI certificates from Apple's official [Apple PKI repository](https://www.apple.com/certificateauthority/), verify their published fingerprints/provenance independently, convert them to PEM if necessary, and configure only the roots they intend to trust.

Inspection reports the profile-byte SHA-256, CMS integrity, explicit-chain trust, UUID/name/dates, Team Identifier, Application Identifier Prefix, application identifier, profile type, device-list key presence (including an empty array), enterprise flag, `get-task-allow`, entitlements, exact SHA-256 membership of the signing certificate, certificate dates, and key/certificate public-key matching. Store export additionally requires a current certificate and profile, verified explicit chain, exact non-wildcard bundle ID, internally consistent identifiers, App Store profile with no `ProvisionedDevices` key or enterprise flag, `get-task-allow=false`, and the selected certificate in `DeveloperCertificates`.

The Application Identifier Prefix is validated and retained separately from the Team Identifier. Signing derives `application-identifier` and the default keychain access group from the former, while `com.apple.developer.team-identifier` uses the latter. They are often equal but are not interchangeable: Apple's [TN2311](https://developer.apple.com/library/archive/technotes/tn2311/_index.html) documents legacy prefixes that differ from the Team ID. Pomeforge never silently migrates such a prefix because that could change keychain access.

Requested entitlements are an explicit compatible subset of profile grants. String and list grants support Apple's trailing `*` prefix pattern. Unsupported keys, absent grants, and incompatible values are machine-readable errors. The exporter creates exact application/team identifiers and never copies a profile-only wildcard value into the app. Add a new entitlement to the package's reviewed allowlist before an adapter can request it.

## Inspection

`InspectBundle(ctx, absoluteAppPath, options)` scans a real `.app` without modifying it. `InspectIPA(ctx, absoluteIPAPath, options)` hashes and reads an IPA without extracting it. Both return `Result[T]`, which separates typed facts (`Value`) from stable `Problem` values (`code`, `severity`, `field`, and `message`). `Result.Valid()` is false when an error-severity problem is present.

Default limits include a 2 GiB IPA, 4 GiB expanded bundle, 512 MiB individual file, 20,000 archive entries, and a 200:1 per-entry compression ratio. Reads poll `context.Context`. Linux file access opens every path component and leaf without following links; leaves are opened nonblocking and verified from the descriptor before reading, so a FIFO replacement cannot block a sensitive or bundle read. Directory trees are traversed descriptor-relative with the same no-follow rule. Executable and primary-icon metadata must name simple contained leaves before any derived file read or archive lookup; malformed executable metadata produces the stable `invalid_executable_name` problem. IPA inspection rejects absolute, traversal, backslash, NUL, duplicate, symlink/special archive entries, normalized file/directory collisions, regular-file ancestors, explicit directory-kind mismatches, excessive counts/sizes/ratios, entries outside the single `Payload/*.app`, multiple or missing main bundles, unsuitable directory/file/executable modes, and unsupported nested signing targets. Bundle export currently rejects frameworks, extensions, and Watch content: extensions need their own profile/identity validation, while frameworks need correct nested code-signing treatment but do not inherently require separate provisioning profiles.

The inspector decodes XML and binary `Info.plist`, records bundle/version/build, iPhone/iPad families, minimum OS, icons, SDK/build provenance, embedded profile facts, and the main executable. The executable must contain an arm64 slice and iOS `LC_BUILD_VERSION` or legacy `LC_VERSION_MIN_IPHONEOS` minimum/SDK metadata. Info metadata is checked for completeness and consistency with Mach-O; the library never fabricates `DTXcode`, `DTSDK*`, or `DTPlatform*` fields. Compiled icon validation requires and fully decodes AssetKit's 15 distinct loose PNG filenames corresponding to the supported 18 iPhone/iPad/marketing slots, verifies their exact dimensions, and checks the matching icon metadata rather than trusting names alone.

`StructureValid` means only that these local structural checks passed. The report records whether `_CodeSignature/CodeResources` exists but deliberately reports code-signature cryptographic verification as `not_performed`. It is not App Store processing, on-device launch, OCSP status, or Apple acceptance.

## Export

Call `PreflightExport` for one typed, read-only aggregate readiness result, or call `PlanExport`, which delegates to that same preflight and adds display-safe actions. Preflight validates the concrete no-symlink tool executables, private identity, source topology, SDK/version/family metadata, requested entitlements, destination availability, and either a selected complete catalog or complete precompiled icons. It does not compile assets, sign, create output, or mutate the source. An unsigned pre-AssetKit bundle may omit generated icon metadata/files when a valid catalog is selected. Only the exact root `_CodeSignature`, root `CodeResources`, and root `embedded.mobileprovision` artifacts that export replaces are ignored; unrelated problems remain blocking. Callers that manage tools must resolve and verify a concrete executable before constructing the request rather than passing a managed symlink.

The plan shows this confirmed AssetKit contract when a catalog is selected:

```text
pomeforge-assets compile --catalog <validated-catalog> --app <private-staged-app> --minimum-ios 17.0 --json
```

Complete explicit PNG slots are required for iPhone (20, 29, 40, and 60 points at applicable 2x/3x scales), iPad (20, 29, 40, 76, and 83.5 points at applicable 1x/2x scales), and the 1024-point marketing icon. Catalog trees and filenames are containment-checked, every input must be regular, and PNG dimensions must match the declared slot.

Export creates a mode-`0700` temporary directory beside the destination so final no-replace publication is atomic. It snapshots the exact validated key, certificate, profile, and trust-root bytes into a private directory and passes only those immutable snapshots to zsign. A dedicated archive root contains only `Payload/NAME.app`; identity snapshots, generated entitlements, catalog scratch, and signer output remain outside it. The bounded descriptor-relative copy leaves the source untouched and omits only root `_CodeSignature`, root `CodeResources`, and root `embedded.mobileprovision` from staging. AssetKit runs after that omission and before signing. Generated entitlements are a mode-`0600` temporary plist.

The runner receives this exact zsign 1.1.2 argument array internally, without a shell:

```text
zsign -k KEY-SNAPSHOT.pem -c CERT-SNAPSHOT.pem -m PROFILE-SNAPSHOT.mobileprovision -e ENTITLEMENTS.plist -o STAGED_OUTPUT.ipa ARCHIVE/Payload/STAGED.app
```

The public plan substitutes placeholders for key, certificate, profile, entitlements, staging app, and staging output paths. A runner is trusted with those paths only for process execution and must not log or persist its `Action`. Runner errors returned by the package omit the runner's potentially sensitive error text.

After zsign returns, export requires a bounded regular output, checks that it contains only the supported application payload, re-runs IPA inspection, and checks exact bundle/version/build/minimum OS, embedded profile UUID and byte hash, explicit chain, profile signature integrity, and code-resource presence. Exact zsign 1.1.2 emits DOS-style ZIP attributes, so Pomeforge then repacks those already-signed file bytes without modifying bundle content or signatures, assigning conventional `0755` directory/main-executable and `0644` resource modes. The canonical archive is fully re-inspected before an atomic no-replace hard-link publication in the destination filesystem. An existing output is never overwritten, including if it appears during export. The receipt includes the canonical IPA SHA-256 and size, inspection and identity facts, and `appleProcessing: "not_checked"`.

## Current boundaries

- Frameworks, app extensions, Watch apps, and other nested signing targets are rejected until exact per-target identities, profiles, and entitlement validation are implemented.
- Asset catalogs support the explicit PNG app-icon coverage above. This library does not run `actool` or accept Xcode-only resource workflows.
- The library does not cryptographically validate the Mach-O CodeDirectory/CMS signature or perform revocation checks; it reports code-resource presence, verifies provisioning-profile CMS integrity and configured trust separately, and relies on zsign's successful output only as one fact.
- Synthetic zsign tests validate staging, argv, output handling, and reinspection. They are not real Apple signing, device installation, App Store upload, processing, TestFlight, or review evidence.
- No Apple account operation, certificate issuance, upload, validation, submission, or release mutation exists in this package.

Dependencies are pinned in `go.mod`: `howett.net/plist v1.0.1` and `github.com/smallstep/pkcs7 v0.2.3`, with module and archive hashes retained in `go.sum`.
