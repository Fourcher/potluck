import SwiftUI
import UIKit
import UserNotifications

/// What tasks running in Potluck are waiting on. Each can be answered with Yes, No, or your own
/// words: here, or straight from the notification without opening the app.
struct QuestionsBanner: View {
    @Environment(AppModel.self) private var model

    var body: some View {
        if !model.questions.isEmpty {
            VStack(alignment: .leading, spacing: 10) {
                Label(model.questions.count == 1 ? "A task is waiting for you" : "\(model.questions.count) tasks are waiting for you", systemImage: "hand.raised.fill")
                    .font(.subheadline.weight(.semibold))
                    .foregroundStyle(Color.accentColor)
                ForEach(model.questions) { q in QuestionCard(question: q) }
            }
            .padding(12)
            .background(Color.accentColor.opacity(0.08))
        }
    }
}

struct QuestionCard: View {
    @Environment(AppModel.self) private var model
    let question: TaskQuestion
    @State private var reply = ""
    @State private var busy = false
    @State private var error: String?

    var body: some View {
        VStack(alignment: .leading, spacing: 8) {
            if let t = question.task, !t.isEmpty {
                Text(t).font(.caption).foregroundStyle(.secondary).lineLimit(1)
            }
            Text(question.title).font(.body.weight(.medium))
            if let d = question.detail, !d.isEmpty {
                ScrollView { Text(d).font(.caption.monospaced()).frame(maxWidth: .infinity, alignment: .leading).textSelection(.enabled) }
                    .frame(maxHeight: 140)
                    .padding(8)
                    .background(Color(.secondarySystemBackground), in: RoundedRectangle(cornerRadius: 8))
            }
            HStack {
                Button { send("yes") } label: { Text("Yes").frame(maxWidth: .infinity) }
                    .buttonStyle(.borderedProminent)
                Button { send("no") } label: { Text("No").frame(maxWidth: .infinity) }
                    .buttonStyle(.bordered)
                    .tint(.secondary)
            }
            .disabled(busy)
            HStack {
                TextField("Or answer in your own words", text: $reply, axis: .vertical)
                    .lineLimit(1...4)
                    .textFieldStyle(.roundedBorder)
                    .submitLabel(.send)
                    .onSubmit { if !reply.isEmpty { send(reply) } }
                if !reply.isEmpty {
                    Button("Send") { send(reply) }.disabled(busy)
                }
            }
            if let error { Text(error).font(.caption).foregroundStyle(.red) }
        }
        .padding(12)
        .background(Color(.systemBackground), in: RoundedRectangle(cornerRadius: 12))
    }

    private func send(_ text: String) {
        busy = true
        error = nil
        Task {
            do { try await model.answer(question, text) } catch { self.error = error.localizedDescription }
            busy = false
        }
    }
}

/// Notifications: asks once for permission, registers for Apple's push alerts, and answers from
/// the alert's own buttons (Yes, No, Reply) without opening the app.
final class AppDelegate: NSObject, UIApplicationDelegate, UNUserNotificationCenterDelegate {
    var model: AppModel?

    static let category = "POTLUCK_QUESTION"

    func application(_ app: UIApplication, didFinishLaunchingWithOptions opts: [UIApplication.LaunchOptionsKey: Any]? = nil) -> Bool {
        let center = UNUserNotificationCenter.current()
        center.delegate = self
        let yes = UNNotificationAction(identifier: "YES", title: "Yes", options: [])
        let no = UNNotificationAction(identifier: "NO", title: "No", options: [.destructive])
        let reply = UNTextInputNotificationAction(identifier: "REPLY", title: "Reply…", options: [], textInputButtonTitle: "Send", textInputPlaceholder: "Your answer")
        center.setNotificationCategories([UNNotificationCategory(identifier: Self.category, actions: [yes, no, reply], intentIdentifiers: [], options: [])])
        return true
    }

    /// Called once the phone is paired: ask to show alerts and register for them.
    func enableAlerts() {
        UNUserNotificationCenter.current().requestAuthorization(options: [.alert, .sound, .badge, .timeSensitive]) { ok, _ in
            guard ok else { return }
            DispatchQueue.main.async { UIApplication.shared.registerForRemoteNotifications() }
        }
    }

    func application(_ app: UIApplication, didRegisterForRemoteNotificationsWithDeviceToken token: Data) {
        let hex = token.map { String(format: "%02x", $0) }.joined()
        Task { @MainActor in self.model?.registerPush(token: hex) }
    }

    func application(_ app: UIApplication, didFailToRegisterForRemoteNotificationsWithError error: Error) {
        // Without push (no push key in Potluck, or a build without the push capability) the app
        // still shows waiting questions whenever it is open.
    }

    // An alert arrives while the app is open: show it anyway and refresh the list.
    func userNotificationCenter(_ center: UNUserNotificationCenter, willPresent n: UNNotification) async -> UNNotificationPresentationOptions {
        await MainActor.run { model?.refreshNow() }
        return [.banner, .sound, .list]
    }

    // A button on the alert was pressed (or the alert itself).
    func userNotificationCenter(_ center: UNUserNotificationCenter, didReceive response: UNNotificationResponse) async {
        let info = response.notification.request.content.userInfo
        guard let session = info["session"] as? String, let question = info["question"] as? String else { return }
        var answer: String?
        switch response.actionIdentifier {
        case "YES": answer = "yes"
        case "NO": answer = "no"
        case "REPLY": answer = (response as? UNTextInputNotificationResponse)?.userText
        default: break // the alert itself: the app opens on the list
        }
        if let a = answer, !a.trimmingCharacters(in: .whitespaces).isEmpty {
            await model?.answer(session: session, question: question, text: a)
        } else {
            await MainActor.run { model?.refreshNow() }
        }
    }
}
