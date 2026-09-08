import Foundation
#if os(Linux)
import Glibc
#else
import Darwin
#endif
import OrchardAssetsCore

@main
struct OrchardAssetsCommand {
    static func main() async {
        do {
            switch try CLIParser.parse(Array(CommandLine.arguments.dropFirst())) {
            case .help:
                print(usage)
            case .compile(let options):
                let report = try await OrchardAssetCompiler.compile(options)
                if options.json {
                    let encoder = JSONEncoder()
                    encoder.outputFormatting = [.prettyPrinted, .sortedKeys, .withoutEscapingSlashes]
                    let data = try encoder.encode(report)
                    FileHandle.standardOutput.write(data)
                    FileHandle.standardOutput.write(Data([0x0a]))
                } else {
                    print("Compiled \(report.emittedFiles.count) asset file(s) into \(report.app)")
                }
            }
        } catch {
            let message = error as? AssetCompilerError
            FileHandle.standardError.write(Data("error: \(message?.description ?? error.localizedDescription)\n".utf8))
            if let message, case .usage = message {
                FileHandle.standardError.write(Data("\n\(usage)\n".utf8))
            }
            exit(2)
        }
    }
}
