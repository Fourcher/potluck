import SwiftUI

/// The list of chats, with settings at the bottom.
struct ChatsSheet: View {
    @Environment(AppModel.self) private var model
    @Environment(\.dismiss) private var dismiss
    @State private var search = ""
    @State private var confirmForget = false

    var body: some View {
        @Bindable var model = model
        NavigationStack {
            List {
                Section {
                    Button { model.newChat(); dismiss() } label: { Label("New chat", systemImage: "square.and.pencil") }
                }
                let shown = model.chats.filter { !$0.messages.isEmpty && (search.isEmpty || $0.displayTitle.localizedCaseInsensitiveContains(search) || $0.messages.contains { $0.content.localizedCaseInsensitiveContains(search) }) }
                Section("Chats") {
                    if shown.isEmpty {
                        Text(search.isEmpty ? "Your chats will appear here. They are kept on this phone only." : "No chats match.")
                            .font(.footnote).foregroundStyle(.secondary)
                    }
                    ForEach(shown) { c in
                        Button {
                            model.open(c.id)
                            dismiss()
                        } label: {
                            VStack(alignment: .leading, spacing: 2) {
                                Text(c.displayTitle).lineLimit(1).foregroundStyle(.primary)
                                Text(c.updated.formatted(.relative(presentation: .named))).font(.caption).foregroundStyle(.secondary)
                            }
                        }
                        .listRowBackground(c.id == model.currentID ? Color.accentColor.opacity(0.1) : nil)
                    }
                    .onDelete { idx in for i in idx { model.delete(shown[i].id) } }
                }
                Section("Settings") {
                    Toggle(isOn: $model.think) {
                        VStack(alignment: .leading) {
                            Text("Think before answering")
                            Text("Slower, better for hard questions.").font(.caption).foregroundStyle(.secondary)
                        }
                    }
                    if let c = model.computer {
                        LabeledContent("Computer", value: c.name)
                        LabeledContent("Address", value: (model.client?.addr ?? c.lastAddr ?? c.addrs.first ?? "") + ":\(c.port)")
                        if let v = model.state?.version { LabeledContent("Potluck", value: v) }
                    }
                    Button("Forget this computer", role: .destructive) { confirmForget = true }
                }
            }
            .searchable(text: $search, prompt: "Search chats")
            .navigationTitle("Potluck")
            .navigationBarTitleDisplayMode(.inline)
            .toolbar { ToolbarItem(placement: .confirmationAction) { Button("Done") { dismiss() } } }
            .confirmationDialog("Forget \(model.computer?.name ?? "this computer")?", isPresented: $confirmForget, titleVisibility: .visible) {
                Button("Forget it", role: .destructive) { model.forget(); dismiss() }
            } message: {
                Text("This phone stops connecting to it. Your chats stay on this phone. To connect again, pair from Potluck's control panel.")
            }
        }
    }
}

/// The models on the user's computers; tapping one starts it.
struct ModelsSheet: View {
    @Environment(AppModel.self) private var model
    @Environment(\.dismiss) private var dismiss
    @State private var loading = true
    @State private var error: String?
    @State private var switching: String?

    var body: some View {
        NavigationStack {
            List {
                if let s = model.state, let m = s.model {
                    Section("Now") {
                        VStack(alignment: .leading, spacing: 4) {
                            Text(m.title).font(.headline)
                            Text(phaseText(s)).font(.footnote).foregroundStyle(.secondary)
                            if s.phase == "starting", let steps = s.steps {
                                ForEach(Array(steps.enumerated()), id: \.offset) { _, st in
                                    Label(st.text, systemImage: st.state == "done" ? "checkmark.circle.fill" : "circle.dotted")
                                        .font(.caption).foregroundStyle(st.state == "done" ? .green : .secondary)
                                }
                            }
                        }
                    }
                }
                Section {
                    if loading { ProgressView() }
                    if let error { Text(error).font(.footnote).foregroundStyle(.red) }
                    ForEach(model.models) { m in
                        Button {
                            Task { await start(m) }
                        } label: {
                            HStack {
                                VStack(alignment: .leading, spacing: 2) {
                                    Text(m.title).foregroundStyle(.primary).lineLimit(2)
                                    Text("\(ByteCountFormatter.string(fromByteCount: m.size, countStyle: .file)) · on \(m.nodeName)")
                                        .font(.caption).foregroundStyle(.secondary)
                                }
                                Spacer()
                                if switching == m.id { ProgressView() }
                                else if m.running { Image(systemName: "checkmark").foregroundStyle(Color.accentColor) }
                            }
                        }
                        .disabled(m.running || switching != nil)
                    }
                } header: {
                    Text("Models on your computers")
                } footer: {
                    Text("Starting another model stops the one running now. To get new models, use Potluck's control panel on your computer.")
                }
            }
            .navigationTitle("Models")
            .navigationBarTitleDisplayMode(.inline)
            .toolbar { ToolbarItem(placement: .confirmationAction) { Button("Done") { dismiss() } } }
            .task { await load() }
            .refreshable { await load() }
        }
    }

    private func phaseText(_ s: ServerState) -> String {
        switch s.phase {
        case "running": return s.helpers > 0 ? "Running on \(s.helpers + 1) computers" : "Running"
        case "resting": return "Resting to free memory. It loads again with your next message."
        case "starting": return "Loading…"
        case "error": return s.error ?? "Stopped with an error."
        default: return "Not running"
        }
    }

    private func load() async {
        loading = true
        defer { loading = false }
        do { try await model.loadModels(); error = nil } catch { self.error = error.localizedDescription }
    }

    private func start(_ m: ModelEntry) async {
        switching = m.id
        defer { switching = nil }
        do {
            try await model.switchTo(m)
            dismiss()
        } catch {
            self.error = error.localizedDescription
        }
    }
}
