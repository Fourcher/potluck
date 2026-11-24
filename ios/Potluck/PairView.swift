import AVFoundation
import SwiftUI
import VisionKit

struct PairView: View {
    @Environment(AppModel.self) private var model
    @State private var link = ""
    @State private var scanning = false
    @State private var working = false
    @State private var error: String?

    var body: some View {
        NavigationStack {
            ScrollView {
                VStack(alignment: .leading, spacing: 22) {
                    HStack(spacing: 14) {
                        LogoMark(size: 52)
                        VStack(alignment: .leading, spacing: 2) {
                            Text("Potluck").font(.largeTitle.bold())
                            Text("Your own AI, on your own computer").foregroundStyle(.secondary)
                        }
                    }
                    .padding(.top, 8)

                    if model.notPaired {
                        Notice(text: "\(model.computer?.name ?? "Your computer") no longer knows this phone. Pair it again.", warn: true)
                    }

                    VStack(alignment: .leading, spacing: 10) {
                        Step(n: 1, text: "Open Potluck on your computer and go to **Computers**.")
                        Step(n: 2, text: "Under **Your iPhone**, choose **Pair a phone**.")
                        Step(n: 3, text: "Scan the code it shows.")
                    }

                    Button {
                        error = nil
                        scanning = true
                    } label: {
                        Label("Scan the pairing code", systemImage: "qrcode.viewfinder")
                            .frame(maxWidth: .infinity)
                            .padding(.vertical, 6)
                    }
                    .buttonStyle(.borderedProminent)
                    .controlSize(.large)
                    .disabled(working)

                    VStack(alignment: .leading, spacing: 8) {
                        Text("Or paste the pairing link").font(.subheadline.weight(.semibold))
                        TextField("potluck://pair?…", text: $link, axis: .vertical)
                            .lineLimit(1...4)
                            .font(.footnote.monospaced())
                            .textInputAutocapitalization(.never)
                            .autocorrectionDisabled()
                            .padding(10)
                            .background(Color(.secondarySystemBackground), in: RoundedRectangle(cornerRadius: 10))
                            .accessibilityLabel("Pairing link")
                        HStack {
                            Button("Paste") { link = UIPasteboard.general.string ?? link }
                                .buttonStyle(.bordered)
                            Spacer()
                            Button {
                                submit(link)
                            } label: {
                                if working { ProgressView() } else { Text("Pair") }
                            }
                            .buttonStyle(.borderedProminent)
                            .disabled(working || link.trimmingCharacters(in: .whitespaces).isEmpty)
                        }
                    }

                    if let error { Notice(text: error, warn: true) }

                    Text("The phone talks only to your computer, encrypted, over your home network or Tailscale. Nothing goes through anyone else's servers, and your chats stay on this phone.")
                        .font(.footnote)
                        .foregroundStyle(.secondary)
                }
                .padding(20)
            }
            .sheet(isPresented: $scanning) {
                ScannerSheet { text in
                    scanning = false
                    submit(text)
                }
            }
        }
    }

    private func submit(_ text: String) {
        guard let code = PairingCode(link: text) else {
            error = "That is not a Potluck pairing code. It starts with potluck://pair."
            return
        }
        working = true
        error = nil
        Task {
            do {
                try await model.pair(code)
            } catch {
                self.error = error.localizedDescription
            }
            working = false
        }
    }
}

private struct Step: View {
    let n: Int
    let text: LocalizedStringKey
    var body: some View {
        HStack(alignment: .firstTextBaseline, spacing: 10) {
            Text("\(n)")
                .font(.footnote.bold())
                .frame(width: 22, height: 22)
                .background(Color.accentColor.opacity(0.15), in: Circle())
                .foregroundStyle(Color.accentColor)
            Text(text)
        }
    }
}

struct Notice: View {
    let text: String
    var warn = false
    var body: some View {
        Label(text, systemImage: warn ? "exclamationmark.triangle.fill" : "info.circle")
            .font(.subheadline)
            .padding(12)
            .frame(maxWidth: .infinity, alignment: .leading)
            .background((warn ? Color.orange : Color.blue).opacity(0.12), in: RoundedRectangle(cornerRadius: 12))
    }
}

struct LogoMark: View {
    var size: CGFloat = 28
    var body: some View {
        ZStack {
            Circle().fill(Color.accentColor)
            let d = size * 0.23
            Circle().fill(.white).frame(width: d, height: d).offset(y: -size * 0.11)
            Circle().fill(.white).frame(width: d, height: d).offset(x: -size * 0.14, y: size * 0.12)
            Circle().fill(.white).frame(width: d, height: d).offset(x: size * 0.14, y: size * 0.12)
        }
        .frame(width: size, height: size)
        .accessibilityHidden(true)
    }
}

/// The camera, looking for a QR code.
struct ScannerSheet: View {
    var found: (String) -> Void
    @Environment(\.dismiss) private var dismiss

    var body: some View {
        NavigationStack {
            Group {
                if DataScannerViewController.isSupported && DataScannerViewController.isAvailable {
                    QRScanner(found: found).ignoresSafeArea()
                } else {
                    ContentUnavailableView("No camera here", systemImage: "camera.fill",
                                           description: Text("Scanning needs the camera. Paste the pairing link instead: in Potluck's control panel it is under the code."))
                }
            }
            .navigationTitle("Scan the code")
            .navigationBarTitleDisplayMode(.inline)
            .toolbar { ToolbarItem(placement: .cancellationAction) { Button("Cancel") { dismiss() } } }
        }
    }
}

struct QRScanner: UIViewControllerRepresentable {
    var found: (String) -> Void

    func makeUIViewController(context: Context) -> DataScannerViewController {
        let vc = DataScannerViewController(recognizedDataTypes: [.barcode(symbologies: [.qr])], qualityLevel: .fast,
                                           isHighlightingEnabled: true)
        vc.delegate = context.coordinator
        try? vc.startScanning()
        return vc
    }

    func updateUIViewController(_ vc: DataScannerViewController, context: Context) {}

    func makeCoordinator() -> Coordinator { Coordinator(found: found) }

    final class Coordinator: NSObject, DataScannerViewControllerDelegate {
        let found: (String) -> Void
        var done = false
        init(found: @escaping (String) -> Void) { self.found = found }

        func dataScanner(_ s: DataScannerViewController, didAdd items: [RecognizedItem], allItems: [RecognizedItem]) {
            for item in items {
                if case .barcode(let b) = item, let v = b.payloadStringValue, v.hasPrefix("potluck://"), !done {
                    done = true
                    s.stopScanning()
                    UINotificationFeedbackGenerator().notificationOccurred(.success)
                    found(v)
                }
            }
        }
    }
}
