import SwiftUI

/// The tasks running in Potluck: what each is doing, its report, and a way to tell one to
/// carry on or stop. What a task may do was decided on the computer; the phone does not
/// start new work here.
struct TasksSheet: View {
    @Environment(AppModel.self) private var model
    @Environment(\.dismiss) private var dismiss
    @State private var tasks: [PhoneTask] = []
    @State private var error: String?
    @State private var loaded = false

    var body: some View {
        NavigationStack {
            List {
                if let error { Text(error).foregroundStyle(.red).font(.footnote) }
                if loaded && tasks.isEmpty && error == nil {
                    Text("No tasks yet. Start one in Potluck's chat on your computer; it keeps going there, and you can follow it here.")
                        .font(.footnote).foregroundStyle(.secondary)
                }
                ForEach(tasks) { t in
                    NavigationLink { TaskDetail(id: t.id, title: t.title) } label: {
                        VStack(alignment: .leading, spacing: 3) {
                            HStack {
                                Circle().fill(color(t.status)).frame(width: 8, height: 8)
                                Text(t.title).lineLimit(1)
                            }
                            Text(t.question != nil ? "Waiting for you: \(t.question!.title)" : (t.report ?? label(t.status)))
                                .font(.caption).foregroundStyle(.secondary).lineLimit(2)
                        }
                    }
                }
            }
            .navigationTitle("Tasks")
            .navigationBarTitleDisplayMode(.inline)
            .toolbar { ToolbarItem(placement: .confirmationAction) { Button("Done") { dismiss() } } }
            .refreshable { await load() }
            .task { await load() }
        }
    }

    private func load() async {
        guard let c = model.client else { return }
        do { tasks = try await c.tasks(); error = nil } catch { self.error = error.localizedDescription }
        loaded = true
    }
}

func color(_ status: String) -> Color {
    switch status {
    case "working": return .green
    case "waiting": return .orange
    case "error": return .red
    default: return .gray
    }
}

func label(_ status: String) -> String {
    ["working": "Working", "waiting": "Waiting for you", "error": "Stopped with an error", "stopped": "Stopped", "idle": "Finished"][status] ?? status
}

struct TaskDetail: View {
    @Environment(AppModel.self) private var model
    let id: String
    let title: String
    @State private var task: PhoneTask?
    @State private var draft = ""
    @State private var note: String?
    @State private var sending = false

    var body: some View {
        VStack(spacing: 0) {
            ScrollView {
                VStack(alignment: .leading, spacing: 12) {
                    if let t = task {
                        Label(label(t.status), systemImage: "circle.fill").foregroundStyle(color(t.status)).font(.footnote)
                        if let e = t.error, !e.isEmpty, t.status != "working" { Text(e).font(.footnote).foregroundStyle(.red) }
                        if let r = t.report, !r.isEmpty {
                            VStack(alignment: .leading, spacing: 4) {
                                Text("Report").font(.headline)
                                Text(r).font(.footnote).textSelection(.enabled)
                            }
                            .padding(12).frame(maxWidth: .infinity, alignment: .leading)
                            .background(.quaternary.opacity(0.5), in: RoundedRectangle(cornerRadius: 12))
                        }
                        ForEach(Array((t.messages ?? []).enumerated()), id: \.offset) { _, m in
                            switch m.role {
                            case "step":
                                Label(m.text, systemImage: "wrench.and.screwdriver").font(.caption).foregroundStyle(.secondary).lineLimit(2)
                            case "user":
                                Text(m.text).padding(10).background(Color.accentColor.opacity(0.15), in: RoundedRectangle(cornerRadius: 12))
                                    .frame(maxWidth: .infinity, alignment: .trailing)
                            default:
                                Text(m.text).textSelection(.enabled)
                            }
                        }
                    } else {
                        ProgressView().frame(maxWidth: .infinity)
                    }
                    if let note { Text(note).font(.footnote).foregroundStyle(.secondary) }
                }
                .padding(16)
            }
            HStack(spacing: 8) {
                TextField(task?.status == "working" ? "Add a note for its next step" : "Tell it to carry on", text: $draft, axis: .vertical)
                    .textFieldStyle(.roundedBorder).lineLimit(1...4)
                Button { Task { await send() } } label: { Image(systemName: "arrow.up.circle.fill").font(.title2) }
                    .disabled(draft.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty || sending || task?.status == "waiting")
                    .accessibilityLabel("Send")
            }
            .padding(12)
        }
        .navigationTitle(title)
        .navigationBarTitleDisplayMode(.inline)
        .toolbar {
            if task?.status == "working" || task?.status == "waiting" {
                ToolbarItem(placement: .topBarTrailing) {
                    Button("Stop", role: .destructive) { Task { try? await model.client?.taskStop(id); await load() } }
                }
            }
        }
        .task {
            // Followed while open: a slow model's task changes every minute or so.
            while !Task.isCancelled {
                await load()
                try? await Task.sleep(for: .seconds(15))
            }
        }
    }

    private func load() async {
        guard let c = model.client else { return }
        if let t = try? await c.task(id) { task = t }
    }

    private func send() async {
        guard let c = model.client else { return }
        sending = true
        defer { sending = false }
        do {
            try await c.taskMessage(id, draft)
            note = task?.status == "working" ? "Kept for its next step." : "Sent: it carries on."
            draft = ""
            await load()
        } catch {
            note = error.localizedDescription
        }
    }
}
