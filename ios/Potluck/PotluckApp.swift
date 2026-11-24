import SwiftUI

@main
struct PotluckApp: App {
    @UIApplicationDelegateAdaptor(AppDelegate.self) private var delegate
    @State private var model = AppModel()
    @Environment(\.scenePhase) private var phase

    var body: some Scene {
        WindowGroup {
            RootView()
                .environment(model)
                #if DEBUG
                // For App Store screenshots: -demo opens the demo, -demoAsk also asks a question.
                .task {
                    let args = ProcessInfo.processInfo.arguments
                    if args.contains("-demo") || args.contains("-demoAsk") { model.startDemo() }
                    if args.contains("-demoAsk") {
                        try? await Task.sleep(for: .seconds(1))
                        model.send("How do I connect my computer?", attachments: [])
                    }
                }
                #endif
                .tint(Color.accentColor)
                .onOpenURL { url in
                    // A pairing link opened from the Camera app or another app.
                    if let code = PairingCode(link: url.absoluteString) {
                        Task { try? await model.pair(code) }
                    }
                }
        }
        .onChange(of: phase) { _, p in
            delegate.model = model
            if p == .active, model.computer != nil { model.startPolling(); if !model.isDemo { delegate.enableAlerts() } }
            if p == .background { model.stopPolling(); model.scheduleSave() }
        }
        // Right after pairing the app is already in the foreground, so ask for alerts then too.
        .onChange(of: model.computer != nil) { _, paired in
            delegate.model = model
            if paired, !model.isDemo { delegate.enableAlerts() }
        }
    }
}

struct RootView: View {
    @Environment(AppModel.self) private var model

    var body: some View {
        if model.computer == nil || model.notPaired {
            PairView()
        } else {
            NavigationStack { ChatView() }
                .task { model.startPolling() }
        }
    }
}
