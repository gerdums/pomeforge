# Linux distribution integration packet

Implement the working native pipeline after integrating the core, graphical app, bootstrap package, and AssetKit bridge. This is source implementation and local synthetic validation, not authorization to issue certificates, change signing policy, upload, or submit a real app.

## Required behavior

1. Connect the managed installer to discoverable CLI and app operations. `tools install` must first show pinned versions, download sizes, and destinations; explicit execution installs checked dependencies in user-owned XDG state. Tool discovery must prefer successfully verified managed installs without changing the system PATH or accepting an arbitrary executable from the browser. Add Swift/USB prerequisite instructions; do not download Apple SDKs silently.
2. Offer a user-provided SDK import operation using actual `xtool sdk build INPUT NEW_OUTPUT --arch ...` or `sdk install INPUT` contracts. Avoid silently deleting/replacing an existing SDK. Import must retain source SHA-256 and selected tool/SDK metadata. The original Apple SDK terms remain linked. Do not fabricate Xcode provenance fields.
3. Add a private signing configuration outside tracked project files. Accept separate PEM private key/certificate and provisioning profile files. Do not put private-key contents, passphrases, or authentication tokens on argv, in API payloads, or in receipts. Key paths may be selected by the local operator but should be redacted in public plans and persisted output. Unsupported encrypted identities get a useful diagnostic, never a password-on-argv workaround.
4. Implement local profile/identity inspection. Check certificate dates, matching key public key, certificate membership in the profile, bundle/team identity, profile expiry and distribution type, and entitlements. Distinguish signature verification from mere CMS decoding. An App Store profile has no device list and must not enable get-task-allow. Never label structural validation complete signature verification.
5. Export from a staged copy of the real unsigned app bundle. Run the AssetKit bridge before signing, merge its icon metadata, then sign through zsign using the verified identity/profile. Detect missing full icon coverage and missing truthful SDK/build metadata. Never silently use xtool's development signer for store output. Preserve the original build and keep distinct development/distribution artifacts.
6. Inspect the exported IPA and retain SHA-256, bundle ID, version, build number, profile type and key provenance. Apple acceptance remains unverified until upload processing succeeds. Check expected single main bundle, arm64 Mach-O/platform metadata, icon metadata, and bounded archive extraction. Extensions require exact additional profiles or a clear unsupported diagnostic.
7. Integrate confirmed ASC 5 native upload, remote validation, and review submit. Submit uses `asc review submit --app ID --version-id ID --build-id ID --platform IOS --confirm --output json`; preview uses --dry-run. Never auto-submit when upload succeeds. Keep remote app/version/build IDs explicit. Require a concrete reviewed plan for every external write and bind it to the IPA hash or build ID.

## Implementation guidance

Prefer existing tested open-source parsers when standard Go lacks binary plist or CMS support. Pin and document any new dependencies. Never invent an upstream command. `docs/toolchain-research.md` and live Linux binary help in `docs/upstream-command-reference.md` are the evidence baseline.

Keep configuration schemas and CLI/HTTP JSON documented. Humans can perform these workflows without AI. Tests should cover malformed/expired/wrong-type profiles, wrong private keys, plan invalidation, missing output, failed signing, unchanged source bundle, archive traversal, and refusal of unconfirmed uploads/submission. Synthetic signing fixtures are acceptable when clearly labeled and never reused as trusted Apple profiles.

## Completion evidence

Provide exact integrated SHA, passing relevant tests, Linux binary execution, an actual Linux AssetKit wrapper test, installer runs with upstream releases, and graphical interaction proof. State the SDK/device/account prerequisites that prevent true build/store validation; do not represent those gates as passed.
