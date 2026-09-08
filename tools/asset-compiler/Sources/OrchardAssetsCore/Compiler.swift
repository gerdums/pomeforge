import AssetKit
import Foundation

public struct EmittedFile: Codable, Equatable, Sendable {
    public var relativePath: String
    public var bytes: Int
    public var sha256: String
}

public struct CompileReport: Codable, Equatable, Sendable {
    public var success: Bool
    public var catalog: String
    public var app: String
    public var minimumIOS: String
    public var emittedFiles: [EmittedFile]
}

private struct PreparedOutput: Sendable {
    var relativePath: String
    var data: Data
    var reportable: Bool
}

public enum OrchardAssetCompiler {
    public static func compile(_ options: CompileOptions) async throws -> CompileReport {
        let context = try validateInputs(options)

        // AssetKit compiles before any caller-owned output is modified.
        let result = try await XCAssetCompiler(deploymentTarget: options.minimumIOS)
            .compile(catalog: context.catalog)

        var plist = try readPropertyList(context.infoPlist)
        var outputs = [PreparedOutput(relativePath: "Assets.car", data: result.carData, reportable: true)]

        if let icons = result.appIconBundle {
            for (key, value) in icons.infoPlistAdditions {
                plist[key] = value
            }
            var seen = Set(["Assets.car", "Info.plist"])
            for looseFile in icons.looseFiles {
                try validateLooseFilename(looseFile.name)
                guard seen.insert(looseFile.name).inserted else {
                    throw AssetCompilerError.unsafeOutput("duplicate output filename '\(looseFile.name)'")
                }
                outputs.append(PreparedOutput(relativePath: looseFile.name, data: looseFile.data, reportable: true))
            }
        }

        let plistData: Data
        do {
            plistData = try PropertyListSerialization.data(
                fromPropertyList: plist,
                format: context.plistFormat,
                options: 0
            )
        } catch {
            throw AssetCompilerError.invalidInput("serialize merged Info.plist: \(error)")
        }
        outputs.append(PreparedOutput(relativePath: "Info.plist", data: plistData, reportable: false))

        try preflightDestinations(outputs, app: context.app)
        try applyTransaction(outputs, app: context.app)

        let emitted = outputs.filter(\.reportable).map {
            EmittedFile(relativePath: $0.relativePath, bytes: $0.data.count, sha256: SHA256.hexDigest($0.data))
        }.sorted { $0.relativePath < $1.relativePath }
        return CompileReport(
            success: true,
            catalog: context.catalog.path,
            app: context.app.path,
            minimumIOS: options.minimumIOS,
            emittedFiles: emitted
        )
    }
}

private struct InputContext {
    var catalog: URL
    var app: URL
    var infoPlist: URL
    var plistFormat: PropertyListSerialization.PropertyListFormat
}

private func validateInputs(_ options: CompileOptions) throws -> InputContext {
    let fileManager = FileManager.default
    let suppliedCatalog = URL(fileURLWithPath: options.catalogPath)
    let suppliedApp = URL(fileURLWithPath: options.appPath)
    try rejectDirectSymlink(suppliedCatalog, label: "catalog")
    try rejectDirectSymlink(suppliedApp, label: "app bundle")

    let catalog = suppliedCatalog.standardizedFileURL.resolvingSymlinksInPath()
    let app = suppliedApp.standardizedFileURL.resolvingSymlinksInPath()
    var isDirectory: ObjCBool = false
    guard fileManager.fileExists(atPath: catalog.path, isDirectory: &isDirectory), isDirectory.boolValue else {
        throw AssetCompilerError.invalidInput("asset catalog is not a directory: \(catalog.path)")
    }
    isDirectory = false
    guard fileManager.fileExists(atPath: app.path, isDirectory: &isDirectory), isDirectory.boolValue else {
        throw AssetCompilerError.invalidInput("app bundle is not a directory: \(app.path)")
    }
    guard !containsPath(catalog, app), !containsPath(app, catalog) else {
        throw AssetCompilerError.invalidInput("asset catalog and app bundle must not overlap")
    }

    for marker in ["_CodeSignature", "embedded.mobileprovision", "CodeResources"] {
        if fileManager.fileExists(atPath: app.appendingPathComponent(marker).path) {
            throw AssetCompilerError.signedBundle("refusing signed app bundle: found \(marker); compile assets before signing")
        }
    }

    let infoPlist = app.appendingPathComponent("Info.plist", isDirectory: false)
    try rejectDirectSymlink(infoPlist, label: "Info.plist")
    guard fileManager.fileExists(atPath: infoPlist.path) else {
        throw AssetCompilerError.invalidInput("app bundle has no Info.plist")
    }
    let data: Data
    do { data = try Data(contentsOf: infoPlist) }
    catch { throw AssetCompilerError.invalidInput("read Info.plist: \(error)") }
    var format = PropertyListSerialization.PropertyListFormat.xml
    do {
        let object = try PropertyListSerialization.propertyList(from: data, options: [], format: &format)
        guard object is [String: Any] else {
            throw AssetCompilerError.invalidInput("Info.plist root must be a dictionary")
        }
    } catch let error as AssetCompilerError { throw error }
    catch { throw AssetCompilerError.invalidInput("parse Info.plist: \(error)") }
    return InputContext(catalog: catalog, app: app, infoPlist: infoPlist, plistFormat: format)
}

private func readPropertyList(_ url: URL) throws -> [String: Any] {
    do {
        let data = try Data(contentsOf: url)
        let object = try PropertyListSerialization.propertyList(from: data, options: [], format: nil)
        guard let dictionary = object as? [String: Any] else {
            throw AssetCompilerError.invalidInput("Info.plist root must be a dictionary")
        }
        return dictionary
    } catch let error as AssetCompilerError { throw error }
    catch { throw AssetCompilerError.invalidInput("parse Info.plist: \(error)") }
}

private func containsPath(_ parent: URL, _ candidate: URL) -> Bool {
    let parentComponents = parent.pathComponents
    let candidateComponents = candidate.pathComponents
    return candidateComponents.count >= parentComponents.count
        && Array(candidateComponents.prefix(parentComponents.count)) == parentComponents
}

private func rejectDirectSymlink(_ url: URL, label: String) throws {
    guard FileManager.default.fileExists(atPath: url.path) else { return }
    do {
        let values = try url.resourceValues(forKeys: [.isSymbolicLinkKey])
        if values.isSymbolicLink == true {
            throw AssetCompilerError.invalidInput("\(label) must not be a symbolic link: \(url.path)")
        }
    } catch let error as AssetCompilerError { throw error }
    catch { throw AssetCompilerError.invalidInput("inspect \(label): \(error)") }
}

private func validateLooseFilename(_ name: String) throws {
    guard !name.isEmpty,
          name != ".", name != "..",
          !name.contains("/"), !name.contains("\\"), !name.contains("\0"),
          (name as NSString).lastPathComponent == name
    else {
        throw AssetCompilerError.unsafeOutput("unsafe loose asset filename '\(name)'")
    }
}

private func preflightDestinations(_ outputs: [PreparedOutput], app: URL) throws {
    let fileManager = FileManager.default
    let canonicalApp = app.resolvingSymlinksInPath()
    for output in outputs {
        try validateLooseFilename(output.relativePath)
        let destination = app.appendingPathComponent(output.relativePath, isDirectory: false)
        let parent = destination.deletingLastPathComponent().resolvingSymlinksInPath()
        guard parent == canonicalApp else {
            throw AssetCompilerError.unsafeOutput("output escapes app bundle: \(output.relativePath)")
        }
        if fileManager.fileExists(atPath: destination.path) {
            let values: URLResourceValues
            do { values = try destination.resourceValues(forKeys: [.isSymbolicLinkKey, .isRegularFileKey]) }
            catch { throw AssetCompilerError.unsafeOutput("inspect output \(output.relativePath): \(error)") }
            guard values.isSymbolicLink != true, values.isRegularFile == true else {
                throw AssetCompilerError.unsafeOutput("output destination is not a regular file: \(output.relativePath)")
            }
        }
    }
}

private func applyTransaction(_ outputs: [PreparedOutput], app: URL) throws {
    let fileManager = FileManager.default
    let stage = app.deletingLastPathComponent()
        .appendingPathComponent(".orchard-assets-stage-\(UUID().uuidString)", isDirectory: true)
    do {
        try fileManager.createDirectory(at: stage, withIntermediateDirectories: false, attributes: [.posixPermissions: 0o700])
    } catch {
        throw AssetCompilerError.transaction("create private output stage: \(error)")
    }
    var preserveStageForRecovery = false
    defer {
        if !preserveStageForRecovery {
            try? fileManager.removeItem(at: stage)
        }
    }

    struct Item {
        var destination: URL
        var staged: URL
        var backup: URL
        var hadOriginal: Bool
    }
    var items: [Item] = []
    do {
        for (index, output) in outputs.enumerated() {
            let staged = stage.appendingPathComponent("new-\(index)")
            try output.data.write(to: staged, options: [.atomic])
            items.append(Item(
                destination: app.appendingPathComponent(output.relativePath),
                staged: staged,
                backup: stage.appendingPathComponent("backup-\(index)"),
                hadOriginal: fileManager.fileExists(atPath: app.appendingPathComponent(output.relativePath).path)
            ))
        }
    } catch {
        throw AssetCompilerError.transaction("stage compiled outputs: \(error)")
    }

    var committed = 0
    do {
        for index in items.indices {
            if items[index].hadOriginal {
                try fileManager.moveItem(at: items[index].destination, to: items[index].backup)
            }
            do {
                try fileManager.moveItem(at: items[index].staged, to: items[index].destination)
            } catch {
                if items[index].hadOriginal {
                    do {
                        try fileManager.moveItem(at: items[index].backup, to: items[index].destination)
                    } catch let restoreError {
                        preserveStageForRecovery = true
                        throw AssetCompilerError.transaction(
                            "commit compiled output failed and its original remains in \(items[index].backup.path): \(restoreError)"
                        )
                    }
                }
                throw error
            }
            committed += 1
        }
    } catch {
        var rollbackErrors: [String] = []
        if committed > 0 {
            for index in stride(from: committed - 1, through: 0, by: -1) {
                do {
                    if fileManager.fileExists(atPath: items[index].destination.path) {
                        try fileManager.removeItem(at: items[index].destination)
                    }
                    if items[index].hadOriginal {
                        try fileManager.moveItem(at: items[index].backup, to: items[index].destination)
                    }
                } catch {
                    rollbackErrors.append(error.localizedDescription)
                }
            }
        }
        if !rollbackErrors.isEmpty {
            preserveStageForRecovery = true
        }
        let suffix = rollbackErrors.isEmpty ? "" : "; rollback errors: \(rollbackErrors.joined(separator: "; "))"
        throw AssetCompilerError.transaction("commit compiled outputs: \(error)\(suffix)")
    }
}
