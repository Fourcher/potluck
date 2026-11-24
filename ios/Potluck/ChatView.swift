import PDFKit
import SwiftUI
import UniformTypeIdentifiers

struct ChatView: View {
    @Environment(AppModel.self) private var model
    @State private var draft = ""
    @State private var attachments: [Attachment] = []
    @State private var importing = false
    @State private var showChats = false
    @State private var showModels = false
    @State private var note: String?
    @FocusState private var focused: Bool

    var body: some View {
        let chat = model.current
        VStack(spacing: 0) {
            StatusBar(showModels: $showModels)
            QuestionsBanner()
            ScrollViewReader { proxy in
                ScrollView {
                    LazyVStack(alignment: .leading, spacing: 18) {
                        if let chat, !chat.messages.isEmpty {
                            ForEach(chat.messages) { m in
                                MessageRow(message: m, live: model.streaming == chat.id && m.id == chat.messages.last?.id)
                                    .id(m.id)
                            }
                        } else {
                            Welcome { s in draft = s; focused = true }
                        }
                        Color.clear.frame(height: 1).id("end")
                    }
                    .padding(.horizontal, 16)
                    .padding(.vertical, 12)
                }
                .scrollDismissesKeyboard(.interactively)
                .onChange(of: chat?.messages.last?.content) { _, _ in proxy.scrollTo("end", anchor: .bottom) }
                .onChange(of: chat?.messages.count) { _, _ in withAnimation { proxy.scrollTo("end", anchor: .bottom) } }
                .onChange(of: model.currentID) { _, _ in proxy.scrollTo("end", anchor: .bottom) }
            }
            Composer(draft: $draft, attachments: $attachments, importing: $importing, focused: $focused, note: note)
        }
        .navigationTitle(chat?.displayTitle ?? "Potluck")
        .navigationBarTitleDisplayMode(.inline)
        .toolbar {
            ToolbarItem(placement: .topBarLeading) {
                Button { showChats = true } label: { Image(systemName: "line.3.horizontal") }
                    .accessibilityLabel("Chats and settings")
            }
            ToolbarItem(placement: .topBarTrailing) {
                Button { model.newChat(); draft = ""; attachments = []; focused = true } label: { Image(systemName: "square.and.pencil") }
                    .accessibilityLabel("New chat")
                    .disabled(model.current?.messages.isEmpty ?? true)
            }
        }
        .sheet(isPresented: $showChats) { ChatsSheet() }
        .sheet(isPresented: $showModels) { ModelsSheet() }
        .fileImporter(isPresented: $importing, allowedContentTypes: [.plainText, .text, .sourceCode, .json, .commaSeparatedText, .xml, .html, .pdf, .data],
                      allowsMultipleSelection: true) { result in
            if case .success(let urls) = result { add(urls) }
        }
        .dropDestination(for: URL.self) { urls, _ in add(urls); return true }
    }

    /// Reads files as text (PDFs through PDFKit). Images and other binary files are left out:
    /// the model reads text only.
    private func add(_ urls: [URL]) {
        var skipped: [String] = []
        for u in urls {
            let scoped = u.startAccessingSecurityScopedResource()
            defer { if scoped { u.stopAccessingSecurityScopedResource() } }
            var text: String?
            if u.pathExtension.lowercased() == "pdf" {
                text = PDFDocument(url: u)?.string
            } else if let d = try? Data(contentsOf: u), d.count < 2_000_000 {
                text = String(data: d, encoding: .utf8) ?? String(data: d, encoding: .isoLatin1).flatMap { $0.contains("\u{0}") ? nil : $0 }
            }
            if let t = text, !t.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty, !t.contains("\u{0}") {
                attachments.append(Attachment(name: u.lastPathComponent, text: String(t.prefix(120_000))))
            } else {
                skipped.append(u.lastPathComponent)
            }
        }
        note = skipped.isEmpty ? nil : "Could not read \(skipped.joined(separator: ", ")) as text. The model reads text and PDFs."
    }
}

// MARK: - status

struct StatusBar: View {
    @Environment(AppModel.self) private var model
    @Binding var showModels: Bool

    var body: some View {
        let (dot, line, sub) = describe()
        Button { showModels = true } label: {
            HStack(spacing: 10) {
                Circle().fill(dot).frame(width: 9, height: 9)
                VStack(alignment: .leading, spacing: 1) {
                    Text(line).font(.subheadline.weight(.semibold)).lineLimit(1)
                    Text(sub).font(.caption).foregroundStyle(.secondary).lineLimit(2)
                }
                Spacer()
                Image(systemName: "chevron.up.chevron.down").font(.caption).foregroundStyle(.secondary)
            }
            .padding(.horizontal, 16)
            .padding(.vertical, 8)
            .contentShape(Rectangle())
        }
        .buttonStyle(.plain)
        .background(.bar)
        .accessibilityHint("Choose a model")
        .accessibilityIdentifier("modelStatus")
    }

    private func describe() -> (Color, String, String) {
        let name = model.computer?.name ?? "your computer"
        switch model.link {
        case .connecting: return (.gray, "Connecting to \(name)…", "")
        case .offline(let why): return (.red, "Can't reach \(name)", why)
        case .connected: break
        }
        guard let s = model.state else { return (.gray, name, "") }
        let title = s.model?.title ?? "No model"
        switch s.phase {
        case "running":
            let whereText = s.helpers > 0 ? "Running on \(s.helpers + 1) computers" : "Running on \(s.head ?? name)"
            return (.green, title, whereText)
        case "resting": return (.green.opacity(0.35), title, "Resting to free memory · wakes with your message")
        case "starting":
            let step = s.steps?.last { $0.state == "doing" }?.text ?? s.steps?.last?.text ?? "Loading"
            return (.orange, title, step + "…")
        case "stopping": return (.orange, title, "Stopping…")
        case "error": return (.red, title, s.error ?? "The model stopped with an error.")
        default: return (.gray, "No model running", "Tap to choose one on \(name)")
        }
    }
}

// MARK: - messages

struct MessageRow: View {
    @Environment(AppModel.self) private var model
    let message: Message
    let live: Bool
    @State private var showThinking = false

    var body: some View {
        if message.role == "user" {
            VStack(alignment: .trailing, spacing: 6) {
                ForEach(message.attachments) { a in
                    Label(a.name, systemImage: "doc.text").font(.caption)
                        .padding(.horizontal, 10).padding(.vertical, 6)
                        .background(Color(.secondarySystemBackground), in: Capsule())
                }
                if !message.content.isEmpty {
                    Text(message.content)
                        .textSelection(.enabled)
                        .padding(.horizontal, 14).padding(.vertical, 10)
                        .background(Color.accentColor.opacity(0.14), in: RoundedRectangle(cornerRadius: 18))
                }
            }
            .frame(maxWidth: .infinity, alignment: .trailing)
            .padding(.leading, 40)
            .contextMenu { Button("Copy", systemImage: "doc.on.doc") { UIPasteboard.general.string = message.content } }
        } else {
            VStack(alignment: .leading, spacing: 8) {
                if !message.reasoning.isEmpty {
                    DisclosureGroup(isExpanded: $showThinking) {
                        Text(message.reasoning).font(.footnote).foregroundStyle(.secondary).textSelection(.enabled)
                            .frame(maxWidth: .infinity, alignment: .leading)
                    } label: {
                        Label(live && message.content.isEmpty ? "Thinking…" : "Thought about it", systemImage: "lightbulb")
                            .font(.footnote).foregroundStyle(.secondary)
                    }
                }
                if !message.content.isEmpty {
                    MarkdownView(text: message.content)
                } else if live && message.reasoning.isEmpty {
                    HStack(spacing: 8) {
                        ProgressView()
                        Text(waitingText).font(.footnote).foregroundStyle(.secondary)
                    }
                }
                if let e = message.error {
                    HStack {
                        Label(e, systemImage: "exclamationmark.triangle").font(.footnote).foregroundStyle(.red)
                        Spacer()
                        if model.streaming == nil && model.current?.messages.last?.id == message.id {
                            Button("Try again") { model.retry() }.font(.footnote).buttonStyle(.bordered)
                        }
                    }
                }
                if let s = message.stats, !live {
                    Text("\(s.tokensOut) tokens" + (s.perSecond > 0 ? String(format: " · %.1f/s", s.perSecond) : ""))
                        .font(.caption2).foregroundStyle(.tertiary)
                }
            }
            .frame(maxWidth: .infinity, alignment: .leading)
            .contextMenu {
                Button("Copy", systemImage: "doc.on.doc") { UIPasteboard.general.string = message.content }
                if !live { ShareLink(item: message.content) }
            }
        }
    }

    private var waitingText: String {
        switch model.state?.phase {
        case "resting", "idle": return "Loading the model again…"
        case "starting": return "The model is loading…"
        default: return "Reading…"
        }
    }
}

struct Welcome: View {
    @Environment(AppModel.self) private var model
    var pick: (String) -> Void
    var body: some View {
        VStack(spacing: 14) {
            LogoMark(size: 56).padding(.top, 50)
            Text("What shall we work on?").font(.title2.bold())
            Text("Running privately on \(model.computer?.name ?? "your computer").")
                .font(.subheadline).foregroundStyle(.secondary)
            VStack(spacing: 8) {
                ForEach(["Help me plan my week", "Draft a polite email declining a meeting", "Explain a topic like I'm new to it", "Summarize a file I attach"], id: \.self) { s in
                    Button { pick(s) } label: {
                        Text(s).font(.subheadline).frame(maxWidth: .infinity, alignment: .leading).padding(12)
                            .background(Color(.secondarySystemBackground), in: RoundedRectangle(cornerRadius: 12))
                    }
                    .buttonStyle(.plain)
                }
            }
            .padding(.top, 10)
        }
        .frame(maxWidth: .infinity)
    }
}

// MARK: - composer

struct Composer: View {
    @Environment(AppModel.self) private var model
    @Binding var draft: String
    @Binding var attachments: [Attachment]
    @Binding var importing: Bool
    var focused: FocusState<Bool>.Binding
    var note: String?

    var body: some View {
        @Bindable var model = model
        let busy = model.streaming != nil
        VStack(alignment: .leading, spacing: 8) {
            if let note { Text(note).font(.caption).foregroundStyle(.orange) }
            if !attachments.isEmpty {
                ScrollView(.horizontal, showsIndicators: false) {
                    HStack {
                        ForEach(attachments) { a in
                            HStack(spacing: 4) {
                                Image(systemName: "doc.text")
                                Text(a.name).lineLimit(1)
                                Text("· about \(max(1, a.text.count / 4)) tokens").foregroundStyle(.secondary)
                                Button { attachments.removeAll { $0.id == a.id } } label: { Image(systemName: "xmark.circle.fill") }
                                    .buttonStyle(.plain).foregroundStyle(.secondary)
                                    .accessibilityLabel("Remove \(a.name)")
                            }
                            .font(.caption)
                            .padding(.horizontal, 10).padding(.vertical, 6)
                            .background(Color(.secondarySystemBackground), in: Capsule())
                        }
                    }
                }
            }
            TextField("Message", text: $draft, axis: .vertical)
                .lineLimit(1...6)
                .focused(focused)
                .submitLabel(.return)
            HStack(spacing: 12) {
                Button { importing = true } label: { Image(systemName: "paperclip") }
                    .accessibilityLabel("Attach files")
                Button { model.think.toggle() } label: {
                    Label("Think", systemImage: "lightbulb")
                        .font(.footnote.weight(.medium))
                        .padding(.horizontal, 10).padding(.vertical, 5)
                        .background(model.think ? Color.accentColor.opacity(0.18) : Color.clear, in: Capsule())
                        .overlay(Capsule().stroke(model.think ? Color.accentColor : Color(.separator)))
                }
                .foregroundStyle(model.think ? Color.accentColor : .secondary)
                .accessibilityLabel(model.think ? "Thinking first: on" : "Thinking first: off")
                Spacer()
                Button {
                    if busy { model.stop() } else {
                        model.send(draft, attachments: attachments)
                        draft = ""
                        attachments = []
                    }
                } label: {
                    Image(systemName: busy ? "stop.fill" : "arrow.up")
                        .font(.system(size: 15, weight: .bold))
                        .frame(width: 32, height: 32)
                        .background(Circle().fill(busy || canSend ? Color.accentColor : Color(.systemGray4)))
                        .foregroundStyle(.white)
                }
                .disabled(!busy && !canSend)
                .accessibilityLabel(busy ? "Stop" : "Send")
            }
            .buttonStyle(.plain)
            .font(.title3)
        }
        .padding(12)
        .background(Color(.secondarySystemBackground).opacity(0.6), in: RoundedRectangle(cornerRadius: 20))
        .overlay(RoundedRectangle(cornerRadius: 20).stroke(Color(.separator).opacity(0.5)))
        .padding(.horizontal, 12)
        .padding(.bottom, 8)
    }

    private var canSend: Bool {
        guard case .connected = model.link else { return false }
        return !draft.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty || !attachments.isEmpty
    }
}
