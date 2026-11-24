import AppKit
import Foundation
import Observation

/// Runs Potluck itself (the program inside this app) and keeps an eye on it: what is running,
/// which computers are connected, and which tasks are waiting for the person.
@MainActor @Observable
final class PotluckEngine {
    // What the menu bar shows.
    var running = false            // Potluck answers on this computer
    var phase = "idle"             // the main model: idle | starting | running | resting | stopping | error
    var modelTitle = ""
    var stepText = ""
    var errorText = ""
    var tps: Double = 0
    var computers: [(name: String, online: Bool, role: String)] = []
    var waiting: [Question] = []   // questions tasks are waiting on
    var version = ""
    var problem = ""               // why Potluck could not be started, if it could not
    var role = ""                  // "" until the person chooses: single | main | helper
    var hasModel = false           // a model has been chosen at least once
    var linkPhase = ""             // helper: searching | pairing | connected
    var linkNote = ""
    var mainName = ""
    var pairCode = ""
    var checked = false            // the first answer from Potluck has arrived

    struct Question: Decodable, Identifiable, Equatable {
        let id: String
        let session: String
        let title: String
        let detail: String?
        let kind: String
    }

    static let panelURL = URL(string: "http://127.0.0.1:7716/")!
    static let chatURL = URL(string: "http://127.0.0.1:7720/")!

    private var process: Process?
    private var timer: Timer?
    var onNewQuestion: ((Question) -> Void)?
    private var seenQuestions = Set<String>()

    /// Where Potluck keeps everything (models, settings, logs). Can be changed in Settings.
    var dataDir: String {
        get { UserDefaults.standard.string(forKey: "dataDir") ?? (NSHomeDirectory() as NSString).appendingPathComponent("Potluck") }
        set { UserDefaults.standard.set(newValue, forKey: "dataDir") }
    }

    var binary: URL? { Bundle.main.url(forResource: "potluck", withExtension: nil) }

    func begin() {
        Task { await ensureRunning() }
        timer = Timer.scheduledTimer(withTimeInterval: 2, repeats: true) { [weak self] _ in
            Task { @MainActor in await self?.refresh() }
        }
    }

    /// Starts Potluck if nothing answers on this computer yet.
    func ensureRunning() async {
        if await ping() { await refresh(); return }
        guard let bin = binary else { problem = "The Potluck program is missing from this app. Download it again."; return }
        let p = Process()
        p.executableURL = bin
        p.arguments = ["--data", dataDir, "--no-browser"] // the role comes from the welcome window, then from its settings
        let logURL = URL(fileURLWithPath: dataDir).appendingPathComponent("logs/potluck-app.log")
        try? FileManager.default.createDirectory(at: logURL.deletingLastPathComponent(), withIntermediateDirectories: true)
        FileManager.default.createFile(atPath: logURL.path, contents: nil)
        if let h = try? FileHandle(forWritingTo: logURL) { p.standardOutput = h; p.standardError = h }
        p.terminationHandler = { [weak self] _ in Task { @MainActor in self?.process = nil } }
        do {
            try p.run()
            process = p
            problem = ""
        } catch {
            problem = "Potluck could not start: \(error.localizedDescription)"
            return
        }
        for _ in 0..<40 {
            try? await Task.sleep(for: .milliseconds(250))
            if await ping() { break }
        }
        await refresh()
    }

    /// Stops Potluck on this computer (the models on the other computers stop with it).
    func shutDown() async {
        _ = try? await post(Self.panelURL.appendingPathComponent("api/quit"), [:])
        try? await Task.sleep(for: .milliseconds(800))
        process?.terminate()
        process = nil
        running = false
    }

    func stopModel() async { _ = try? await post(Self.panelURL.appendingPathComponent("api/stop"), [:]) }

    /// Loads the last model again (also wakes one that is resting).
    func startModel() async {
        guard let s = try? await getJSON(Self.panelURL.appendingPathComponent("api/state")),
              let main = s["main"] as? [String: Any] else { return }
        let cluster = main["cluster"] as? [String: Any]
        guard var m = (cluster?["model"] as? [String: Any]) ?? (main["lastModel"] as? [String: Any]) else { return }
        m["ctx"] = (main["lastCtx"] as? Int) ?? 16384
        m["solo"] = ""
        _ = try? await post(Self.panelURL.appendingPathComponent("api/start"), m)
    }

    /// The person's first choice: what this computer is for.
    func choose(role r: String) async throws {
        _ = try await post(Self.panelURL.appendingPathComponent("api/role"), ["role": r])
        await refresh()
    }

    /// Downloads and starts the recommended model in the size that suits these computers.
    func quickStart() async throws -> String {
        let d = try await post(Self.panelURL.appendingPathComponent("api/quickstart"), [:], timeout: 120)
        let j = (try? JSONSerialization.jsonObject(with: d)) as? [String: Any]
        if let e = j?["error"] as? String { throw NSError(domain: "Potluck", code: 1, userInfo: [NSLocalizedDescriptionKey: e]) }
        await refresh()
        return j?["title"] as? String ?? ""
    }

    func answer(_ q: Question, _ text: String) async {
        _ = try? await post(Self.chatURL.appendingPathComponent("potluck/sessions/\(q.session)/answer"), ["question": q.id, "answer": text])
        waiting.removeAll { $0.id == q.id }
    }

    func answer(session: String, question: String, text: String) async {
        _ = try? await post(Self.chatURL.appendingPathComponent("potluck/sessions/\(session)/answer"), ["question": question, "answer": text])
        waiting.removeAll { $0.id == question }
    }

    // MARK: - status

    private func ping() async -> Bool {
        var r = URLRequest(url: Self.panelURL.appendingPathComponent("api/state"))
        r.timeoutInterval = 1.5
        return ((try? await URLSession.shared.data(for: r).1) as? HTTPURLResponse)?.statusCode == 200
    }

    func refresh() async {
        guard let s = try? await getJSON(Self.panelURL.appendingPathComponent("api/state")) else {
            running = false
            return
        }
        running = true
        checked = true
        version = s["version"] as? String ?? ""
        role = s["role"] as? String ?? ""
        if let hp = s["helper"] as? [String: Any], let link = hp["link"] as? [String: Any] {
            linkPhase = link["phase"] as? String ?? ""
            linkNote = link["note"] as? String ?? ""
            mainName = link["mainName"] as? String ?? ""
            pairCode = link["code"] as? String ?? ""
        }
        if let main = s["main"] as? [String: Any], let c = main["cluster"] as? [String: Any] {
            phase = c["phase"] as? String ?? "idle"
            if phase == "idle", main["asleep"] as? [String: Any] != nil { phase = "resting" }
            let m = (c["model"] as? [String: Any]) ?? (main["asleep"] as? [String: Any]) ?? (main["lastModel"] as? [String: Any])
            modelTitle = (m?["title"] as? String) ?? (m?["file"] as? String) ?? ""
            errorText = c["error"] as? String ?? ""
            tps = c["tps"] as? Double ?? 0
            stepText = ((c["steps"] as? [[String: Any]])?.last { ($0["state"] as? String) == "doing" }?["text"] as? String) ?? ""
            hasModel = c["model"] as? [String: Any] != nil || main["lastModel"] as? [String: Any] != nil || main["asleep"] as? [String: Any] != nil
            computers = ((main["nodes"] as? [[String: Any]]) ?? []).map {
                (name: (($0["info"] as? [String: Any])?["name"] as? String) ?? "?", online: $0["online"] as? Bool ?? false, role: $0["role"] as? String ?? "")
            }
        }
        if let q = try? await getJSON(Self.chatURL.appendingPathComponent("potluck/questions")),
           let list = q["questions"] as? [[String: Any]],
           let data = try? JSONSerialization.data(withJSONObject: list),
           let qs = try? JSONDecoder().decode([Question].self, from: data) {
            waiting = qs
            for q in qs where !seenQuestions.contains(q.id) {
                seenQuestions.insert(q.id)
                onNewQuestion?(q)
            }
        }
    }

    // MARK: - plumbing

    private func getJSON(_ url: URL) async throws -> [String: Any] {
        var r = URLRequest(url: url)
        r.timeoutInterval = 3
        let (d, resp) = try await URLSession.shared.data(for: r)
        guard (resp as? HTTPURLResponse)?.statusCode == 200 else { throw URLError(.badServerResponse) }
        return (try JSONSerialization.jsonObject(with: d)) as? [String: Any] ?? [:]
    }

    @discardableResult
    private func post(_ url: URL, _ body: [String: Any], timeout: TimeInterval = 30) async throws -> Data {
        var r = URLRequest(url: url)
        r.httpMethod = "POST"
        r.timeoutInterval = timeout
        r.setValue("application/json", forHTTPHeaderField: "Content-Type")
        r.setValue("1", forHTTPHeaderField: "X-Potluck") // Potluck's own pages and apps send this
        r.httpBody = try JSONSerialization.data(withJSONObject: body)
        return try await URLSession.shared.data(for: r).0
    }
}
