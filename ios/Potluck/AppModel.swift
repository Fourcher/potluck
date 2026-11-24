import Foundation
import Observation
import SwiftUI
import UIKit

struct Attachment: Codable, Equatable, Identifiable {
    var id = UUID()
    var name: String
    var text: String
}

struct Message: Codable, Equatable, Identifiable {
    var id = UUID()
    var role: String // user | assistant
    var content: String = ""
    var reasoning: String = ""
    var attachments: [Attachment] = []
    var stats: Stats?
    var error: String?
    var date = Date()
}

struct Conversation: Codable, Equatable, Identifiable {
    var id = UUID()
    var title: String = ""
    var messages: [Message] = []
    var updated = Date()

    var displayTitle: String { title.isEmpty ? "New chat" : title }
}

enum Link: Equatable {
    case connecting
    case connected
    case offline(String)
}

@MainActor
@Observable
final class AppModel {
    // Pairing
    private(set) var computer: Computer?
    private(set) var client: PotluckClient?
    var link: Link = .connecting
    var notPaired = false

    // What the computer is doing
    var state: ServerState?
    var models: [ModelEntry] = []

    // Chats, kept on this phone only
    var chats: [Conversation] = []
    var currentID: UUID?
    var streaming: UUID? // the chat whose answer is being written
    var think: Bool { didSet { UserDefaults.standard.set(think, forKey: "think") } }

    private var sendTask: Task<Void, Never>?
    private var pollTask: Task<Void, Never>?
    private var saveTask: Task<Void, Never>?

    init() {
        think = UserDefaults.standard.bool(forKey: "think")
        if let d = UserDefaults.standard.data(forKey: "computer"), let c = try? JSONDecoder().decode(Computer.self, from: d),
           let key = Keychain.load(account: c.id) {
            computer = c
            client = PotluckClient(computer: c, key: key)
        }
        loadChats()
    }

    var current: Conversation? {
        get { chats.first { $0.id == currentID } }
    }

    // MARK: pairing

    func pair(_ code: PairingCode) async throws {
        let (c, key) = try await PotluckClient.pair(code, phoneName: UIDevice.current.name)
        if let old = computer { Keychain.delete(account: old.id) }
        Keychain.save(key, account: c.id)
        UserDefaults.standard.set(try JSONEncoder().encode(c), forKey: "computer")
        client?.close()
        computer = c
        client = PotluckClient(computer: c, key: key)
        notPaired = false
        link = .connecting
        startPolling()
    }

    func forget() {
        stop()
        pollTask?.cancel()
        if let c = computer { Keychain.delete(account: c.id) }
        UserDefaults.standard.removeObject(forKey: "computer")
        client?.close()
        client = nil
        computer = nil
        state = nil
        models = []
    }

    // MARK: status

    /// Keeps the status fresh while the app is open: every 3 s while something changes,
    /// every 10 s otherwise.
    func startPolling() {
        pollTask?.cancel()
        pollTask = Task { [weak self] in
            var connected = false
            while !Task.isCancelled {
                guard let self, let client = self.client else { return }
                do {
                    if !connected {
                        if case .offline = self.link {} else { self.link = .connecting }
                        _ = try await client.connect()
                        connected = true
                        self.saveLastAddr()
                    }
                    let s = try await client.state()
                    if s != self.state { self.state = s }
                    self.link = .connected
                } catch PotluckError.notPaired {
                    self.notPaired = true
                    self.link = .offline(PotluckError.notPaired.localizedDescription)
                    return
                } catch is CancellationError {
                    return
                } catch {
                    connected = false
                    self.link = .offline(error.localizedDescription)
                }
                let busy = ["starting", "stopping"].contains(self.state?.phase ?? "") || self.streaming != nil
                try? await Task.sleep(for: .seconds(connected ? (busy ? 3 : 10) : 5))
            }
        }
    }

    func stopPolling() { pollTask?.cancel() }

    func refreshNow() {
        startPolling()
    }

    private func saveLastAddr() {
        guard var c = computer, let a = client?.addr, c.lastAddr != a else { return }
        c.lastAddr = a
        computer = c
        UserDefaults.standard.set(try? JSONEncoder().encode(c), forKey: "computer")
    }

    func loadModels() async throws {
        guard let client else { return }
        models = try await client.models()
    }

    func switchTo(_ m: ModelEntry) async throws {
        guard let client else { return }
        try await client.switchModel(m, ctx: max(state?.ctx ?? 8192, 8192))
        refreshNow()
    }

    // MARK: chats

    func newChat() {
        if let c = current, c.messages.isEmpty { return }
        let c = Conversation()
        chats.insert(c, at: 0)
        currentID = c.id
    }

    func open(_ id: UUID) { currentID = id }

    func delete(_ id: UUID) {
        if streaming == id { stop() }
        chats.removeAll { $0.id == id }
        if currentID == id { currentID = chats.first?.id }
        if currentID == nil { newChat() }
        scheduleSave()
    }

    private func update(_ id: UUID, _ f: (inout Conversation) -> Void) {
        guard let i = chats.firstIndex(where: { $0.id == id }) else { return }
        f(&chats[i])
    }

    func send(_ text: String, attachments: [Attachment]) {
        let text = text.trimmingCharacters(in: .whitespacesAndNewlines)
        guard (!text.isEmpty || !attachments.isEmpty), streaming == nil else { return }
        if currentID == nil || current == nil { newChat() }
        guard let id = currentID else { return }
        update(id) { c in
            c.messages.append(Message(role: "user", content: text, attachments: attachments))
            if c.title.isEmpty {
                let t = text.isEmpty ? (attachments.first?.name ?? "Files") : text
                c.title = String(t.prefix(60))
            }
            c.updated = Date()
        }
        // Newest chat first.
        if let i = chats.firstIndex(where: { $0.id == id }), i > 0 { chats.insert(chats.remove(at: i), at: 0) }
        answer(id)
    }

    /// Asks again for the last answer of a chat (after an error, or for another try).
    func retry() {
        guard let id = currentID, streaming == nil else { return }
        update(id) { c in
            if c.messages.last?.role == "assistant" { c.messages.removeLast() }
        }
        answer(id)
    }

    func stop() {
        sendTask?.cancel()
        sendTask = nil
    }

    private func answer(_ id: UUID) {
        guard let client, let conv = chats.first(where: { $0.id == id }) else { return }
        let history = apiMessages(conv.messages)
        let reply = Message(role: "assistant")
        update(id) { $0.messages.append(reply) }
        streaming = id
        let think = self.think
        sendTask = Task { [weak self] in
            do {
                for try await ev in client.chat(messages: history, think: think) {
                    guard let self else { return }
                    self.update(id) { c in
                        guard let i = c.messages.lastIndex(where: { $0.id == reply.id }) else { return }
                        switch ev {
                        case .reasoning(let r): c.messages[i].reasoning += r
                        case .content(let t): c.messages[i].content += t
                        case .stats(let s): c.messages[i].stats = s
                        }
                    }
                }
            } catch is CancellationError {
            } catch {
                self?.update(id) { c in
                    if let i = c.messages.lastIndex(where: { $0.id == reply.id }) { c.messages[i].error = error.localizedDescription }
                }
            }
            guard let self else { return }
            self.update(id) { c in
                c.updated = Date()
                if let i = c.messages.lastIndex(where: { $0.id == reply.id }), Task.isCancelled, c.messages[i].content.isEmpty, c.messages[i].error == nil {
                    c.messages[i].error = "Stopped."
                }
            }
            self.streaming = nil
            self.scheduleSave()
            UIImpactFeedbackGenerator(style: .light).impactOccurred()
        }
    }

    /// The conversation as the model reads it. Attached files go in front of the message
    /// they came with. Earlier thinking is left out: the engine does not read it back.
    private func apiMessages(_ msgs: [Message]) -> [[String: String]] {
        var out: [[String: String]] = [["role": "system", "content": "You are a helpful assistant, running privately on the user's own computer through Potluck. The user is writing from their phone, so keep answers easy to read on a small screen. Today is \(Date().formatted(date: .complete, time: .omitted))."]]
        for m in msgs where m.error == nil || !m.content.isEmpty {
            var text = m.content
            if !m.attachments.isEmpty {
                let files = m.attachments.map { "File: \($0.name)\n```\n\($0.text)\n```" }.joined(separator: "\n\n")
                text = files + (text.isEmpty ? "" : "\n\n" + text)
            }
            if m.role == "assistant" && text.isEmpty { continue }
            out.append(["role": m.role, "content": text])
        }
        return out
    }

    // MARK: storage

    private var chatsURL: URL {
        let dir = FileManager.default.urls(for: .applicationSupportDirectory, in: .userDomainMask)[0]
        try? FileManager.default.createDirectory(at: dir, withIntermediateDirectories: true)
        return dir.appendingPathComponent("chats.json")
    }

    private func loadChats() {
        if let d = try? Data(contentsOf: chatsURL), let c = try? JSONDecoder().decode([Conversation].self, from: d) {
            chats = c.sorted { $0.updated > $1.updated }
        }
        let c = Conversation()
        chats.insert(c, at: 0)
        currentID = c.id
    }

    func scheduleSave() {
        saveTask?.cancel()
        let snapshot = chats.filter { !$0.messages.isEmpty }
        let url = chatsURL
        saveTask = Task.detached(priority: .utility) {
            try? await Task.sleep(for: .milliseconds(400))
            if Task.isCancelled { return }
            if let d = try? JSONEncoder().encode(snapshot) {
                try? d.write(to: url, options: [.atomic, .completeFileProtection])
            }
        }
    }
}
