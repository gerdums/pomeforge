// swift-tools-version: 6.3
import PackageDescription

let package = Package(
    name: "PomeforgeAssets",
    products: [
        .executable(name: "pomeforge-assets", targets: ["PomeforgeAssets"]),
    ],
    dependencies: [
        .package(
            url: "https://github.com/xtool-org/AssetKit",
            revision: "e763558b55fcbb5a443b1d7b2c6f0972d8bd14f7"
        ),
    ],
    targets: [
        .target(
            name: "PomeforgeAssetsCore",
            dependencies: [
                .product(name: "AssetKit", package: "AssetKit"),
            ]
        ),
        .executableTarget(
            name: "PomeforgeAssets",
            dependencies: ["PomeforgeAssetsCore"]
        ),
        .testTarget(
            name: "PomeforgeAssetsCoreTests",
            dependencies: ["PomeforgeAssetsCore"]
        ),
    ],
    swiftLanguageModes: [.v6]
)
