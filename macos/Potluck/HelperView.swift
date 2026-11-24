import SwiftUI

/// "Ask Potluck": the built-in helper, a small model of Potluck's own that sets Potluck up and
/// answers questions about it ("install the best model my computers can run", "help me add my
/// other computers"). It acts with Potluck's own controls and asks before anything big.
@MainActor @Observable
final class HelperChat {
    struct Line: Identifiable, Equatable {
        let id: Int
        let who: String   // you | helper | action
        let text: String
    }
    struct Ask: Equatable { let id: String; let title: String; let detail: String }

    var lines: [Line] = []
    var status = "idle"          // idle | working | waiting | error
    var error = ""
    var question: Ask?
    var helperNote = ""          // while the helper's own model gets ready
    private var session: String?
    private var poller: Task<Void, Never>?

    static let base = URL(string: "http://127.0.0.1:7720/potluck/")!

    func send(_ text: String) {
        let t = text.trimmingCharacters(in: .whitespacesAndNewlines)
        guard !t.isEmpty else { return }
        lines.append(Line(id: -lines.count - 1, who: "you", text: t)) // shown at once; replaced by the session's copy
        status = "working"; error = ""
        Task {
            do {
                if let s = session {
                    _ = try await post("sessions/\(s)/message", ["text": t])
                } else {
                    let d = try await post("sessions", ["kind": "helper", "text": t])
                    let j = try JSONSerialization.jsonObject(with: d) as? [String: Any]
                    session = j?["id"] as? String
                }
                watch()
            } catch { self.error = error.localizedDescription; status = "error" }
        }
    }

    func answer(_ text: String) {
        guard let s = session, let q = question else { return }
        question = nil
        status = "working"
        Task { _ = try? await post("sessions/\(s)/answer", ["question": q.id, "answer": text]); watch() }
    }

    func reset() {
        poller?.cancel()
        if let s = session { Task { _ = try? await post("sessions/\(s)/stop", [:]) } }
        session = nil; lines = []; status = "idle"; question = nil; error = ""
    }

    private func watch() {
        poller?.cancel()
        poller = Task {
            while !Task.isCancelled {
                await refresh()
                if status == "idle" || status == "error" || status == "stopped" { break }
                try? await Task.sleep(for: .milliseconds(900))
            }
        }
    }

    private func refresh() async {
        guard let s = session, let d = try? await get("sessions/\(s)"),
              let j = try? JSONSerialization.jsonObject(with: d) as? [String: Any] else { return }
        status = j["status"] as? String ?? "idle"
        error = j["error"] as? String ?? ""
        var out: [Line] = []
        for (i, m) in ((j["messages"] as? [[String: Any]]) ?? []).enumerated() {
            let role = m["role"] as? String ?? ""
            let content = (m["content"] as? String ?? "").trimmingCharacters(in: .whitespacesAndNewlines)
            switch role {
            case "user": out.append(Line(id: i, who: "you", text: content))
            case "assistant":
                if !content.isEmpty { out.append(Line(id: i, who: "helper", text: content)) }
            case "tool":
                if let n = m["name"] as? String, n != "ask_user" {
                    out.append(Line(id: i, who: "action", text: Self.actionText(n, ok: (m["state"] as? String) != "error")))
                }
            default: break
            }
        }
        lines = out
        if let q = j["question"] as? [String: Any], let id = q["id"] as? String {
            question = Ask(id: id, title: q["title"] as? String ?? "", detail: q["detail"] as? String ?? "")
        } else { question = nil }
        if status == "working", out.last?.who == "you",
           let hd = try? await getPanel("api/helper"), let hj = try? JSONSerialization.jsonObject(with: hd) as? [String: Any],
           (hj["phase"] as? String) != "ready" {
            helperNote = (hj["note"] as? String).map { "Getting the helper ready: " + $0 } ?? "Getting the helper ready…"
        } else { helperNote = "" }
    }

    static func actionText(_ tool: String, ok: Bool) -> String {
        let names = ["get_status": "Looked at your computers", "list_drives": "Looked at your drives", "set_data_folder": "Moving Potluck's folder",
                     "recommend_model": "Compared models for your computers", "install_model": "Starting a model", "stop_model": "Stopping the model",
                     "add_computer_steps": "Looked up how to add a computer", "pair_phone": "Made a phone pairing code", "add_ai_connection": "Connecting your other AI app"]
        return (names[tool] ?? tool) + (ok ? "" : " (didn't work)")
    }

    private func get(_ path: String) async throws -> Data {
        var r = URLRequest(url: Self.base.appendingPathComponent(path)); r.timeoutInterval = 10
        return try await URLSession.shared.data(for: r).0
    }
    private func getPanel(_ path: String) async throws -> Data {
        var r = URLRequest(url: PotluckEngine.panelURL.appendingPathComponent(path)); r.timeoutInterval = 5
        return try await URLSession.shared.data(for: r).0
    }
    private func post(_ path: String, _ body: [String: Any]) async throws -> Data {
        var r = URLRequest(url: Self.base.appendingPathComponent(path))
        r.httpMethod = "POST"; r.timeoutInterval = 30
        r.setValue("application/json", forHTTPHeaderField: "Content-Type")
        r.setValue("1", forHTTPHeaderField: "X-Potluck")
        r.httpBody = try JSONSerialization.data(withJSONObject: body)
        let (d, resp) = try await URLSession.shared.data(for: r)
        if (resp as? HTTPURLResponse)?.statusCode ?? 500 >= 400,
           let j = try? JSONSerialization.jsonObject(with: d) as? [String: Any], let e = j["error"] as? String {
            throw NSError(domain: "Potluck", code: 1, userInfo: [NSLocalizedDescriptionKey: e])
        }
        return d
    }
}

struct HelperView: View {
    @Environment(HelperChat.self) private var chat
    @State private var text = ""
    @State private var reply = ""
    @FocusState private var focused: Bool

    private let examples = ["Install the best model my computers can run", "Help me add my other computers", "Put Potluck on another drive", "Connect my iPhone"]

    var body: some View {
        VStack(spacing: 0) {
            ScrollViewReader { proxy in
                ScrollView {
                    VStack(alignment: .leading, spacing: 10) {
                        if chat.lines.isEmpty {
                            VStack(alignment: .leading, spacing: 8) {
                                Text("Ask Potluck").font(.title2.weight(.semibold))
                                Text("Potluck's own helper sets things up for you and answers questions. It asks before anything big.")
                                    .foregroundStyle(.secondary)
                                ForEach(examples, id: \.self) { e in
                                    Button(e) { chat.send(e) }.buttonStyle(.link)
                                }
                            }
                            .padding(.bottom, 8)
                        }
                        ForEach(chat.lines) { l in bubble(l).id(l.id) }
                        if !chat.helperNote.isEmpty {
                            Label(chat.helperNote, systemImage: "hourglass").font(.callout).foregroundStyle(.secondary)
                        } else if chat.status == "working" {
                            HStack(spacing: 6) { ProgressView().controlSize(.small); Text("Working…").foregroundStyle(.secondary) }
                        }
                        if !chat.error.isEmpty { Text(chat.error).foregroundStyle(.red).font(.callout) }
                        Color.clear.frame(height: 1).id("end")
                    }
                    .padding(16)
                }
                .onChange(of: chat.lines) { _, _ in withAnimation { proxy.scrollTo("end") } }
            }
            if let q = chat.question {
                VStack(alignment: .leading, spacing: 8) {
                    Text(q.title).font(.headline)
                    if !q.detail.isEmpty { Text(q.detail).font(.callout).foregroundStyle(.secondary) }
                    HStack {
                        Button("Yes") { chat.answer("yes") }.keyboardShortcut(.defaultAction).buttonStyle(.borderedProminent)
                        Button("No") { chat.answer("no") }
                        TextField("Or type your answer", text: $reply).onSubmit { let r = reply; reply = ""; chat.answer(r) }
                    }
                }
                .padding(14)
                .background(Color.orange.opacity(0.12))
            }
            Divider()
            HStack(spacing: 8) {
                TextField("Ask Potluck anything, like \"install the best model I can run\"", text: $text, axis: .vertical)
                    .lineLimit(1...4).textFieldStyle(.plain).focused($focused)
                    .onSubmit { let t = text; text = ""; chat.send(t) }
                Button { let t = text; text = ""; chat.send(t) } label: { Image(systemName: "arrow.up.circle.fill").font(.title2) }
                    .buttonStyle(.plain).disabled(text.trimmingCharacters(in: .whitespaces).isEmpty)
                Menu { Button("New conversation") { chat.reset() } } label: { Image(systemName: "ellipsis.circle") }
                    .menuStyle(.borderlessButton).fixedSize()
            }
            .padding(12)
        }
        .frame(minWidth: 460, minHeight: 420)
        .onAppear { focused = true }
    }

    @ViewBuilder private func bubble(_ l: HelperChat.Line) -> some View {
        switch l.who {
        case "you":
            HStack { Spacer(minLength: 60); Text(l.text).padding(10).background(Color.accentColor.opacity(0.18), in: RoundedRectangle(cornerRadius: 10)).textSelection(.enabled) }
        case "action":
            Label(l.text, systemImage: "checkmark.circle").font(.caption).foregroundStyle(.secondary)
        default:
            Text((try? AttributedString(markdown: l.text, options: .init(interpretedSyntax: .inlineOnlyPreservingWhitespace))) ?? AttributedString(l.text))
                .padding(10).background(Color.primary.opacity(0.06), in: RoundedRectangle(cornerRadius: 10)).textSelection(.enabled)
        }
    }
}
