import AppKit
import SwiftUI

/// The first thing a new person sees: what this computer is for, then one button to get a
/// model running, then the chat. No browser, nothing to type.
struct WelcomeView: View {
    @Environment(PotluckEngine.self) private var engine
    @Environment(\.openWindow) private var openWindow
    @Environment(\.dismissWindow) private var dismissWindow
    @State private var busy = false
    @State private var error = ""
    @State private var started = ""

    var body: some View {
        VStack(alignment: .leading, spacing: 18) {
            HStack(spacing: 12) {
                Image(nsImage: NSApp.applicationIconImage).resizable().frame(width: 52, height: 52)
                VStack(alignment: .leading, spacing: 2) {
                    Text("Welcome to Potluck").font(.largeTitle.weight(.semibold))
                    Text("Private AI on the computers you already own. Free, no account.").foregroundStyle(.secondary)
                }
            }
            Divider()
            if !engine.checked {
                HStack { ProgressView().controlSize(.small); Text("Starting Potluck…") }
                if !engine.problem.isEmpty { Text(engine.problem).foregroundStyle(.red) }
            } else if engine.role.isEmpty {
                chooseRole
            } else if engine.role == "helper" {
                helperStatus
            } else {
                firstModel
            }
            if !error.isEmpty { Text(error).foregroundStyle(.red).font(.callout) }
            Spacer(minLength: 0)
        }
        .padding(28)
        .frame(width: 640, height: 470)
    }

    // Step 1: what is this computer for?
    private var chooseRole: some View {
        VStack(alignment: .leading, spacing: 12) {
            Text("How will you use this computer?").font(.title2.weight(.semibold))
            RoleCard(title: "Just this computer", detail: "Run AI models here, on this computer. Models you already have from LM Studio, Ollama or Hugging Face are found and used where they are. You can add other computers later.", primary: true) { pick("single") }
            RoleCard(title: "This is my main computer, and I have others to pool", detail: "This one finds your other computers and runs the chat. They lend their memory so together they run bigger models.", primary: false) { pick("main") }
            RoleCard(title: "This is a spare computer", detail: "It lends its memory to your main computer. You decide how much, and at what hours.", primary: false) { pick("helper") }
        }
        .disabled(busy)
    }

    // Step 2: one button to a running model.
    private var firstModel: some View {
        VStack(alignment: .leading, spacing: 14) {
            if engine.phase == "running" || engine.phase == "resting" {
                Label("\(engine.modelTitle.isEmpty ? "Your model" : engine.modelTitle) is ready.", systemImage: "checkmark.circle.fill")
                    .font(.title2.weight(.semibold)).foregroundStyle(.green)
                Text("Chat with it here, from your iPhone, or from other apps. Potluck stays in the menu bar at the top of your screen.")
                    .foregroundStyle(.secondary)
                Button { done(open: "chat") } label: { Text("Start chatting").frame(minWidth: 160) }
                    .controlSize(.large).buttonStyle(.borderedProminent).keyboardShortcut(.defaultAction)
            } else if engine.phase == "starting" || busy {
                Text("Getting your model ready").font(.title2.weight(.semibold))
                Text(started.isEmpty ? "Choosing the right size for this computer…" : started).foregroundStyle(.secondary)
                ProgressView().progressViewStyle(.linear)
                Text(engine.stepText.isEmpty ? "Working…" : engine.stepText + "…").font(.callout)
                Text("The first download takes a few minutes. You can close this window; Potluck keeps going and tells you when it's ready.")
                    .font(.caption).foregroundStyle(.secondary)
            } else {
                Text("Get your first model").font(.title2.weight(.semibold))
                Text("Potluck picks a capable, private model in the size that suits \(engine.role == "single" ? "this computer" : "your computers"), downloads it and starts it.")
                    .foregroundStyle(.secondary)
                if engine.phase == "error", !engine.errorText.isEmpty {
                    Text(engine.errorText).foregroundStyle(.red).font(.callout)
                }
                HStack(spacing: 14) {
                    Button { quick() } label: { Text("Download and start").frame(minWidth: 180) }
                        .controlSize(.large).buttonStyle(.borderedProminent).keyboardShortcut(.defaultAction)
                    Button("Choose a different model") { done(open: "panel") }
                }
                if engine.role == "main" {
                    Text("Open Potluck on your other computers and choose \"This is a spare computer\". They appear here by themselves.")
                        .font(.caption).foregroundStyle(.secondary)
                }
            }
        }
    }

    private var helperStatus: some View {
        VStack(alignment: .leading, spacing: 12) {
            switch engine.linkPhase {
            case "connected":
                Label("Lending to \(engine.mainName.isEmpty ? "your main computer" : engine.mainName)", systemImage: "checkmark.circle.fill")
                    .font(.title2.weight(.semibold)).foregroundStyle(.green)
                Text("Nothing else to do here. Potluck stays in the menu bar; choose how much this computer lends from the main computer.")
                    .foregroundStyle(.secondary)
                Button("Done") { done(open: nil) }.controlSize(.large).buttonStyle(.borderedProminent).keyboardShortcut(.defaultAction)
            case "pairing":
                Text("Check this number on your main computer").font(.title2.weight(.semibold))
                Text(engine.pairCode).font(.system(size: 44, weight: .bold, design: .monospaced))
                Text("If your main computer shows the same number, click Add it there.").foregroundStyle(.secondary)
            default:
                Text("Looking for your main computer…").font(.title2.weight(.semibold))
                ProgressView().progressViewStyle(.linear)
                Text(engine.linkNote.isEmpty ? "Open Potluck on your main computer. Both need to be on the same network (or the same Tailscale)." : engine.linkNote)
                    .foregroundStyle(.secondary)
            }
        }
    }

    private func pick(_ role: String) {
        busy = true; error = ""
        Task {
            do { try await engine.choose(role: role) } catch { self.error = error.localizedDescription }
            busy = false
        }
    }

    private func quick() {
        busy = true; error = ""
        Task {
            do {
                let t = try await engine.quickStart()
                started = t.isEmpty ? "" : "Downloading \(t)"
            } catch { self.error = error.localizedDescription }
            busy = false
        }
    }

    private func done(open id: String?) {
        UserDefaults.standard.set(true, forKey: "welcomed")
        if let id { openWindow(id: id) }
        dismissWindow(id: "welcome")
    }
}

private struct RoleCard: View {
    let title: String
    let detail: String
    let primary: Bool
    let action: () -> Void
    @State private var hover = false

    var body: some View {
        Button(action: action) {
            HStack(alignment: .top, spacing: 12) {
                VStack(alignment: .leading, spacing: 4) {
                    Text(title).font(.headline)
                    Text(detail).font(.callout).foregroundStyle(.secondary).fixedSize(horizontal: false, vertical: true)
                }
                Spacer()
                Image(systemName: "chevron.right").foregroundStyle(.tertiary).padding(.top, 2)
            }
            .padding(14)
            .frame(maxWidth: .infinity, alignment: .leading)
            .background(RoundedRectangle(cornerRadius: 10).fill(primary ? Color.accentColor.opacity(hover ? 0.18 : 0.12) : Color.primary.opacity(hover ? 0.08 : 0.04)))
            .overlay(RoundedRectangle(cornerRadius: 10).stroke(primary ? Color.accentColor.opacity(0.6) : Color.primary.opacity(0.1)))
            .contentShape(Rectangle())
        }
        .buttonStyle(.plain)
        .onHover { hover = $0 }
    }
}

/// Offers to move Potluck into Applications when it is opened from Downloads or the disk
/// image, so updates and "Open at login" work. Asked once.
@MainActor
enum MoveToApplications {
    static func offerIfNeeded() {
        let here = Bundle.main.bundleURL
        let path = here.path
        if path.hasPrefix("/Applications/") || path.hasPrefix(NSHomeDirectory() + "/Applications/") { return }
        if UserDefaults.standard.bool(forKey: "noMoveOffer") { return }
        // Only for copies that came from a download or a disk image, not one built locally.
        let fromDownload = path.contains("/AppTranslocation/") || path.hasPrefix("/Volumes/Potluck") || path.contains("/Downloads/")
        guard fromDownload else { return }
        let a = NSAlert()
        a.messageText = "Move Potluck to your Applications folder?"
        a.informativeText = "Potluck works best from Applications: it can update itself there and open when you log in."
        a.addButton(withTitle: "Move to Applications")
        a.addButton(withTitle: "Not now")
        a.showsSuppressionButton = true
        a.suppressionButton?.title = "Don't ask again"
        NSApp.activate(ignoringOtherApps: true)
        let r = a.runModal()
        if a.suppressionButton?.state == .on { UserDefaults.standard.set(true, forKey: "noMoveOffer") }
        guard r == .alertFirstButtonReturn else { return }
        let dest = URL(fileURLWithPath: "/Applications/Potluck.app")
        let fm = FileManager.default
        do {
            if fm.fileExists(atPath: dest.path) {
                try fm.trashItem(at: dest, resultingItemURL: nil)
            }
            try fm.copyItem(at: here, to: dest)
            // The copy is the one the person chose to keep: open it as an ordinary app.
            let x = Process()
            x.executableURL = URL(fileURLWithPath: "/usr/bin/xattr")
            x.arguments = ["-dr", "com.apple.quarantine", dest.path]
            try? x.run(); x.waitUntilExit()
            let relaunch = Process()
            relaunch.executableURL = URL(fileURLWithPath: "/bin/sh")
            relaunch.arguments = ["-c", "sleep 1; /usr/bin/open \"\(dest.path)\""]
            try relaunch.run()
            NSApp.terminate(nil)
        } catch {
            let e = NSAlert()
            e.messageText = "Potluck could not move itself"
            e.informativeText = "Drag Potluck into the Applications folder yourself. (\(error.localizedDescription))"
            e.runModal()
        }
    }
}
