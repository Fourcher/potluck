import SwiftUI

@main
struct PotluckApp: App {
    @State private var model = AppModel()
    @Environment(\.scenePhase) private var phase

    var body: some Scene {
        WindowGroup {
            RootView()
                .environment(model)
                .tint(Color.accentColor)
                .onOpenURL { url in
                    // A pairing link opened from the Camera app or another app.
                    if let code = PairingCode(link: url.absoluteString) {
                        Task { try? await model.pair(code) }
                    }
                }
        }
        .onChange(of: phase) { _, p in
            if p == .active, model.computer != nil { model.startPolling() }
            if p == .background { model.stopPolling(); model.scheduleSave() }
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
