import Foundation
import XCTest
@testable import PomeforgeAssetsCore

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
            "CFBundleIdentifier": "dev.pomeforge.fixture",
            "CFBundleVersion": "7",
            "Unrelated": ["preserved": true],
        ])

        let report = try await PomeforgeAssetCompiler.compile(CompileOptions(
            catalogPath: fixture.catalog.path,
            appPath: fixture.app.path,
            minimumIOS: "17.0",
            json: true
        ))

        XCTAssertTrue(report.success)
        XCTAssertTrue(report.emittedFiles.contains { $0.relativePath == "Assets.car" && $0.bytes > 0 && $0.sha256.count == 64 })
        XCTAssertTrue(report.emittedFiles.contains { $0.relativePath == "AppIcon60x60@2x.png" })
        XCTAssertTrue(FileManager.default.fileExists(atPath: fixture.app.appendingPathComponent("Assets.car").path))
        XCTAssertEqual(try fixture.plist()["CFBundleIdentifier"] as? String, "dev.pomeforge.fixture")
        XCTAssertEqual((try fixture.plist()["Unrelated"] as? [String: Bool])?["preserved"], true)
        XCTAssertEqual(try fixture.plist()["CFBundleIconName"] as? String, "AppIcon")
        XCTAssertFalse(try fixture.hasTransactionStage())
    }

    func testCompleteIPhoneAndIPadCatalogCoalescesIdenticalLooseIcons() async throws {
        let fixture = try Fixture()
        defer { fixture.cleanup() }
        try fixture.makeCompleteIconCatalog()
        try fixture.makeApp(plist: [
            "CFBundleIdentifier": "dev.pomeforge.full-icons",
            "CFBundleVersion": "42",
            "Unrelated": ["preserved": true],
        ])

        let report = try await PomeforgeAssetCompiler.compile(CompileOptions(
            catalogPath: fixture.catalog.path,
            appPath: fixture.app.path,
            minimumIOS: "17.0",
            json: true
        ))

        XCTAssertTrue(report.success)
        XCTAssertEqual(report.emittedFiles.filter { $0.relativePath == "AppIcon20x20@2x.png" }.count, 1)
        XCTAssertTrue(report.emittedFiles.contains { $0.relativePath == "Assets.car" && $0.bytes > 0 })
        let plist = try fixture.plist()
        XCTAssertEqual(plist["CFBundleIdentifier"] as? String, "dev.pomeforge.full-icons")
        XCTAssertEqual((plist["Unrelated"] as? [String: Bool])?["preserved"], true)
        let phoneIcons = try XCTUnwrap(plist["CFBundleIcons"] as? [String: Any])
        let phonePrimary = try XCTUnwrap(phoneIcons["CFBundlePrimaryIcon"] as? [String: Any])
        XCTAssertEqual(phonePrimary["CFBundleIconName"] as? String, "AppIcon")
        XCTAssertEqual(phonePrimary["CFBundleIconFiles"] as? [String], [
            "AppIcon20x20", "AppIcon29x29", "AppIcon40x40", "AppIcon60x60",
        ])
        let padIcons = try XCTUnwrap(plist["CFBundleIcons~ipad"] as? [String: Any])
        let padPrimary = try XCTUnwrap(padIcons["CFBundlePrimaryIcon"] as? [String: Any])
        XCTAssertEqual(padPrimary["CFBundleIconName"] as? String, "AppIcon")
        XCTAssertEqual(padPrimary["CFBundleIconFiles"] as? [String], [
            "AppIcon20x20", "AppIcon29x29", "AppIcon40x40", "AppIcon76x76", "AppIcon83.5x83.5",
        ])
        let expectedDimensions = [
            "AppIcon20x20.png": 20, "AppIcon20x20@2x.png": 40, "AppIcon20x20@3x.png": 60,
            "AppIcon29x29.png": 29, "AppIcon29x29@2x.png": 58, "AppIcon29x29@3x.png": 87,
            "AppIcon40x40.png": 40, "AppIcon40x40@2x.png": 80, "AppIcon40x40@3x.png": 120,
            "AppIcon60x60@2x.png": 120, "AppIcon60x60@3x.png": 180,
            "AppIcon76x76.png": 76, "AppIcon76x76@2x.png": 152,
            "AppIcon83.5x83.5@2x.png": 167, "AppIcon1024x1024.png": 1024,
        ]
        XCTAssertEqual(Set(report.emittedFiles.map(\.relativePath)), Set(expectedDimensions.keys).union(["Assets.car"]))
        for emitted in report.emittedFiles where emitted.relativePath.hasSuffix(".png") {
            let data = try Data(contentsOf: fixture.app.appendingPathComponent(emitted.relativePath))
            let dimensions = try pngDimensions(data)
            XCTAssertEqual(dimensions.width, expectedDimensions[emitted.relativePath], emitted.relativePath)
            XCTAssertEqual(dimensions.height, expectedDimensions[emitted.relativePath], emitted.relativePath)
        }
        XCTAssertFalse(try fixture.hasTransactionStage())
    }

    func testCompilationFailurePreservesOriginalApp() async throws {
        let fixture = try Fixture()
        defer { fixture.cleanup() }
        try fixture.makeBrokenCatalog()
        try fixture.makeApp(plist: ["CFBundleIdentifier": "dev.pomeforge.unchanged", "Sentinel": "original"])
        let original = try Data(contentsOf: fixture.app.appendingPathComponent("Info.plist"))

        do {
            _ = try await PomeforgeAssetCompiler.compile(CompileOptions(catalogPath: fixture.catalog.path, appPath: fixture.app.path))
            XCTFail("compile unexpectedly succeeded")
        } catch {}

        XCTAssertEqual(try Data(contentsOf: fixture.app.appendingPathComponent("Info.plist")), original)
        XCTAssertFalse(FileManager.default.fileExists(atPath: fixture.app.appendingPathComponent("Assets.car").path))
    }

    func testRejectsSignedBundleAndOverlappingOrSymlinkInputs() async throws {
        let fixture = try Fixture()
        defer { fixture.cleanup() }
        try fixture.makeValidCatalog()
        try fixture.makeApp(plist: ["CFBundleIdentifier": "dev.pomeforge.signed"])
        try FileManager.default.createDirectory(at: fixture.app.appendingPathComponent("_CodeSignature"), withIntermediateDirectories: false)

        await XCTAssertThrowsErrorAsync {
            _ = try await PomeforgeAssetCompiler.compile(CompileOptions(catalogPath: fixture.catalog.path, appPath: fixture.app.path))
        }

        try FileManager.default.removeItem(at: fixture.app.appendingPathComponent("_CodeSignature"))
        let symlink = fixture.root.appendingPathComponent("Linked.app")
        try FileManager.default.createSymbolicLink(at: symlink, withDestinationURL: fixture.app)
        await XCTAssertThrowsErrorAsync {
            _ = try await PomeforgeAssetCompiler.compile(CompileOptions(catalogPath: fixture.catalog.path, appPath: symlink.path))
        }

        let nestedCatalog = fixture.app.appendingPathComponent("Nested.xcassets")
        try FileManager.default.copyItem(at: fixture.catalog, to: nestedCatalog)
        await XCTAssertThrowsErrorAsync {
            _ = try await PomeforgeAssetCompiler.compile(CompileOptions(catalogPath: nestedCatalog.path, appPath: fixture.app.path))
        }
    }

    func testRejectsSymlinkOutputDestination() async throws {
        let fixture = try Fixture()
        defer { fixture.cleanup() }
        try fixture.makeValidCatalog()
        try fixture.makeApp(plist: ["CFBundleIdentifier": "dev.pomeforge.safe"])
        let outside = fixture.root.appendingPathComponent("outside.car")
        try Data("outside".utf8).write(to: outside)
        try FileManager.default.createSymbolicLink(
            at: fixture.app.appendingPathComponent("Assets.car"),
            withDestinationURL: outside
        )

        await XCTAssertThrowsErrorAsync {
            _ = try await PomeforgeAssetCompiler.compile(CompileOptions(catalogPath: fixture.catalog.path, appPath: fixture.app.path))
        }
        XCTAssertEqual(try String(contentsOf: outside, encoding: .utf8), "outside")
    }

    func testRejectsNestedCatalogSymlinkAndTraversalBeforeMutation() async throws {
        for attack in ["symlink", "traversal"] {
            let fixture = try Fixture()
            defer { fixture.cleanup() }
            try fixture.makeValidCatalog()
            try fixture.makeApp(plist: ["CFBundleIdentifier": "dev.pomeforge.boundary", "Sentinel": attack])
            let original = try Data(contentsOf: fixture.app.appendingPathComponent("Info.plist"))
            let icon = fixture.catalog.appendingPathComponent("AppIcon.appiconset", isDirectory: true)
            let outside = fixture.root.appendingPathComponent("outside.png")
            try png.write(to: outside)
            if attack == "symlink" {
                try FileManager.default.removeItem(at: icon.appendingPathComponent("Icon.png"))
                try FileManager.default.createSymbolicLink(at: icon.appendingPathComponent("Icon.png"), withDestinationURL: outside)
            } else {
                try Data(#"{"images":[{"idiom":"iphone","size":"60x60","scale":"2x","filename":"../../outside.png"}],"info":{"author":"xcode","version":1}}"#.utf8)
                    .write(to: icon.appendingPathComponent("Contents.json"))
            }

            await XCTAssertThrowsErrorAsync {
                _ = try await PomeforgeAssetCompiler.compile(CompileOptions(catalogPath: fixture.catalog.path, appPath: fixture.app.path))
            }
            XCTAssertEqual(try Data(contentsOf: fixture.app.appendingPathComponent("Info.plist")), original)
            XCTAssertFalse(FileManager.default.fileExists(atPath: fixture.app.appendingPathComponent("Assets.car").path))
        }
    }

    func testRejectsConflictingDuplicateLooseIconBytesBeforeMutation() async throws {
        let fixture = try Fixture()
        defer { fixture.cleanup() }
        try fixture.makeCompleteIconCatalog(conflictingDuplicate: true)
        try fixture.makeApp(plist: ["CFBundleIdentifier": "dev.pomeforge.conflict", "Sentinel": "original"])
        let original = try Data(contentsOf: fixture.app.appendingPathComponent("Info.plist"))
        do {
            _ = try await PomeforgeAssetCompiler.compile(CompileOptions(catalogPath: fixture.catalog.path, appPath: fixture.app.path))
            XCTFail("compile unexpectedly succeeded")
        } catch {
            XCTAssertTrue(String(describing: error).contains("conflicting bytes"), String(describing: error))
        }
        XCTAssertEqual(try Data(contentsOf: fixture.app.appendingPathComponent("Info.plist")), original)
        XCTAssertFalse(FileManager.default.fileExists(atPath: fixture.app.appendingPathComponent("Assets.car").path))
    }

    func testRejectsDanglingSignedMarkerAndOutputSymlink() async throws {
        for path in ["_CodeSignature", "Assets.car"] {
            let fixture = try Fixture()
            defer { fixture.cleanup() }
            try fixture.makeValidCatalog()
            try fixture.makeApp(plist: ["CFBundleIdentifier": "dev.pomeforge.dangling", "Sentinel": path])
            let original = try Data(contentsOf: fixture.app.appendingPathComponent("Info.plist"))
            try FileManager.default.createSymbolicLink(
                at: fixture.app.appendingPathComponent(path),
                withDestinationURL: fixture.root.appendingPathComponent("missing-target")
            )
            await XCTAssertThrowsErrorAsync {
                _ = try await PomeforgeAssetCompiler.compile(CompileOptions(catalogPath: fixture.catalog.path, appPath: fixture.app.path))
            }
            XCTAssertEqual(try Data(contentsOf: fixture.app.appendingPathComponent("Info.plist")), original)
            XCTAssertEqual(try FileManager.default.destinationOfSymbolicLink(atPath: fixture.app.appendingPathComponent(path).path), fixture.root.appendingPathComponent("missing-target").path)
        }
    }

    func testRollbackFailureRetainsRecoveryBackupAndReportsDirectory() throws {
        let fixture = try Fixture()
        defer { fixture.cleanup() }
        try FileManager.default.createDirectory(at: fixture.app, withIntermediateDirectories: true)
        let first = fixture.app.appendingPathComponent("first")
        let second = fixture.app.appendingPathComponent("second")
        try Data("original-first".utf8).write(to: first)
        try Data("original-second".utf8).write(to: second)
        let outputs = [
            PreparedOutput(relativePath: "first", data: Data("new-first".utf8), reportable: true),
            PreparedOutput(relativePath: "second", data: Data("new-second".utf8), reportable: true),
        ]
        let operations = TransactionFileOperations(
            move: { source, destination in
                if source.lastPathComponent == "new-1" {
                    throw CocoaError(.fileWriteUnknown)
                }
                if source.lastPathComponent == "backup-0", destination.lastPathComponent == "first" {
                    throw CocoaError(.fileWriteNoPermission)
                }
                try FileManager.default.moveItem(at: source, to: destination)
            },
            remove: { try FileManager.default.removeItem(at: $0) }
        )
        var message = ""
        do {
            try applyTransaction(outputs, app: fixture.app, operations: operations)
            XCTFail("transaction unexpectedly succeeded")
        } catch {
            message = String(describing: error)
        }
        XCTAssertTrue(message.contains("recovery files retained at"), message)
        let stages = try FileManager.default.contentsOfDirectory(
            at: fixture.root,
            includingPropertiesForKeys: nil
        ).filter { $0.lastPathComponent.hasPrefix(".pomeforge-assets-stage-") }
        XCTAssertEqual(stages.count, 1)
        XCTAssertEqual(try Data(contentsOf: try XCTUnwrap(stages.first).appendingPathComponent("backup-0")), Data("original-first".utf8))
        XCTAssertEqual(try Data(contentsOf: second), Data("original-second".utf8))
    }
}

private struct Fixture {
    let root: URL
    let catalog: URL
    let app: URL

    init() throws {
        root = FileManager.default.temporaryDirectory.appendingPathComponent("Pomeforge Assets Tests & 100%-\(UUID().uuidString)", isDirectory: true)
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

    func makeCompleteIconCatalog(conflictingDuplicate: Bool = false) throws {
        try FileManager.default.createDirectory(at: catalog, withIntermediateDirectories: true)
        try Data(#"{"info":{"author":"xcode","version":1}}"#.utf8)
            .write(to: catalog.appendingPathComponent("Contents.json"))
        let icon = catalog.appendingPathComponent("AppIcon.appiconset", isDirectory: true)
        try FileManager.default.createDirectory(at: icon, withIntermediateDirectories: false)
        let specifications: [(String, String, String, Int)] = [
            ("iphone", "20x20", "2x", 40), ("iphone", "20x20", "3x", 60),
            ("iphone", "29x29", "2x", 58), ("iphone", "29x29", "3x", 87),
            ("iphone", "40x40", "2x", 80), ("iphone", "40x40", "3x", 120),
            ("iphone", "60x60", "2x", 120), ("iphone", "60x60", "3x", 180),
            ("ipad", "20x20", "1x", 20), ("ipad", "20x20", "2x", 40),
            ("ipad", "29x29", "1x", 29), ("ipad", "29x29", "2x", 58),
            ("ipad", "40x40", "1x", 40), ("ipad", "40x40", "2x", 80),
            ("ipad", "76x76", "1x", 76), ("ipad", "76x76", "2x", 152),
            ("ipad", "83.5x83.5", "2x", 167),
            ("ios-marketing", "1024x1024", "1x", 1024),
        ]
        var images: [[String: Any]] = []
        var written = Set<Int>()
        for (idiom, size, scale, pixels) in specifications {
            let isConflict = conflictingDuplicate && idiom == "ipad" && size == "20x20" && scale == "2x"
            let filename = isConflict ? "Icon-40-conflict.png" : "Icon-\(pixels).png"
            if isConflict {
                try makePNG(width: pixels, height: pixels, pixel: [0xd0, 0x31, 0x31, 0xff])
                    .write(to: icon.appendingPathComponent(filename))
            } else if written.insert(pixels).inserted {
                try makePNG(width: pixels, height: pixels).write(to: icon.appendingPathComponent(filename))
            }
            images.append(["idiom": idiom, "size": size, "scale": scale, "filename": filename])
        }
        let contents: [String: Any] = ["images": images, "info": ["author": "xcode", "version": 1]]
        try JSONSerialization.data(withJSONObject: contents, options: [.sortedKeys])
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

    func hasTransactionStage() throws -> Bool {
        try FileManager.default.contentsOfDirectory(at: root, includingPropertiesForKeys: nil)
            .contains { $0.lastPathComponent.hasPrefix(".pomeforge-assets-stage-") }
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

private func pngDimensions(_ data: Data) throws -> (width: Int, height: Int) {
    guard data.count >= 24, data.prefix(8) == Data([0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a]) else {
        throw CocoaError(.fileReadCorruptFile)
    }
    let bytes = [UInt8](data)
    let width = bytes[16..<20].reduce(0) { ($0 << 8) | Int($1) }
    let height = bytes[20..<24].reduce(0) { ($0 << 8) | Int($1) }
    return (width, height)
}

private func makePNG(width: Int, height: Int, pixel: [UInt8] = [0x35, 0x7a, 0xc8, 0xff]) -> Data {
    var raw: [UInt8] = []
    raw.reserveCapacity(height * (1 + width * 4))
    for _ in 0..<height {
        raw.append(0)
        for _ in 0..<width { raw.append(contentsOf: pixel) }
    }
    var compressed: [UInt8] = [0x78, 0x01]
    var offset = 0
    while offset < raw.count {
        let count = min(65_535, raw.count - offset)
        let final: UInt8 = offset + count == raw.count ? 1 : 0
        compressed.append(final)
        compressed.append(UInt8(count & 0xff))
        compressed.append(UInt8((count >> 8) & 0xff))
        let complement = 0xffff ^ count
        compressed.append(UInt8(complement & 0xff))
        compressed.append(UInt8((complement >> 8) & 0xff))
        compressed.append(contentsOf: raw[offset..<(offset + count)])
        offset += count
    }
    appendBigEndian(adler32(raw), to: &compressed)

    var result: [UInt8] = [0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a]
    var header: [UInt8] = []
    appendBigEndian(UInt32(width), to: &header)
    appendBigEndian(UInt32(height), to: &header)
    header.append(contentsOf: [8, 6, 0, 0, 0])
    appendPNGChunk("IHDR", data: header, to: &result)
    appendPNGChunk("IDAT", data: compressed, to: &result)
    appendPNGChunk("IEND", data: [], to: &result)
    return Data(result)
}

private func appendPNGChunk(_ name: String, data: [UInt8], to output: inout [UInt8]) {
    appendBigEndian(UInt32(data.count), to: &output)
    let type = Array(name.utf8)
    output.append(contentsOf: type)
    output.append(contentsOf: data)
    appendBigEndian(crc32(type + data), to: &output)
}

private func appendBigEndian(_ value: UInt32, to output: inout [UInt8]) {
    output.append(UInt8((value >> 24) & 0xff))
    output.append(UInt8((value >> 16) & 0xff))
    output.append(UInt8((value >> 8) & 0xff))
    output.append(UInt8(value & 0xff))
}

private func adler32(_ bytes: [UInt8]) -> UInt32 {
    var a: UInt32 = 1
    var b: UInt32 = 0
    for byte in bytes {
        a = (a + UInt32(byte)) % 65_521
        b = (b + a) % 65_521
    }
    return (b << 16) | a
}

private func crc32(_ bytes: [UInt8]) -> UInt32 {
    var crc: UInt32 = 0xffff_ffff
    for byte in bytes {
        crc ^= UInt32(byte)
        for _ in 0..<8 {
            crc = (crc & 1) == 1 ? (crc >> 1) ^ 0xedb8_8320 : crc >> 1
        }
    }
    return crc ^ 0xffff_ffff
}
