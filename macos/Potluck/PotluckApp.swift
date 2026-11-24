import AppKit
import ServiceManagement
import SwiftUI
import UserNotifications
import WebKit

@main
struct PotluckApp: App {
    @NSApplicationDelegateAdaptor(AppDelegate.self) private var delegate

    var body: some Scene {
        MenuBarExtra {
            MenuContent().environment(delegate.engine).environment(delegate.updater)
        } label: {
            MenuIcon().environment(delegate.engine)
        }
        .menuBarExtraStyle(.window)

        Window("Welcome to Potluck", id: "welcome") {
            WelcomeView().environment(delegate.engine)
        }
        .windowResizability(.contentSize)

        Window("Potluck", id: "chat") {
            WebView(url: PotluckEngine.chatURL).frame(minWidth: 720, minHeight: 520)
        }
        .defaultSize(width: 1100, height: 780)

        Window("Potluck control panel", id: "panel") {
            WebView(url: PotluckEngine.panelURL).frame(minWidth: 720, minHeight: 520)
        }
        .defaultSize(width: 1100, height: 820)

        Settings {
            SettingsView().environment(delegate.engine).environment(delegate.updater)
        }
    }
}

/// Starts Potluck when the app opens, and answers task questions from notifications.
@MainActor
final class AppDelegate: NSObject, NSApplicationDelegate, UNUserNotificationCenterDelegate {
    let engine = PotluckEngine()
    let updater = Updater()

    func applicationDidFinishLaunching(_ n: Notification) {
        MoveToApplications.offerIfNeeded()
        let center = UNUserNotificationCenter.current()
        center.delegate = self
        let yes = UNNotificationAction(identifier: "YES", title: "Yes")
        let no = UNNotificationAction(identifier: "NO", title: "No", options: [.destructive])
        let reply = UNTextInputNotificationAction(identifier: "REPLY", title: "Reply…", options: [], textInputButtonTitle: "Send", textInputPlaceholder: "Your answer")
        center.setNotificationCategories([UNNotificationCategory(identifier: "POTLUCK_QUESTION", actions: [yes, no, reply], intentIdentifiers: [])])
        center.requestAuthorization(options: [.alert, .sound, .badge]) { _, _ in }
        Task { @MainActor in
            engine.onNewQuestion = { q in
                let c = UNMutableNotificationContent()
                c.title = "Potluck needs you"
                c.body = q.title + ((q.detail ?? "").isEmpty || q.kind == "plan" ? "" : "\n" + String((q.detail ?? "").prefix(180)))
                c.categoryIdentifier = "POTLUCK_QUESTION"
                c.sound = .default
                c.userInfo = ["session": q.session, "question": q.id]
                center.add(UNNotificationRequest(identifier: q.id, content: c, trigger: nil))
            }
            engine.begin()
            updater.checkSoon()
        }
    }

    func applicationShouldTerminateAfterLastWindowClosed(_ app: NSApplication) -> Bool { false } // the menu bar item stays

    func userNotificationCenter(_ center: UNUserNotificationCenter, willPresent n: UNNotification) async -> UNNotificationPresentationOptions {
        [.banner, .sound, .list]
    }

    func userNotificationCenter(_ center: UNUserNotificationCenter, didReceive r: UNNotificationResponse) async {
        let info = r.notification.request.content.userInfo
        guard let s = info["session"] as? String, let q = info["question"] as? String else { return }
        let answer: String? = switch r.actionIdentifier {
        case "YES": "yes"
        case "NO": "no"
        case "REPLY": (r as? UNTextInputNotificationResponse)?.userText
        default: nil
        }
        if let a = answer, !a.trimmingCharacters(in: .whitespaces).isEmpty {
            await engine.answer(session: s, question: q, text: a)
        } else {
            await MainActor.run { NSApp.activate(ignoringOtherApps: true) }
        }
    }
}

// MARK: - menu bar

struct MenuIcon: View {
    @Environment(PotluckEngine.self) private var engine
    @Environment(\.openWindow) private var openWindow
    @State private var welcomed = false
    var body: some View {
        icon
            // The first time (or while nothing is set up yet) the welcome window opens by itself,
            // so a new person never faces just an icon in the menu bar.
            .onChange(of: engine.checked) { _, ok in
                guard ok, !welcomed else { return }
                welcomed = true
                if engine.role.isEmpty || (engine.role != "helper" && !engine.hasModel) || !UserDefaults.standard.bool(forKey: "welcomed") {
                    NSApp.setActivationPolicy(.regular)
                    openWindow(id: "welcome")
                    NSApp.activate(ignoringOtherApps: true)
                }
            }
    }

    @ViewBuilder private var icon: some View {
        // Three dots in a circle; filled while a model runs, with a badge when a task waits.
        let symbol = engine.waiting.isEmpty ? (engine.phase == "running" ? "circle.grid.cross.fill" : "circle.grid.cross") : "exclamationmark.circle.fill"
        Image(systemName: symbol)
            .symbolRenderingMode(.hierarchical)
            .accessibilityLabel(engine.waiting.isEmpty ? "Potluck" : "Potluck: a task is waiting for you")
    }
}

struct MenuContent: View {
    @Environment(PotluckEngine.self) private var engine
    @Environment(Updater.self) private var updater
    @Environment(\.openWindow) private var openWindow
    @Environment(\.openSettings) private var openSettings
    @State private var reply = ""

    var body: some View {
        VStack(alignment: .leading, spacing: 10) {
            HStack(spacing: 8) {
                Circle().fill(dotColor).frame(width: 9, height: 9)
                VStack(alignment: .leading, spacing: 1) {
                    Text(headline).font(.headline).lineLimit(2)
                    Text(subline).font(.caption).foregroundStyle(.secondary).lineLimit(3)
                }
            }
            if !engine.problem.isEmpty {
                Text(engine.problem).font(.caption).foregroundStyle(.red)
            }

            if !engine.waiting.isEmpty {
                Divider()
                Text(engine.waiting.count == 1 ? "A task is waiting for you" : "\(engine.waiting.count) tasks are waiting for you")
                    .font(.subheadline.weight(.semibold)).foregroundStyle(.orange)
                ForEach(engine.waiting.prefix(3)) { q in
                    VStack(alignment: .leading, spacing: 6) {
                        Text(q.title).font(.callout)
                        if let d = q.detail, !d.isEmpty {
                            Text(d).font(.caption.monospaced()).lineLimit(6).foregroundStyle(.secondary)
                        }
                        HStack {
                            Button("Yes") { Task { await engine.answer(q, "yes") } }.keyboardShortcut(.defaultAction)
                            Button("No") { Task { await engine.answer(q, "no") } }
                            TextField("Or your answer", text: $reply).onSubmit {
                                let r = reply; reply = ""
                                Task { await engine.answer(q, r) }
                            }
                        }
                    }
                }
            }

            if !engine.computers.isEmpty {
                Divider()
                ForEach(Array(engine.computers.enumerated()), id: \.offset) { _, c in
                    HStack {
                        Circle().fill(c.online ? .green : .gray).frame(width: 6, height: 6)
                        Text(c.name).font(.caption)
                        Spacer()
                        Text(c.role == "head" ? "runs the model" : c.role == "worker" ? "holds part of it" : c.online ? "connected" : "offline")
                            .font(.caption).foregroundStyle(.secondary)
                    }
                }
            }

            Divider()
            Button { open("chat") } label: { Label("Open chat", systemImage: "bubble.left.and.bubble.right") }
            Button { open("panel") } label: { Label("Control panel", systemImage: "slider.horizontal.3") }
            if engine.running {
                if engine.phase == "running" || engine.phase == "starting" {
                    Button { Task { await engine.stopModel() } } label: { Label("Stop the model", systemImage: "stop.circle") }
                } else if !engine.modelTitle.isEmpty {
                    Button { Task { await engine.startModel() } } label: { Label("Load \(engine.modelTitle)", systemImage: "play.circle") }
                }
                Button { Task { await engine.shutDown() } } label: { Label("Pause Potluck", systemImage: "pause.circle") }
            } else {
                Button { Task { await engine.ensureRunning() } } label: { Label("Start Potluck", systemImage: "play.circle.fill") }
            }
            if let u = updater.available {
                Button { Task { await updater.install() } } label: { Label(updater.busy ? "Updating…" : "Update to \(u)", systemImage: "arrow.down.circle") }
                    .disabled(updater.busy)
            }
            Divider()
            HStack {
                Button("Settings…") { NSApp.activate(ignoringOtherApps: true); openSettings() }
                Spacer()
                Button("Quit") { Task { await engine.shutDown(); NSApp.terminate(nil) } }
            }
            .buttonStyle(.borderless)
        }
        .buttonStyle(.borderless)
        .padding(14)
        .frame(width: 320)
    }

    private func open(_ id: String) {
        NSApp.setActivationPolicy(.regular) // a Dock icon while a window is open
        openWindow(id: id)
        NSApp.activate(ignoringOtherApps: true)
    }

    private var dotColor: Color {
        if !engine.running { return .gray }
        switch engine.phase {
        case "running": return .green
        case "starting", "stopping": return .orange
        case "error": return .red
        case "resting": return .green.opacity(0.4)
        default: return .gray
        }
    }

    private var headline: String {
        if !engine.running { return "Potluck is not running" }
        switch engine.phase {
        case "running", "resting", "starting": return engine.modelTitle.isEmpty ? "Potluck" : engine.modelTitle
        case "error": return "The model stopped"
        default: return "No model is running"
        }
    }

    private var subline: String {
        if !engine.running { return "Start it to use your models here and from your phone." }
        switch engine.phase {
        case "running": return engine.tps > 0 ? String(format: "Running · %.1f words a second", engine.tps * 0.75) : "Running"
        case "resting": return "Resting to free memory · wakes with your next message"
        case "starting": return engine.stepText.isEmpty ? "Loading…" : engine.stepText + "…"
        case "stopping": return "Stopping…"
        case "error": return engine.errorText
        default: return "Choose one in the control panel."
        }
    }
}

// MARK: - settings

struct SettingsView: View {
    @Environment(PotluckEngine.self) private var engine
    @Environment(Updater.self) private var updater
    @State private var atLogin = SMAppService.mainApp.status == .enabled
    @State private var dataDir = ""

    var body: some View {
        Form {
            Toggle("Open Potluck when I log in", isOn: $atLogin)
                .onChange(of: atLogin) { _, on in
                    do { if on { try SMAppService.mainApp.register() } else { try SMAppService.mainApp.unregister() } } catch { atLogin = !on }
                }
            LabeledContent("Data folder") {
                HStack {
                    Text(dataDir).font(.caption).lineLimit(1).truncationMode(.middle)
                    Button("Change…") {
                        let p = NSOpenPanel()
                        p.canChooseDirectories = true
                        p.canChooseFiles = false
                        p.canCreateDirectories = true
                        if p.runModal() == .OK, let u = p.url { engine.dataDir = u.path; dataDir = u.path }
                    }
                }
            }
            Text("Where Potluck keeps its settings, logs and downloaded models. Takes effect the next time Potluck starts.")
                .font(.caption).foregroundStyle(.secondary)
            LabeledContent("Version") {
                HStack {
                    Text(Bundle.main.object(forInfoDictionaryKey: "CFBundleShortVersionString") as? String ?? "?")
                    Button(updater.busy ? "Checking…" : "Check for updates") { Task { await updater.check(force: true) } }.disabled(updater.busy)
                }
            }
            if !updater.note.isEmpty { Text(updater.note).font(.caption).foregroundStyle(.secondary) }
        }
        .formStyle(.grouped)
        .frame(width: 480)
        .onAppear { dataDir = engine.dataDir }
    }
}

// MARK: - native windows around Potluck's pages

struct WebView: NSViewRepresentable {
    let url: URL
    func makeNSView(context: Context) -> WKWebView {
        let conf = WKWebViewConfiguration()
        conf.preferences.setValue(true, forKey: "developerExtrasEnabled")
        let v = WKWebView(frame: .zero, configuration: conf)
        v.navigationDelegate = context.coordinator
        v.uiDelegate = context.coordinator
        v.load(URLRequest(url: url))
        return v
    }
    func updateNSView(_ v: WKWebView, context: Context) {}
    func makeCoordinator() -> Coordinator { Coordinator() }

    final class Coordinator: NSObject, WKNavigationDelegate, WKUIDelegate {
        // Links to other sites open in the person's browser; Potluck's own pages stay here.
        func webView(_ w: WKWebView, decidePolicyFor a: WKNavigationAction) async -> WKNavigationActionPolicy {
            if let u = a.request.url, a.navigationType == .linkActivated, u.host != "127.0.0.1", u.host != "localhost" {
                NSWorkspace.shared.open(u)
                return .cancel
            }
            return .allow
        }
        func webView(_ w: WKWebView, createWebViewWith c: WKWebViewConfiguration, for a: WKNavigationAction, windowFeatures f: WKWindowFeatures) -> WKWebView? {
            if let u = a.request.url { NSWorkspace.shared.open(u) }
            return nil
        }
        func webView(_ w: WKWebView, runJavaScriptConfirmPanelWithMessage m: String, initiatedByFrame f: WKFrameInfo) async -> Bool {
            let alert = NSAlert()
            alert.messageText = m
            alert.addButton(withTitle: "OK")
            alert.addButton(withTitle: "Cancel")
            return alert.runModal() == .alertFirstButtonReturn
        }
        func webView(_ w: WKWebView, runJavaScriptAlertPanelWithMessage m: String, initiatedByFrame f: WKFrameInfo) async {
            let alert = NSAlert()
            alert.messageText = m
            alert.runModal()
        }
        func webView(_ w: WKWebView, runJavaScriptTextInputPanelWithPrompt p: String, defaultText d: String?, initiatedByFrame f: WKFrameInfo) async -> String? {
            let alert = NSAlert()
            alert.messageText = p
            let field = NSTextField(frame: NSRect(x: 0, y: 0, width: 300, height: 24))
            field.stringValue = d ?? ""
            alert.accessoryView = field
            alert.addButton(withTitle: "OK")
            alert.addButton(withTitle: "Cancel")
            return alert.runModal() == .alertFirstButtonReturn ? field.stringValue : nil
        }
    }
}
