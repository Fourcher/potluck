import CryptoKit
import Foundation
import Security

// The connection to the computer running Potluck. Everything goes over HTTPS to the
// computer's own certificate, which the pairing code names by its fingerprint: the app
// accepts that certificate and no other, so nobody on the network can stand in for it.

/// A computer this phone is paired with. Its key lives in the Keychain, not here.
struct Computer: Codable, Equatable {
    var name: String
    var addrs: [String]
    var port: Int
    var fingerprint: String
    var id: String
    var lastAddr: String?
}

/// What a pairing code (QR or link) carries.
struct PairingCode: Equatable {
    var name: String
    var addrs: [String]
    var port: Int
    var fingerprint: String
    var secret: String

    /// Reads potluck://pair?n=…&a=ip1,ip2&p=7721&f=…&s=…
    init?(link: String) {
        let text = link.trimmingCharacters(in: .whitespacesAndNewlines)
        guard let c = URLComponents(string: text), c.scheme == "potluck", c.host == "pair" else { return nil }
        var q: [String: String] = [:]
        for item in c.queryItems ?? [] { q[item.name] = item.value ?? "" }
        guard let a = q["a"], let p = q["p"].flatMap(Int.init), let f = q["f"], let s = q["s"],
              f.count == 64, !s.isEmpty else { return nil }
        let addrs = a.split(separator: ",").map(String.init).filter { !$0.isEmpty }
        guard !addrs.isEmpty else { return nil }
        name = q["n"] ?? "Your computer"
        self.addrs = addrs
        port = p
        fingerprint = f.lowercased()
        secret = s
    }
}

enum PotluckError: LocalizedError {
    case unreachable(String)
    case server(String)
    case notPaired

    var errorDescription: String? {
        switch self {
        case .unreachable(let s): return s
        case .server(let s): return s
        case .notPaired: return "This phone is not paired with that computer any more. Pair it again from Potluck's control panel."
        }
    }
}

/// Accepts the computer's certificate only if its SHA-256 fingerprint matches.
final class PinnedTrust: NSObject, URLSessionDelegate, URLSessionTaskDelegate, @unchecked Sendable {
    let fingerprint: String
    init(fingerprint: String) { self.fingerprint = fingerprint }

    func urlSession(_ session: URLSession, didReceive challenge: URLAuthenticationChallenge) async
        -> (URLSession.AuthChallengeDisposition, URLCredential?) {
        guard challenge.protectionSpace.authenticationMethod == NSURLAuthenticationMethodServerTrust,
              let trust = challenge.protectionSpace.serverTrust,
              let chain = SecTrustCopyCertificateChain(trust) as? [SecCertificate],
              let leaf = chain.first else {
            return (.cancelAuthenticationChallenge, nil)
        }
        let der = SecCertificateCopyData(leaf) as Data
        let hex = SHA256.hash(data: der).map { String(format: "%02x", $0) }.joined()
        return hex == fingerprint ? (.useCredential, URLCredential(trust: trust)) : (.cancelAuthenticationChallenge, nil)
    }
}

struct Hello: Decodable { var name: String; var version: String; var phone: String? }

struct ServerState: Decodable, Equatable {
    struct Model: Decodable, Equatable { var title: String; var file: String; var size: Int64 }
    struct Step: Decodable, Equatable { var text: String; var state: String }
    struct Node: Decodable, Equatable { var name: String; var role: String; var online: Bool }
    var version: String?
    var phase: String
    var error: String?
    var model: Model?
    var steps: [Step]?
    var nodes: [Node]?
    var ctx: Int?

    var helpers: Int { (nodes ?? []).filter { $0.role == "worker" }.count }
    var head: String? { (nodes ?? []).first { $0.role == "head" }?.name }
}

struct ModelEntry: Decodable, Identifiable, Equatable {
    var title: String
    var file: String
    var size: Int64
    var node: String
    var nodeName: String
    var running: Bool
    var id: String { node + "/" + file }
}

struct Stats: Codable, Equatable {
    var tokensIn: Int
    var tokensOut: Int
    var perSecond: Double
}

enum ChatEvent {
    case reasoning(String)
    case content(String)
    case stats(Stats)
}

final class PotluckClient: @unchecked Sendable {
    let computer: Computer
    private let key: String
    private let session: URLSession
    private(set) var addr: String?

    init(computer: Computer, key: String) {
        self.computer = computer
        self.key = key
        let conf = URLSessionConfiguration.ephemeral
        conf.timeoutIntervalForRequest = 900 // a model that was let go loads again first
        conf.waitsForConnectivity = false
        session = URLSession(configuration: conf, delegate: PinnedTrust(fingerprint: computer.fingerprint), delegateQueue: nil)
        addr = computer.lastAddr
    }

    static func url(_ addr: String, _ port: Int, _ path: String) -> URL {
        URL(string: "https://\(addr):\(port)\(path)")!
    }

    /// Pairs with a computer: swaps the code's one-time secret for a key of this phone's own.
    static func pair(_ code: PairingCode, phoneName: String) async throws -> (Computer, String) {
        let conf = URLSessionConfiguration.ephemeral
        conf.timeoutIntervalForRequest = 6
        let session = URLSession(configuration: conf, delegate: PinnedTrust(fingerprint: code.fingerprint), delegateQueue: nil)
        defer { session.finishTasksAndInvalidate() }
        var lastError: Error = PotluckError.unreachable("Could not reach \(code.name).")
        var silent: [String] = [] // addresses that did not answer at all
        for addr in code.addrs {
            var req = URLRequest(url: url(addr, code.port, "/phone/pair"))
            req.httpMethod = "POST"
            req.setValue("application/json", forHTTPHeaderField: "Content-Type")
            req.httpBody = try JSONSerialization.data(withJSONObject: ["secret": code.secret, "name": phoneName])
            do {
                let (data, resp) = try await session.data(for: req)
                try check(data, resp)
                struct Res: Decodable { var key: String; var id: String; var name: String }
                let r = try JSONDecoder().decode(Res.self, from: data)
                let c = Computer(name: r.name, addrs: code.addrs, port: code.port, fingerprint: code.fingerprint, id: r.id, lastAddr: addr)
                return (c, r.key)
            } catch let e as PotluckError {
                if case .server = e { throw e } // the computer answered: no use trying other addresses
                lastError = e
                silent.append(addr)
            } catch {
                lastError = error
                silent.append(addr)
            }
        }
        // Say which addresses were tried and what to do, not just that it failed.
        if !silent.isEmpty, silent.count == code.addrs.count {
            let tried = silent.map { $0.hasPrefix("100.") ? "\($0) (Tailscale)" : "\($0) (home network)" }.joined(separator: " and ")
            var hint = "Check the phone is on the same Wi-Fi as \(code.name), and that Potluck is running there."
            if silent.contains(where: { $0.hasPrefix("100.") }) {
                hint = "If this phone has Tailscale, turn it on in the Tailscale app and try again: it reaches \(code.name) from anywhere, and some computers only accept phones that way. Otherwise check the phone is on the same Wi-Fi."
            }
            throw PotluckError.unreachable("No answer from \(code.name) at \(tried). \(hint)")
        }
        throw friendly(lastError, name: code.name)
    }

    /// Finds an address that answers, the last one that worked first.
    func connect() async throws -> Hello {
        var order = computer.addrs
        if let a = addr, let i = order.firstIndex(of: a) { order.remove(at: i); order.insert(a, at: 0) }
        var lastError: Error = PotluckError.unreachable("Could not reach \(computer.name).")
        for a in order {
            do {
                var req = request(a, "/phone/hello")
                req.timeoutInterval = 5
                let (data, resp) = try await session.data(for: req)
                try Self.check(data, resp)
                addr = a
                return try JSONDecoder().decode(Hello.self, from: data)
            } catch PotluckError.notPaired {
                throw PotluckError.notPaired
            } catch {
                lastError = error
            }
        }
        throw Self.friendly(lastError, name: computer.name)
    }

    func state() async throws -> ServerState { try await get("/potluck/state") }

    func models() async throws -> [ModelEntry] {
        struct Res: Decodable { var models: [ModelEntry]? }
        let r: Res = try await get("/potluck/models")
        return r.models ?? []
    }

    func switchModel(_ m: ModelEntry, ctx: Int) async throws {
        var req = try request("/phone/switch")
        req.httpMethod = "POST"
        req.setValue("application/json", forHTTPHeaderField: "Content-Type")
        req.httpBody = try JSONSerialization.data(withJSONObject: ["file": m.file, "node": m.node, "ctx": ctx])
        req.timeoutInterval = 30
        let (data, resp) = try await session.data(for: req)
        try Self.check(data, resp)
    }

    /// Streams one answer. `messages` are OpenAI-style {role, content}.
    func chat(messages: [[String: String]], think: Bool) -> AsyncThrowingStream<ChatEvent, Error> {
        AsyncThrowingStream { cont in
            let task = Task {
                do {
                    var req = try request("/v1/chat/completions")
                    req.httpMethod = "POST"
                    req.setValue("application/json", forHTTPHeaderField: "Content-Type")
                    let body: [String: Any] = [
                        "messages": messages,
                        "stream": true,
                        "stream_options": ["include_usage": true],
                        "cache_prompt": true,
                        // Qwen-style templates read enable_thinking; Inkling 2 reads a reasoning
                        // effort, where "none" keeps its thinking as short as it allows.
                        "chat_template_kwargs": ["enable_thinking": think, "reasoning_effort": think ? "high" : "none"],
                        // The only reliable stop for models that think regardless (Inkling 2).
                        "reasoning_budget_tokens": think ? 4096 : 1,
                        "reasoning_budget_message": "Time to stop thinking and give the answer.",
                    ]
                    req.httpBody = try JSONSerialization.data(withJSONObject: body)
                    let (bytes, resp) = try await session.bytes(for: req)
                    if let h = resp as? HTTPURLResponse, h.statusCode != 200 {
                        var data = Data()
                        for try await b in bytes { data.append(b); if data.count > 65536 { break } }
                        try Self.check(data, resp)
                    }
                    for try await line in bytes.lines {
                        guard line.hasPrefix("data:") else { continue }
                        let payload = line.dropFirst(5).trimmingCharacters(in: .whitespaces)
                        if payload == "[DONE]" { break }
                        guard let d = payload.data(using: .utf8),
                              let j = try? JSONSerialization.jsonObject(with: d) as? [String: Any] else { continue }
                        if let err = j["error"] as? [String: Any] {
                            throw PotluckError.server(err["message"] as? String ?? "The model stopped with an error.")
                        }
                        for ch in j["choices"] as? [[String: Any]] ?? [] {
                            let delta = ch["delta"] as? [String: Any] ?? [:]
                            if let r = delta["reasoning_content"] as? String, !r.isEmpty { cont.yield(.reasoning(r)) }
                            if let c = delta["content"] as? String, !c.isEmpty { cont.yield(.content(c)) }
                        }
                        if j["usage"] != nil || j["timings"] != nil {
                            let u = j["usage"] as? [String: Any] ?? [:]
                            let t = j["timings"] as? [String: Any] ?? [:]
                            let s = Stats(tokensIn: (u["prompt_tokens"] as? Int) ?? (t["prompt_n"] as? Int) ?? 0,
                                          tokensOut: (u["completion_tokens"] as? Int) ?? (t["predicted_n"] as? Int) ?? 0,
                                          perSecond: (t["predicted_per_second"] as? Double) ?? 0)
                            cont.yield(.stats(s))
                        }
                    }
                    cont.finish()
                } catch {
                    cont.finish(throwing: Task.isCancelled ? CancellationError() : Self.friendly(error, name: computer.name))
                }
            }
            cont.onTermination = { _ in task.cancel() }
        }
    }

    // MARK: tasks waiting on the person (Potluck's sessions)

    func questions() async throws -> [TaskQuestion] {
        struct R: Decodable { let questions: [TaskQuestion]? }
        let r: R = try await get("/phone/questions")
        return r.questions ?? []
    }

    func answer(session: String, question: String, answer: String) async throws {
        try await post("/phone/answer", ["session": session, "question": question, "answer": answer])
    }

    func registerPush(token: String, development: Bool) async throws {
        try await post("/phone/push", ["token": token, "env": development ? "development" : "production"])
    }

    private func post(_ path: String, _ body: [String: String]) async throws {
        var req = try request(path)
        req.httpMethod = "POST"
        req.timeoutInterval = 20
        req.setValue("application/json", forHTTPHeaderField: "Content-Type")
        req.httpBody = try JSONSerialization.data(withJSONObject: body)
        let (data, resp) = try await session.data(for: req)
        try Self.check(data, resp)
    }

    func close() { session.invalidateAndCancel() }

    // MARK: - plumbing

    private func request(_ a: String, _ path: String) -> URLRequest {
        var req = URLRequest(url: Self.url(a, computer.port, path))
        req.setValue("Bearer \(key)", forHTTPHeaderField: "Authorization")
        return req
    }

    private func request(_ path: String) throws -> URLRequest {
        guard let a = addr else { throw PotluckError.unreachable("Not connected to \(computer.name).") }
        return request(a, path)
    }

    private func get<T: Decodable>(_ path: String) async throws -> T {
        var req = try request(path)
        req.timeoutInterval = 10
        let (data, resp) = try await session.data(for: req)
        try Self.check(data, resp)
        return try JSONDecoder().decode(T.self, from: data)
    }

    static func check(_ data: Data, _ resp: URLResponse) throws {
        guard let h = resp as? HTTPURLResponse else { return }
        if h.statusCode == 401 { throw PotluckError.notPaired }
        guard (200..<300).contains(h.statusCode) else {
            var msg = String(data: data, encoding: .utf8) ?? ""
            if let j = try? JSONSerialization.jsonObject(with: data) as? [String: Any] {
                if let e = j["error"] as? [String: Any], let m = e["message"] as? String { msg = m }
                else if let m = j["error"] as? String { msg = m }
            }
            msg = msg.replacingOccurrences(of: "Potluck: ", with: "")
            if msg.range(of: "context|exceed|too (long|large)", options: [.regularExpression, .caseInsensitive]) != nil {
                msg = "This conversation no longer fits in the model's memory. Start a new chat."
            }
            throw PotluckError.server(msg.isEmpty ? "The computer answered with an error (\(h.statusCode))." : String(msg.prefix(400)))
        }
    }

    static func friendly(_ error: Error, name: String) -> Error {
        if error is PotluckError || error is CancellationError { return error }
        let ns = error as NSError
        if ns.domain == NSURLErrorDomain {
            switch ns.code {
            case NSURLErrorCancelled, NSURLErrorServerCertificateUntrusted, NSURLErrorSecureConnectionFailed:
                // A refused certificate also shows up as "cancelled"; only a real stop is one.
                if ns.code == NSURLErrorCancelled && Task.isCancelled { return CancellationError() }
                return PotluckError.unreachable("Something answered at \(name)'s address, but it is not \(name). The connection was refused to keep your chats private.")
            case NSURLErrorTimedOut, NSURLErrorCannotConnectToHost, NSURLErrorNetworkConnectionLost, NSURLErrorNotConnectedToInternet, NSURLErrorCannotFindHost:
                return PotluckError.unreachable("Could not reach \(name). Check that Potluck is open on it and that this phone is on the same network or on Tailscale.")
            default: break
            }
        }
        return error
    }
}

// MARK: - Keychain

enum Keychain {
    static let service = "io.github.fourcher.potluck"

    static func save(_ value: String, account: String) {
        let base: [String: Any] = [kSecClass as String: kSecClassGenericPassword, kSecAttrService as String: service, kSecAttrAccount as String: account]
        SecItemDelete(base as CFDictionary)
        var add = base
        add[kSecValueData as String] = Data(value.utf8)
        add[kSecAttrAccessible as String] = kSecAttrAccessibleAfterFirstUnlockThisDeviceOnly
        SecItemAdd(add as CFDictionary, nil)
    }

    static func load(account: String) -> String? {
        let q: [String: Any] = [kSecClass as String: kSecClassGenericPassword, kSecAttrService as String: service, kSecAttrAccount as String: account,
                                kSecReturnData as String: true, kSecMatchLimit as String: kSecMatchLimitOne]
        var out: AnyObject?
        guard SecItemCopyMatching(q as CFDictionary, &out) == errSecSuccess, let d = out as? Data else { return nil }
        return String(data: d, encoding: .utf8)
    }

    static func delete(account: String) {
        let q: [String: Any] = [kSecClass as String: kSecClassGenericPassword, kSecAttrService as String: service, kSecAttrAccount as String: account]
        SecItemDelete(q as CFDictionary)
    }
}


/// Something a task running in Potluck is waiting on: yes, no, or an answer in your own words.
struct TaskQuestion: Codable, Identifiable, Equatable {
    let id: String
    let session: String
    let title: String
    let detail: String?
    let kind: String
    let task: String?
}


import Network

/// Asks iOS for local-network access up front (it only asks the first time the app reaches
/// for the home network, which would otherwise be in the middle of pairing): a tiny mDNS
/// packet does it, and nothing is sent anywhere else.
enum LocalNetwork {
    static func ask() {
        let c = NWConnection(host: "224.0.0.251", port: 5353, using: .udp)
        c.stateUpdateHandler = { state in
            if case .ready = state {
                c.send(content: Data([0]), completion: .contentProcessed { _ in c.cancel() })
            }
        }
        c.start(queue: .global())
        DispatchQueue.global().asyncAfter(deadline: .now() + 5) { c.cancel() }
    }
}
