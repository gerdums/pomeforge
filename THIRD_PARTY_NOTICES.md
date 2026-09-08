# Third-party components

Pomeforge's original source code and documentation use the [MIT License](LICENSE). This grant does not replace the licenses or copyright notices of the third-party components below. Retain Pomeforge's license, this notice, and the applicable linked license texts when distributing binaries built from them.

| Component | Pin | Use | License text |
| --- | --- | --- | --- |
| [Go](https://go.dev/) | 1.24.13 | Runtime and standard library included in the CLI | [BSD 3-Clause](docs/third-party/Go-LICENSE.txt) |
| [smallstep/pkcs7](https://github.com/smallstep/pkcs7) | v0.2.3 | Provisioning profile CMS signatures | [MIT](docs/third-party/pkcs7-LICENSE.txt) |
| [go-plist](https://github.com/DHowett/go-plist) | v1.0.1 | Apple XML and binary property lists | [BSD notices](docs/third-party/go-plist-LICENSE.txt) |
| [AssetKit](https://github.com/xtool-org/AssetKit) | e763558b55fcbb5a443b1d7b2c6f0972d8bd14f7 | Linux asset catalog compilation | [MIT](docs/third-party/AssetKit-LICENSE.txt) |
| [Apple LZFSE](https://github.com/lzfse/lzfse) | Vendored by the pinned AssetKit revision | Asset compression | [BSD 3-Clause](docs/third-party/LZFSE-LICENSE.txt) |
| [swift-png](https://github.com/tayloraswift/swift-png) | 4.5.1 | PNG processing through AssetKit | [Apache-2.0](docs/third-party/swift-png-LICENSE.txt), [NOTICE](docs/third-party/swift-png-NOTICE.txt) |
| [unxip](https://github.com/saagarjha/unxip) | 3.3.0, `6c3990517fcc4c1db6952fccf4c562fb14097601` | Separate Linux XIP extraction executable | [LGPLv3](docs/third-party/unxip-LICENSE.txt), [GPLv3](docs/third-party/GPL-3.0.txt) |
| [h](https://github.com/rarestype/h) | 1.0.1 | Transitive Swift dependency | [Apache-2.0](docs/third-party/h-LICENSE.txt), [NOTICE](docs/third-party/h-NOTICE.txt) |

`go.sum` and the asset compiler's `Package.resolved` record dependency integrity. Go and Swift toolchains carry their own runtime and standard-library notices.

xtool, ASC CLI, and zsign remain separate upstream executables, downloaded on explicit request from the releases pinned in [toolchains.lock.json](toolchains.lock.json). Their repositories and releases contain the applicable notices and source. Device services and optional device tools also remain separate programs. Pomeforge does not bundle an Apple SDK; the operator supplies it under their applicable terms.

The Linux container packages unxip as a separate, unmodified executable. Distributions that include that binary must also include its pinned corresponding source archive, these license texts, and the reproducible Linux build instructions. The container recipe retains that source separately from the Pomeforge application.
