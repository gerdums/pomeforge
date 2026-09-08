// swift-tools-version: 6.3
import PackageDescription

let package = Package(
    name: "OrchardAssets",
    products: [
        .executable(name: "orchard-assets", targets: ["OrchardAssets"]),
    ],
    dependencies: [
        .package(
            url: "https://github.com/xtool-org/AssetKit",
            revision: "e763558b55fcbb5a443b1d7b2c6f0972d8bd14f7"
        ),
    ],
    targets: [
        .target(
            name: "OrchardAssetsCore",
            dependencies: [
                .product(name: "AssetKit", package: "AssetKit"),
            ]
        ),
        .executableTarget(
            name: "OrchardAssets",
            dependencies: ["OrchardAssetsCore"]
        ),
        .testTarget(
            name: "OrchardAssetsCoreTests",
            dependencies: ["OrchardAssetsCore"]
        ),
    ],
    swiftLanguageModes: [.v6]
)
