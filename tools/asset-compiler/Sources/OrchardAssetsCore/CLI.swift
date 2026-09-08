import Foundation

public struct CompileOptions: Equatable, Sendable {
    public var catalogPath: String
    public var appPath: String
    public var minimumIOS: String
    public var json: Bool

    public init(catalogPath: String, appPath: String, minimumIOS: String = "17.0", json: Bool = false) {
        self.catalogPath = catalogPath
        self.appPath = appPath
        self.minimumIOS = minimumIOS
        self.json = json
    }
}

public enum CLIAction: Equatable, Sendable {
    case help
    case compile(CompileOptions)
}

public enum CLIParser {
    public static func parse(_ arguments: [String]) throws -> CLIAction {
        if arguments.isEmpty || arguments == ["--help"] || arguments == ["-h"] {
            return .help
        }
        guard arguments[0] == "compile" else {
            throw AssetCompilerError.usage("unknown command \(quoted(arguments[0])); expected 'compile'")
        }
        if arguments.dropFirst().contains("--help") || arguments.dropFirst().contains("-h") {
            guard arguments.count == 2 else {
                throw AssetCompilerError.usage("--help cannot be combined with compile options")
            }
            return .help
        }

        var catalog: String?
        var app: String?
        var minimumIOS = "17.0"
        var emitJSON = false
        var index = 1
        while index < arguments.count {
            let argument = arguments[index]
            switch argument {
            case "--catalog", "--app", "--minimum-ios":
                guard index + 1 < arguments.count else {
                    throw AssetCompilerError.usage("missing value for \(argument)")
                }
                let value = arguments[index + 1]
                guard !value.isEmpty, !value.hasPrefix("--") else {
                    throw AssetCompilerError.usage("missing value for \(argument)")
                }
                switch argument {
                case "--catalog":
                    guard catalog == nil else { throw AssetCompilerError.usage("--catalog may be specified only once") }
                    catalog = value
                case "--app":
                    guard app == nil else { throw AssetCompilerError.usage("--app may be specified only once") }
                    app = value
                default:
                    minimumIOS = value
                }
                index += 2
            case "--json":
                guard !emitJSON else { throw AssetCompilerError.usage("--json may be specified only once") }
                emitJSON = true
                index += 1
            default:
                throw AssetCompilerError.usage("unknown option \(quoted(argument))")
            }
        }
        guard let catalog else { throw AssetCompilerError.usage("missing required --catalog PATH.xcassets") }
        guard let app else { throw AssetCompilerError.usage("missing required --app PATH.app") }
        guard isValidDeploymentTarget(minimumIOS) else {
            throw AssetCompilerError.usage("invalid --minimum-ios value \(quoted(minimumIOS)); expected a dotted numeric version such as 17.0")
        }
        return .compile(CompileOptions(catalogPath: catalog, appPath: app, minimumIOS: minimumIOS, json: emitJSON))
    }

    private static func isValidDeploymentTarget(_ value: String) -> Bool {
        let parts = value.split(separator: ".", omittingEmptySubsequences: false)
        guard (1...3).contains(parts.count), let major = Int(parts[0]), major > 0 else { return false }
        return parts.allSatisfy { part in
            !part.isEmpty && part.allSatisfy(\.isNumber) && Int(part) != nil
        }
    }

    private static func quoted(_ value: String) -> String { "'\(value)'" }
}

public let usage = """
Usage:
  orchard-assets compile --catalog PATH.xcassets --app PATH.app [--minimum-ios 17.0] [--json]

Compiles a PNG/JPEG/SVG/color asset catalog with AssetKit, writes Assets.car,
merges app-icon keys into the existing Info.plist, and emits loose icon PNGs.
Run this after xtool creates the unsigned .app and before zsign signs it.
"""

public enum AssetCompilerError: Error, CustomStringConvertible, Sendable {
    case usage(String)
    case invalidInput(String)
    case signedBundle(String)
    case unsafeOutput(String)
    case transaction(String)

    public var description: String {
        switch self {
        case .usage(let message): return message
        case .invalidInput(let message): return message
        case .signedBundle(let message): return message
        case .unsafeOutput(let message): return message
        case .transaction(let message): return message
        }
    }
}
