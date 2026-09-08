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

struct PreparedOutput: Sendable {
    var relativePath: String
    var data: Data
    var reportable: Bool
}

public enum PomeforgeAssetCompiler {
    public static func compile(_ options: CompileOptions) async throws -> CompileReport {
        #if !os(Linux)
        throw AssetCompilerError.invalidInput("pomeforge-assets compile is supported only on Linux")
        #endif
        let context = try validateInputs(options)
        try preflightCatalog(context.catalog)

        // AssetKit compiles before any caller-owned output is modified.
        let result = try await XCAssetCompiler(deploymentTarget: options.minimumIOS)
            .compile(catalog: context.catalog)

        var plist = try readPropertyList(context.infoPlist)
        var outputs = [PreparedOutput(relativePath: "Assets.car", data: result.carData, reportable: true)]

        if let icons = result.appIconBundle {
            for (key, value) in icons.infoPlistAdditions {
                plist[key] = value
            }
            let reserved = Set(["Assets.car", "Info.plist"])
            var looseOutputs: [String: Data] = [:]
            for looseFile in icons.looseFiles {
                try validateLooseFilename(looseFile.name)
                guard !reserved.contains(looseFile.name) else {
                    throw AssetCompilerError.unsafeOutput("loose asset collides with reserved output '\(looseFile.name)'")
                }
                if let existing = looseOutputs[looseFile.name] {
                    guard existing == looseFile.data else {
                        throw AssetCompilerError.unsafeOutput(
                            "duplicate loose output '\(looseFile.name)' has conflicting bytes; distinct idiom images with the same AssetKit loose name are unsupported"
                        )
                    }
                    continue
                }
                looseOutputs[looseFile.name] = looseFile.data
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
    let suppliedCatalog = URL(fileURLWithPath: options.catalogPath)
    let suppliedApp = URL(fileURLWithPath: options.appPath)
    try rejectDirectSymlink(suppliedCatalog, label: "catalog")
    try rejectDirectSymlink(suppliedApp, label: "app bundle")

    let catalog = suppliedCatalog.standardizedFileURL.resolvingSymlinksInPath()
    let app = suppliedApp.standardizedFileURL.resolvingSymlinksInPath()
    guard try lexicalPathKind(catalog) == .directory else {
        throw AssetCompilerError.invalidInput("asset catalog is not a directory: \(catalog.path)")
    }
    guard try lexicalPathKind(app) == .directory else {
        throw AssetCompilerError.invalidInput("app bundle is not a directory: \(app.path)")
    }
    guard !containsPath(catalog, app), !containsPath(app, catalog) else {
        throw AssetCompilerError.invalidInput("asset catalog and app bundle must not overlap")
    }

    for marker in ["_CodeSignature", "embedded.mobileprovision", "CodeResources"] {
        if try lexicalPathKind(app.appendingPathComponent(marker)) != .absent {
            throw AssetCompilerError.signedBundle("refusing signed app bundle: found \(marker); compile assets before signing")
        }
    }

    let infoPlist = app.appendingPathComponent("Info.plist", isDirectory: false)
    try rejectDirectSymlink(infoPlist, label: "Info.plist")
    guard try lexicalPathKind(infoPlist) == .regular else {
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
    if try lexicalPathKind(url) == .symlink {
        throw AssetCompilerError.invalidInput("\(label) must not be a symbolic link: \(url.path)")
    }
}

private enum LexicalPathKind: Equatable {
    case absent
    case directory
    case regular
    case symlink
    case special
}

private func lexicalPathKind(_ url: URL) throws -> LexicalPathKind {
    do {
        let attributes = try FileManager.default.attributesOfItem(atPath: url.path)
        guard let type = attributes[.type] as? FileAttributeType else { return .special }
        switch type {
        case .typeDirectory: return .directory
        case .typeRegular: return .regular
        case .typeSymbolicLink: return .symlink
        default: return .special
        }
    } catch {
        let nsError = error as NSError
        if nsError.domain == NSCocoaErrorDomain && nsError.code == NSFileReadNoSuchFileError {
            return .absent
        }
        throw AssetCompilerError.invalidInput("inspect path \(url.path): \(error)")
    }
}

private func preflightCatalog(_ catalog: URL) throws {
    let fileManager = FileManager.default
    var enumerationError: Error?
    guard let enumerator = fileManager.enumerator(
        at: catalog,
        includingPropertiesForKeys: nil,
        options: [],
        errorHandler: { _, error in enumerationError = error; return false }
    ) else {
        throw AssetCompilerError.invalidInput("enumerate asset catalog: \(catalog.path)")
    }
    var contentsFiles: [URL] = []
    while let candidate = enumerator.nextObject() as? URL {
        if let enumerationError {
            throw AssetCompilerError.invalidInput("enumerate asset catalog: \(enumerationError)")
        }
        guard containsPath(catalog, candidate.standardizedFileURL) else {
            throw AssetCompilerError.invalidInput("asset catalog entry escapes catalog: \(candidate.path)")
        }
        switch try lexicalPathKind(candidate) {
        case .directory:
            break
        case .regular:
            if candidate.lastPathComponent == "Contents.json" { contentsFiles.append(candidate) }
        case .symlink:
            throw AssetCompilerError.invalidInput("asset catalog must not contain symbolic links: \(candidate.path)")
        case .special:
            throw AssetCompilerError.invalidInput("asset catalog must contain only directories and regular files: \(candidate.path)")
        case .absent:
            throw AssetCompilerError.invalidInput("asset catalog entry disappeared during validation: \(candidate.path)")
        }
    }
    if let enumerationError {
        throw AssetCompilerError.invalidInput("enumerate asset catalog: \(enumerationError)")
    }
    for contents in contentsFiles {
        try validateCatalogReferences(contents, catalog: catalog)
    }
}

private func validateCatalogReferences(_ contents: URL, catalog: URL) throws {
    let object: Any
    do {
        object = try JSONSerialization.jsonObject(with: Data(contentsOf: contents))
    } catch {
        throw AssetCompilerError.invalidInput("parse \(contents.path): \(error)")
    }
    guard let dictionary = object as? [String: Any] else {
        throw AssetCompilerError.invalidInput("Contents.json root must be an object: \(contents.path)")
    }
    let assetDirectory = contents.deletingLastPathComponent().standardizedFileURL
    for key in ["images", "data"] {
        guard let rawEntries = dictionary[key] else { continue }
        guard let entries = rawEntries as? [[String: Any]] else {
            throw AssetCompilerError.invalidInput("\(key) must be an array of objects in \(contents.path)")
        }
        for entry in entries {
            guard let rawFilename = entry["filename"] else { continue }
            guard let filename = rawFilename as? String, !filename.isEmpty, !filename.contains("\0") else {
                throw AssetCompilerError.invalidInput("invalid \(key) filename in \(contents.path)")
            }
            let target = assetDirectory.appendingPathComponent(filename).standardizedFileURL
            guard containsPath(assetDirectory, target), containsPath(catalog, target) else {
                throw AssetCompilerError.invalidInput("asset filename escapes its asset directory: \(filename)")
            }
            guard try lexicalPathKind(target) == .regular else {
                throw AssetCompilerError.invalidInput("asset filename is not a regular file: \(filename)")
            }
        }
    }
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
    let canonicalApp = app.resolvingSymlinksInPath()
    for output in outputs {
        try validateLooseFilename(output.relativePath)
        let destination = app.appendingPathComponent(output.relativePath, isDirectory: false)
        let parent = destination.deletingLastPathComponent().resolvingSymlinksInPath()
        guard parent == canonicalApp else {
            throw AssetCompilerError.unsafeOutput("output escapes app bundle: \(output.relativePath)")
        }
        let kind = try lexicalPathKind(destination)
        if kind != .absent && kind != .regular {
            throw AssetCompilerError.unsafeOutput("output destination is not a regular file: \(output.relativePath)")
        }
    }
}

struct TransactionFileOperations: Sendable {
    var move: @Sendable (URL, URL) throws -> Void
    var remove: @Sendable (URL) throws -> Void

    static let live = TransactionFileOperations(
        move: { try FileManager.default.moveItem(at: $0, to: $1) },
        remove: { try FileManager.default.removeItem(at: $0) }
    )
}

func applyTransaction(
    _ outputs: [PreparedOutput],
    app: URL,
    operations: TransactionFileOperations = .live
) throws {
    let fileManager = FileManager.default
    let stage = app.deletingLastPathComponent()
        .appendingPathComponent(".pomeforge-assets-stage-\(UUID().uuidString)", isDirectory: true)
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
                hadOriginal: try lexicalPathKind(app.appendingPathComponent(output.relativePath)) != .absent
            ))
        }
    } catch {
        throw AssetCompilerError.transaction("stage compiled outputs: \(error)")
    }

    var committed = 0
    do {
        for index in items.indices {
            if items[index].hadOriginal {
                try operations.move(items[index].destination, items[index].backup)
            }
            do {
                try operations.move(items[index].staged, items[index].destination)
            } catch {
                if items[index].hadOriginal {
                    do {
                        try operations.move(items[index].backup, items[index].destination)
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
                    if try lexicalPathKind(items[index].destination) != .absent {
                        try operations.remove(items[index].destination)
                    }
                    if items[index].hadOriginal {
                        try operations.move(items[index].backup, items[index].destination)
                    }
                } catch {
                    rollbackErrors.append(error.localizedDescription)
                }
            }
        }
        if !rollbackErrors.isEmpty {
            preserveStageForRecovery = true
        }
        let suffix = rollbackErrors.isEmpty
            ? ""
            : "; recovery files retained at \(stage.path); rollback errors: \(rollbackErrors.joined(separator: "; "))"
        throw AssetCompilerError.transaction("commit compiled outputs: \(error)\(suffix)")
    }
}
