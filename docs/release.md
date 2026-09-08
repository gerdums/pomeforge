# Native Linux release workflow

Pomeforge keeps the unsigned release build, App Store distribution signing,
upload, remote readiness validation, and review submission as separate explicit
operations. Every build and signing step runs locally on Linux. There is no Mac,
remote Xcode, hosted build, or AI-only fallback.

Local inspection proves only the facts it reports. It does not prove that Apple
will process or accept an IPA. Development signing is not App Store distribution
signing, and an App Store IPA is not a physical-device development artifact.

## 1. Prepare App Store Connect access

Managed installs do not alter `PATH`. On a native host, expose Pomeforge's
user-owned bin directory for this session before running ASC directly:

```sh
case ${XDG_DATA_HOME:-} in
  /*) pomeforge_data_home=$XDG_DATA_HOME ;;
  *) pomeforge_data_home=$HOME/.local/share ;;
esac
export PATH="$pomeforge_data_home/pomeforge/bin:$PATH"
install -d -m 0700 "$HOME/.config/pomeforge-private"
```

An Account Holder or Admin first requests App Store Connect API access in the
browser if the team has not enabled it. In App Store Connect, open **Users and
Access > Integrations > Team Keys**, create a team API key, and save the one-time
`.p8` download in a private directory outside every source repository.

Authenticate the pinned ASC 5 CLI explicitly:

```sh
asc auth login --bypass-keychain --name pomeforge \
  --key-id KEY_ID --issuer-id ISSUER_ID \
  --private-key "$HOME/.config/pomeforge-private/AuthKey.p8"
asc auth status --validate
```

Do not pass `--local`: it writes project-local `.asc` state. `auth status
--validate` performs a network account check only when the operator runs it.
The API key authenticates ASC; it is not the private key that signs the app.

## 2. Create signing resources

Use `umask 077` and a writable user-owned private directory outside the
canonical Pomeforge workspace. Never use `--force` to replace a private key.

```sh
umask 077
signing_dir=$HOME/.config/pomeforge-private/signing
install -d -m 0700 "$signing_dir"
asc certificates csr generate \
  --key-out "$signing_dir/distribution.key" \
  --csr-out "$signing_dir/distribution.csr" --output json
asc certificates create --certificate-type IOS_DISTRIBUTION \
  --csr "$signing_dir/distribution.csr" --output json
asc certificates view --id CERT_RESOURCE_ID --output json
asc certificates list --certificate-type IOS_DISTRIBUTION --paginate
asc bundle-ids create --identifier com.example.app --name App \
  --platform IOS --output json
asc bundle-ids list --identifier com.example.app
asc profiles create --name AppStore --profile-type IOS_APP_STORE \
  --bundle BUNDLE_RESOURCE_ID --certificate CERT_RESOURCE_ID
asc profiles download --id PROFILE_RESOURCE_ID \
  --output "$signing_dir/AppStore.mobileprovision"
```

Bundle, certificate, profile, version, and build resource IDs are server IDs;
the bundle resource ID is not `com.example.app`. Decode
`.data.attributes.certificateContent` from the certificate response as base64
DER, save it as `certificate.cer`, then convert it:

```sh
openssl x509 -inform DER -in "$signing_dir/certificate.cer" \
  -out "$signing_dir/certificate.pem"
```

Export requires explicit verified trust roots. Download only the intended root
certificates linked by Apple's [primary certificate-authority
index](https://www.apple.com/certificateauthority/), verify the downloaded DER
bytes, and convert them to PEM:

```sh
curl --fail --location --proto '=https' --tlsv1.2 \
  https://www.apple.com/appleca/AppleIncRootCertificate.cer \
  -o "$signing_dir/AppleIncRootCertificate.cer"
curl --fail --location --proto '=https' --tlsv1.2 \
  https://www.apple.com/certificateauthority/AppleRootCA-G2.cer \
  -o "$signing_dir/AppleRootCA-G2.cer"
curl --fail --location --proto '=https' --tlsv1.2 \
  https://www.apple.com/certificateauthority/AppleRootCA-G3.cer \
  -o "$signing_dir/AppleRootCA-G3.cer"
printf '%s  %s\n' \
  B0B1730ECBC7FF4505142C49F1295E6EDA6BCAED7E2C68C5BE91B5A11001F024 "$signing_dir/AppleIncRootCertificate.cer" \
  C2B9B042DD57830E7D117DAC55AC8AE19407D38E41D88F3215BC3A890444A050 "$signing_dir/AppleRootCA-G2.cer" \
  63343ABFB89A6A03EBB57E9B3F5FA7BE7C4F5C756F3017B3A8C488C3653E9179 "$signing_dir/AppleRootCA-G3.cer" \
  | sha256sum --check
for root in AppleIncRootCertificate AppleRootCA-G2 AppleRootCA-G3; do
  openssl x509 -inform DER -in "$signing_dir/$root.cer" \
    -out "$signing_dir/$root.pem"
done
```

These SHA-256 values were measured from the HTTPS downloads on 2026-09-08;
they are not independently signed publisher checksums. Linux OpenSSL verified
WWDR G3 to the original Apple root and WWDR G6 to Root G3, but profiles can use
different issuer chains. Configure only intended official roots as anchors;
never promote an intermediate or a certificate embedded in a profile to a
trust root. Trust roots are optional only for limited inspection, not export.

Configure a safe named identity. The paths and resulting private JSON config
stay under private user state and never appear in plans, results, history, or
browser storage:

```sh
pomeforge signing configure app-store-main \
  --label "App Store distribution" \
  --private-key "$signing_dir/distribution.key" \
  --certificate "$signing_dir/certificate.pem" \
  --profile "$signing_dir/AppStore.mobileprovision" \
  --trust-root "$signing_dir/AppleIncRootCertificate.pem" \
  --trust-root "$signing_dir/AppleRootCA-G2.pem" \
  --trust-root "$signing_dir/AppleRootCA-G3.pem" --execute --json
pomeforge signing list --json
pomeforge signing inspect app-store-main --json
```

The prerequisites view exposes the same configuration and inspection flow.
Encrypted private keys are unsupported because zsign's password flag would put
the password in argv; Pomeforge does not prompt for or pass a password. Resolve
reported expiration, certificate/profile membership, profile type, bundle ID,
team/prefix, entitlement, or trust-root problems before export.

## 3. Bind and build with the active SDK

Import and inspect the operator-supplied SDK as described in
[`setup.md`](setup.md). A release build requires the active `darwin` Swift SDK,
the exact `arm64-apple-ios` configuration, `xtool sdk status`, and matching
private import provenance. Pomeforge rejects missing, stale, or changed
SDKSettings, SystemVersion, platform version, target-triple, or host-variant
facts.

Generated projects already contain the controlled iOS linker hook. Existing
projects are never rewritten; follow [Linux toolchain
decisions](linux-toolchain-decisions.md) to adopt the hook deliberately.
Pomeforge supplies the canonical
validated SDK root only to the selected xtool release build and independently
checks the resulting Mach-O `LC_BUILD_VERSION`. It never hardcodes an SDK number
or copies a private path into source.

The generated starter catalog is complete and may be exported locally, but it
is intentionally recognizable starter art. Put the project's 1024 PNG inside
the selected project before replacing it:

```sh
project=$HOME/PomeforgeProjects/App
install -m 0600 /absolute/path/to/your/Icon-1024.png "$project/Icon-1024.png"
pomeforge run icons --project "$project" --icon-source Icon-1024.png --execute --json
```

The source must be a 1024x1024 PNG. Deterministic PNG processing
creates all explicit iPhone, iPad, and marketing sizes supported by the pinned
AssetKit bridge. Pomeforge refuses to overwrite a catalog whose starter-art ownership marker
has already been removed.

Build the unsigned release bundle separately:

```sh
pomeforge plan release-build --project ./App --source-bundle xtool/App.app --json
pomeforge run release-build --project ./App --source-bundle xtool/App.app \
  --execute --json
```

After compilation, Pomeforge writes DTPlatform, DTSDK, and DTXcode keys only from
the named import provenance, before resources or signing, and verifies the real
Mach-O SDK stamp while preserving the deployment minimum.

Source bundles, catalogs, icon sources, output IPAs, and inspected IPAs must be
inside the selected project. Relative values resolve from that project, never
from the shell's current directory. Signing material uses absolute paths and
must remain outside the canonical workspace.

## 4. Export and inspect

```sh
pomeforge plan export --project ./App --source-bundle xtool/App.app \
  --identity app-store-main --output-ipa App.ipa \
  --asset-catalog Assets.xcassets --json
pomeforge run export --project ./App --source-bundle xtool/App.app \
  --identity app-store-main --output-ipa App.ipa \
  --asset-catalog Assets.xcassets --execute --json
pomeforge run ipa-inspect --project ./App --ipa App.ipa \
  --identity app-store-main --execute --json
```

Export copies the source into fresh private staging, compiles AssetKit resources
before zsign, snapshots identity material, signs, canonicalizes the archive, and
publishes atomically without replacement. The source app and catalog are not
modified. Nested frameworks, extensions, and Watch content remain unsupported
until each nested target has its own validated signing topology. The receipt
includes structure, Mach-O, Info, profile, icons, SHA-256, and
`appleProcessing: "not_checked"`.

## 5. Upload, validate, and submit separately

Create the app record first in App Store Connect with **Apps > + > New App** and
accept the latest agreement if required. The GUI exposes app, version, and build
IDs; the same values may be passed directly to the CLI, without editing JSON.

```sh
pomeforge plan upload --project ./App --ipa App.ipa \
  --identity app-store-main --app-id 123456789 --json
pomeforge run upload --project ./App --ipa App.ipa \
  --identity app-store-main --app-id 123456789 --execute --confirm --json

pomeforge run store-status --project ./App \
  --build-id BUILD_RESOURCE_ID --execute --json
pomeforge run validate --project ./App --app-id 123456789 \
  --version-id VERSION_RESOURCE_ID --execute --json

pomeforge plan submit --project ./App --app-id 123456789 \
  --version-id VERSION_RESOURCE_ID --build-id BUILD_RESOURCE_ID --json
pomeforge run submit --project ./App --app-id 123456789 \
  --version-id VERSION_RESOURCE_ID --build-id BUILD_RESOURCE_ID \
  --execute --confirm --json
```

Upload binds and rechecks the exact IPA bytes, SHA-256, identifiers, version,
profile, and SDK facts immediately before ASC receives
`builds upload --app ID --ipa PATH --wait --output json`. It never submits after
upload. Validation uses exactly one `--version-id` or `--version`. Submission
first runs `review submit ... --dry-run`, then uses `--confirm` only inside the
explicitly confirmed submit action. ASC alone receives account credential
environment variables. Actual bounded, redacted ASC output and failures remain
available in the operation result.

For a persistent Podman container created as documented in
[`container.md`](container.md), the `pomeforge_container` helper always invokes
Pomeforge. Run ASC itself with the same state/workspace mounts and the managed
absolute executable path instead:

```sh
podman run --rm --userns=keep-id --user "$(id -u):$(id -g)" \
  --volume "$state_dir:/var/lib/pomeforge:Z" \
  --volume "$workspace_dir:/workspace:Z" \
  localhost/pomeforge:local \
  /var/lib/pomeforge/data/pomeforge/bin/asc auth status --validate
```

The container entrypoint supplies the same private `HOME` and XDG directories
used by Pomeforge. Use the equivalent Docker mounts from the container guide when
Docker is the selected engine.
