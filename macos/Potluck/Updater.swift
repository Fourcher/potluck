import AppKit
import CryptoKit
import Foundation
import Observation
import Security

/// Updates Potluck from its releases: finds a newer version, downloads the Mac app, checks it
/// against the release's checksums, swaps it in and starts the new one.
@MainActor @Observable
final class Updater {
    var available: String?   // the newer version, when there is one
    var busy = false
    var note = ""

    private let api = URL(string: "https://api.github.com/repos/Fourcher/potluck/releases/latest")!
    /// Potluck's releases are signed by this Apple developer team.
    private static let releaseTeam = "JMC2566X2S"
    /// Run before the new version starts: stops the running Potluck program, so the new app
    /// starts its own instead of finding the old one still answering.
    var beforeRestart: (() async -> Void)?
    private var asset: URL?
    private var sums: URL?

    var current: String { Bundle.main.object(forInfoDictionaryKey: "CFBundleShortVersionString") as? String ?? "0" }

    func checkSoon() {
        Task {
            try? await Task.sleep(for: .seconds(10))
            await check(force: false)
            // Then once a day while the app runs.
            while true {
                try? await Task.sleep(for: .seconds(24 * 3600))
                await check(force: false)
            }
        }
    }

    func check(force: Bool) async {
        busy = true
        defer { busy = false }
        var r = URLRequest(url: api)
        r.setValue("potluck-app", forHTTPHeaderField: "User-Agent")
        r.timeoutInterval = 20
        guard let (d, resp) = try? await URLSession.shared.data(for: r), (resp as? HTTPURLResponse)?.statusCode == 200,
              let j = try? JSONSerialization.jsonObject(with: d) as? [String: Any] else {
            if force { note = "Could not reach Potluck's releases just now." }
            return
        }
        let tag = (j["tag_name"] as? String ?? "").trimmingCharacters(in: CharacterSet(charactersIn: "v"))
        let assets = j["assets"] as? [[String: Any]] ?? []
        asset = assets.first { ($0["name"] as? String ?? "").hasSuffix("-mac.zip") }.flatMap { URL(string: $0["browser_download_url"] as? String ?? "") }
        sums = assets.first { ($0["name"] as? String) == "SHA256SUMS.txt" }.flatMap { URL(string: $0["browser_download_url"] as? String ?? "") }
        if newer(tag, than: current), asset != nil {
            available = tag
            note = "Potluck \(tag) is available."
        } else {
            available = nil
            if force { note = "You have the newest Potluck (\(current))." }
        }
    }

    private func newer(_ a: String, than b: String) -> Bool {
        let x = a.split(separator: ".").map { Int($0.prefix { $0.isNumber }) ?? 0 }
        let y = b.split(separator: ".").map { Int($0.prefix { $0.isNumber }) ?? 0 }
        for i in 0..<max(x.count, y.count) {
            let p = i < x.count ? x[i] : 0, q = i < y.count ? y[i] : 0
            if p != q { return p > q }
        }
        return false
    }

    /// Downloads, verifies and installs the new version, then restarts into it.
    func install() async {
        guard let asset, let sums else { return }
        busy = true
        defer { busy = false }
        do {
            let (zip, _) = try await URLSession.shared.download(from: asset)
            let (sumData, _) = try await URLSession.shared.data(from: sums)
            let want = String(decoding: sumData, as: UTF8.self).split(separator: "\n")
                .first { $0.hasSuffix(asset.lastPathComponent) }?.split(separator: " ").first.map(String.init) ?? ""
            // Heavy work off the main thread, so the app keeps answering meanwhile.
            let got = try await Task.detached { SHA256.hash(data: try Data(contentsOf: zip)).map { String(format: "%02x", $0) }.joined() }.value
            guard !want.isEmpty, want == got else { note = "The download did not match its checksum, so it was not installed."; return }
            let work = FileManager.default.temporaryDirectory.appendingPathComponent("potluck-update-\(UUID().uuidString)")
            defer { try? FileManager.default.removeItem(at: work) }
            try FileManager.default.createDirectory(at: work, withIntermediateDirectories: true)
            try await Self.run("/usr/bin/ditto", ["-x", "-k", zip.path, work.path])
            guard let newApp = try FileManager.default.contentsOfDirectory(at: work, includingPropertiesForKeys: nil).first(where: { $0.pathExtension == "app" }) else {
                note = "The download has no app in it."
                return
            }
            // The checksum comes from the same release, so it shows the file arrived whole, not
            // who made it. The new app must carry a valid signature from the same developer as
            // this one (or Potluck's own, if this copy is unsigned).
            let team = Self.teamID(Bundle.main.bundleURL) ?? Self.releaseTeam
            guard let newTeam = Self.teamID(newApp), newTeam == team else {
                note = "The new version is not signed by Potluck's developer, so it was not installed."
                return
            }
            // Swap the app on disk, putting the old one back if the swap fails halfway.
            let here = Bundle.main.bundleURL
            let old = here.deletingLastPathComponent().appendingPathComponent(".Potluck-old.app")
            try? FileManager.default.removeItem(at: old)
            try FileManager.default.moveItem(at: here, to: old)
            do {
                try FileManager.default.moveItem(at: newApp, to: here)
            } catch {
                try? FileManager.default.moveItem(at: old, to: here)
                throw error
            }
            try? FileManager.default.removeItem(at: old)
            // Stop the running Potluck program, then start the new app once this one has quit.
            await beforeRestart?()
            let relaunch = Process()
            relaunch.executableURL = URL(fileURLWithPath: "/bin/sh")
            relaunch.arguments = ["-c", "sleep 1; /usr/bin/open \"$0\"", here.path]
            try relaunch.run()
            NSApp.terminate(nil)
        } catch {
            note = "The update did not install: \(error.localizedDescription)"
        }
    }

    /// The Apple developer team that signed an app, when its signature is valid (every part
    /// of it, for every processor); nil when it is unsigned, altered or signed by no team.
    nonisolated static func teamID(_ url: URL) -> String? {
        var code: SecStaticCode?
        guard SecStaticCodeCreateWithPath(url as CFURL, [], &code) == errSecSuccess, let code else { return nil }
        let strict = SecCSFlags(rawValue: kSecCSCheckAllArchitectures | kSecCSStrictValidate | kSecCSCheckNestedCode)
        guard SecStaticCodeCheckValidity(code, strict, nil) == errSecSuccess else { return nil }
        var info: CFDictionary?
        guard SecCodeCopySigningInformation(code, SecCSFlags(rawValue: kSecCSSigningInformation), &info) == errSecSuccess,
              let d = info as? [String: Any] else { return nil }
        return d[kSecCodeInfoTeamIdentifier as String] as? String
    }

    /// Runs a program and waits for it without holding up the main thread.
    nonisolated static func run(_ path: String, _ args: [String]) async throws {
        let p = Process()
        p.executableURL = URL(fileURLWithPath: path)
        p.arguments = args
        try await withCheckedThrowingContinuation { (c: CheckedContinuation<Void, Error>) in
            p.terminationHandler = { p in
                if p.terminationStatus == 0 { c.resume() } else { c.resume(throwing: NSError(domain: "Potluck", code: Int(p.terminationStatus), userInfo: [NSLocalizedDescriptionKey: "\(path) ended with \(p.terminationStatus)"])) }
            }
            do { try p.run() } catch { c.resume(throwing: error) }
        }
    }
}
