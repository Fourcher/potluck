import AppKit
import CryptoKit
import Foundation
import Observation

/// Updates Potluck from its releases: finds a newer version, downloads the Mac app, checks it
/// against the release's checksums, swaps it in and starts the new one.
@MainActor @Observable
final class Updater {
    var available: String?   // the newer version, when there is one
    var busy = false
    var note = ""

    private let api = URL(string: "https://api.github.com/repos/Fourcher/potluck/releases/latest")!
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
            let got = SHA256.hash(data: try Data(contentsOf: zip)).map { String(format: "%02x", $0) }.joined()
            guard !want.isEmpty, want == got else { note = "The download did not match its checksum, so it was not installed."; return }
            let work = FileManager.default.temporaryDirectory.appendingPathComponent("potluck-update-\(UUID().uuidString)")
            try FileManager.default.createDirectory(at: work, withIntermediateDirectories: true)
            let unzip = Process()
            unzip.executableURL = URL(fileURLWithPath: "/usr/bin/ditto")
            unzip.arguments = ["-x", "-k", zip.path, work.path]
            try unzip.run(); unzip.waitUntilExit()
            guard let newApp = try FileManager.default.contentsOfDirectory(at: work, includingPropertiesForKeys: nil).first(where: { $0.pathExtension == "app" }) else {
                note = "The download has no app in it."
                return
            }
            // Swap the app on disk, then start the new one once this one has quit.
            let here = Bundle.main.bundleURL
            let old = here.deletingLastPathComponent().appendingPathComponent(".Potluck-old.app")
            try? FileManager.default.removeItem(at: old)
            try FileManager.default.moveItem(at: here, to: old)
            try FileManager.default.moveItem(at: newApp, to: here)
            try? FileManager.default.removeItem(at: old)
            try? FileManager.default.removeItem(at: work)
            let relaunch = Process()
            relaunch.executableURL = URL(fileURLWithPath: "/bin/sh")
            relaunch.arguments = ["-c", "sleep 1; /usr/bin/open \"\(here.path)\""]
            try relaunch.run()
            NSApp.terminate(nil)
        } catch {
            note = "The update did not install: \(error.localizedDescription)"
        }
    }
}
