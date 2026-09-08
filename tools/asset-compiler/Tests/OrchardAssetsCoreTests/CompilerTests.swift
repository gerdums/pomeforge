import Foundation
import XCTest
@testable import OrchardAssetsCore

final class CompilerTests: XCTestCase {
    func testSHA256KnownVector() {
        XCTAssertEqual(
            SHA256.hexDigest(Data("abc".utf8)),
            "ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad"
        )
    }

    func testOptionValidationAndHelp() throws {
        XCTAssertEqual(try CLIParser.parse([]), .help)
        XCTAssertEqual(try CLIParser.parse(["--help"]), .help)
        XCTAssertThrowsError(try CLIParser.parse(["other"]))
        XCTAssertThrowsError(try CLIParser.parse(["compile", "--catalog", "A.xcassets"]))
        XCTAssertThrowsError(try CLIParser.parse(["compile", "--catalog", "A.xcassets", "--app", "A.app", "--wat"]))
        XCTAssertThrowsError(try CLIParser.parse(["compile", "--catalog", "A.xcassets", "--app", "A.app", "--minimum-ios", "17.x"]))

        let action = try CLIParser.parse([
            "compile", "--catalog", "A.xcassets", "--app", "A.app", "--minimum-ios", "18.1", "--json",
        ])
        XCTAssertEqual(action, .compile(CompileOptions(catalogPath: "A.xcassets", appPath: "A.app", minimumIOS: "18.1", json: true)))
    }

    func testRealPNGAppIconCompilationAndPlistPreservation() async throws {
        let fixture = try Fixture()
        defer { fixture.cleanup() }
        try fixture.makeValidCatalog()
        try fixture.makeApp(plist: [
            "CFBundleIdentifier": "dev.orchard.fixture",
            "CFBundleVersion": "7",
            "Unrelated": ["preserved": true],
        ])

        let report = try await OrchardAssetCompiler.compile(CompileOptions(
            catalogPath: fixture.catalog.path,
            appPath: fixture.app.path,
            minimumIOS: "17.0",
            json: true
        ))

        XCTAssertTrue(report.success)
        XCTAssertTrue(report.emittedFiles.contains { $0.relativePath == "Assets.car" && $0.bytes > 0 && $0.sha256.count == 64 })
        XCTAssertTrue(report.emittedFiles.contains { $0.relativePath == "AppIcon60x60@2x.png" })
        XCTAssertTrue(FileManager.default.fileExists(atPath: fixture.app.appendingPathComponent("Assets.car").path))
        XCTAssertEqual(try fixture.plist()["CFBundleIdentifier"] as? String, "dev.orchard.fixture")
        XCTAssertEqual((try fixture.plist()["Unrelated"] as? [String: Bool])?["preserved"], true)
        XCTAssertEqual(try fixture.plist()["CFBundleIconName"] as? String, "AppIcon")
    }

    func testCompilationFailurePreservesOriginalApp() async throws {
        let fixture = try Fixture()
        defer { fixture.cleanup() }
        try fixture.makeBrokenCatalog()
        try fixture.makeApp(plist: ["CFBundleIdentifier": "dev.orchard.unchanged", "Sentinel": "original"])
        let original = try Data(contentsOf: fixture.app.appendingPathComponent("Info.plist"))

        do {
            _ = try await OrchardAssetCompiler.compile(CompileOptions(catalogPath: fixture.catalog.path, appPath: fixture.app.path))
            XCTFail("compile unexpectedly succeeded")
        } catch {}

        XCTAssertEqual(try Data(contentsOf: fixture.app.appendingPathComponent("Info.plist")), original)
        XCTAssertFalse(FileManager.default.fileExists(atPath: fixture.app.appendingPathComponent("Assets.car").path))
    }

    func testRejectsSignedBundleAndOverlappingOrSymlinkInputs() async throws {
        let fixture = try Fixture()
        defer { fixture.cleanup() }
        try fixture.makeValidCatalog()
        try fixture.makeApp(plist: ["CFBundleIdentifier": "dev.orchard.signed"])
        try FileManager.default.createDirectory(at: fixture.app.appendingPathComponent("_CodeSignature"), withIntermediateDirectories: false)

        await XCTAssertThrowsErrorAsync {
            _ = try await OrchardAssetCompiler.compile(CompileOptions(catalogPath: fixture.catalog.path, appPath: fixture.app.path))
        }

        try FileManager.default.removeItem(at: fixture.app.appendingPathComponent("_CodeSignature"))
        let symlink = fixture.root.appendingPathComponent("Linked.app")
        try FileManager.default.createSymbolicLink(at: symlink, withDestinationURL: fixture.app)
        await XCTAssertThrowsErrorAsync {
            _ = try await OrchardAssetCompiler.compile(CompileOptions(catalogPath: fixture.catalog.path, appPath: symlink.path))
        }

        let nestedCatalog = fixture.app.appendingPathComponent("Nested.xcassets")
        try FileManager.default.copyItem(at: fixture.catalog, to: nestedCatalog)
        await XCTAssertThrowsErrorAsync {
            _ = try await OrchardAssetCompiler.compile(CompileOptions(catalogPath: nestedCatalog.path, appPath: fixture.app.path))
        }
    }

    func testRejectsSymlinkOutputDestination() async throws {
        let fixture = try Fixture()
        defer { fixture.cleanup() }
        try fixture.makeValidCatalog()
        try fixture.makeApp(plist: ["CFBundleIdentifier": "dev.orchard.safe"])
        let outside = fixture.root.appendingPathComponent("outside.car")
        try Data("outside".utf8).write(to: outside)
        try FileManager.default.createSymbolicLink(
            at: fixture.app.appendingPathComponent("Assets.car"),
            withDestinationURL: outside
        )

        await XCTAssertThrowsErrorAsync {
            _ = try await OrchardAssetCompiler.compile(CompileOptions(catalogPath: fixture.catalog.path, appPath: fixture.app.path))
        }
        XCTAssertEqual(try String(contentsOf: outside, encoding: .utf8), "outside")
    }
}

private struct Fixture {
    let root: URL
    let catalog: URL
    let app: URL

    init() throws {
        root = FileManager.default.temporaryDirectory.appendingPathComponent("OrchardAssetsTests-\(UUID().uuidString)", isDirectory: true)
        catalog = root.appendingPathComponent("Assets.xcassets", isDirectory: true)
        app = root.appendingPathComponent("Fixture.app", isDirectory: true)
        try FileManager.default.createDirectory(at: root, withIntermediateDirectories: false)
    }

    func cleanup() { try? FileManager.default.removeItem(at: root) }

    func makeValidCatalog() throws {
        try FileManager.default.createDirectory(at: catalog, withIntermediateDirectories: true)
        try Data(#"{"info":{"author":"xcode","version":1}}"#.utf8)
            .write(to: catalog.appendingPathComponent("Contents.json"))
        let icon = catalog.appendingPathComponent("AppIcon.appiconset", isDirectory: true)
        try FileManager.default.createDirectory(at: icon, withIntermediateDirectories: false)
        try png.write(to: icon.appendingPathComponent("Icon.png"))
        try Data(#"{"images":[{"idiom":"iphone","size":"60x60","scale":"2x","filename":"Icon.png"}],"info":{"author":"xcode","version":1}}"#.utf8)
            .write(to: icon.appendingPathComponent("Contents.json"))
    }

    func makeBrokenCatalog() throws {
        try FileManager.default.createDirectory(at: catalog, withIntermediateDirectories: true)
        try Data(#"{"info":{"author":"xcode","version":1}}"#.utf8)
            .write(to: catalog.appendingPathComponent("Contents.json"))
        let icon = catalog.appendingPathComponent("AppIcon.appiconset", isDirectory: true)
        try FileManager.default.createDirectory(at: icon, withIntermediateDirectories: false)
        try Data(#"{"images":[{"idiom":"iphone","size":"60x60","scale":"2x","filename":"missing.png"}],"info":{"author":"xcode","version":1}}"#.utf8)
            .write(to: icon.appendingPathComponent("Contents.json"))
    }

    func makeApp(plist: [String: Any]) throws {
        try FileManager.default.createDirectory(at: app, withIntermediateDirectories: true)
        let data = try PropertyListSerialization.data(fromPropertyList: plist, format: .xml, options: 0)
        try data.write(to: app.appendingPathComponent("Info.plist"))
    }

    func plist() throws -> [String: Any] {
        let data = try Data(contentsOf: app.appendingPathComponent("Info.plist"))
        return try XCTUnwrap(
            PropertyListSerialization.propertyList(from: data, options: [], format: nil) as? [String: Any]
        )
    }
}

private func XCTAssertThrowsErrorAsync(
    _ expression: () async throws -> Void,
    file: StaticString = #filePath,
    line: UInt = #line
) async {
    do {
        try await expression()
        XCTFail("expected error", file: file, line: line)
    } catch {}
}

private let png = Data([
    0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a,
    0x00, 0x00, 0x00, 0x0d, 0x49, 0x48, 0x44, 0x52,
    0x00, 0x00, 0x00, 0x01, 0x00, 0x00, 0x00, 0x01,
    0x08, 0x04, 0x00, 0x00, 0x00, 0xb5, 0x1c, 0x0c,
    0x02, 0x00, 0x00, 0x00, 0x0b, 0x49, 0x44, 0x41,
    0x54, 0x78, 0xda, 0x63, 0x64, 0xf8, 0x0f, 0x00,
    0x01, 0x05, 0x01, 0x01, 0x27, 0x18, 0xe3, 0x66,
    0x00, 0x00, 0x00, 0x00, 0x49, 0x45, 0x4e, 0x44, 0xae,
    0x42, 0x60, 0x82,
])
