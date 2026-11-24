import SwiftUI

/// Enough Markdown for chat answers: headings, lists, quotes, code blocks with a copy
/// button, and inline bold, italics, code and links.
struct MarkdownView: View {
    let text: String

    var body: some View {
        VStack(alignment: .leading, spacing: 8) {
            ForEach(Array(Self.blocks(text).enumerated()), id: \.offset) { _, b in
                switch b {
                case .code(let lang, let code): CodeBlock(lang: lang, code: code)
                case .heading(let level, let t):
                    inline(t).font(level == 1 ? .title3.bold() : level == 2 ? .headline : .subheadline.bold())
                        .padding(.top, 4)
                case .bullet(let marker, let t, let indent):
                    HStack(alignment: .firstTextBaseline, spacing: 6) {
                        Text(marker).foregroundStyle(.secondary).monospacedDigit()
                        inline(t)
                    }
                    .padding(.leading, CGFloat(indent) * 14)
                case .quote(let t):
                    inline(t).foregroundStyle(.secondary)
                        .padding(.leading, 10)
                        .overlay(alignment: .leading) { Rectangle().fill(.tertiary).frame(width: 3) }
                case .rule: Divider()
                case .paragraph(let t): inline(t)
                }
            }
        }
        .textSelection(.enabled)
    }

    private func inline(_ s: String) -> Text {
        let opts = AttributedString.MarkdownParsingOptions(interpretedSyntax: .inlineOnlyPreservingWhitespace)
        if let a = try? AttributedString(markdown: s, options: opts) { return Text(a) }
        return Text(s)
    }

    enum Block {
        case code(String, String)
        case heading(Int, String)
        case bullet(String, String, Int)
        case quote(String)
        case rule
        case paragraph(String)
    }

    static func blocks(_ text: String) -> [Block] {
        var out: [Block] = []
        var para: [String] = []
        var code: [String]?
        var lang = ""
        func flush() {
            if !para.isEmpty { out.append(.paragraph(para.joined(separator: "\n"))); para = [] }
        }
        for raw in text.components(separatedBy: "\n") {
            let line = raw.trimmingCharacters(in: .whitespaces)
            if line.hasPrefix("```") {
                if let c = code {
                    out.append(.code(lang, c.joined(separator: "\n")))
                    code = nil
                } else {
                    flush()
                    code = []
                    lang = String(line.dropFirst(3)).trimmingCharacters(in: .whitespaces)
                }
                continue
            }
            if code != nil { code!.append(raw); continue }
            if line.isEmpty { flush(); continue }
            if let m = line.firstMatch(of: /^(#{1,6})\s+(.*)$/) {
                flush(); out.append(.heading(m.1.count, String(m.2))); continue
            }
            if line.firstMatch(of: /^(-{3,}|\*{3,}|_{3,})$/) != nil { flush(); out.append(.rule); continue }
            let indent = (raw.prefix { $0 == " " }.count) / 2
            if let m = line.firstMatch(of: /^[-*+]\s+(.*)$/) {
                flush(); out.append(.bullet("•", String(m.1), indent)); continue
            }
            if let m = line.firstMatch(of: /^(\d+)[.)]\s+(.*)$/) {
                flush(); out.append(.bullet(m.1 + ".", String(m.2), indent)); continue
            }
            if let m = line.firstMatch(of: /^>\s?(.*)$/) {
                flush(); out.append(.quote(String(m.1))); continue
            }
            para.append(line)
        }
        if let c = code { out.append(.code(lang, c.joined(separator: "\n"))) } // still being written
        flush()
        return out
    }
}

struct CodeBlock: View {
    let lang: String
    let code: String
    @State private var copied = false

    var body: some View {
        VStack(alignment: .leading, spacing: 0) {
            HStack {
                Text(lang.isEmpty ? "code" : lang).font(.caption).foregroundStyle(.secondary)
                Spacer()
                Button {
                    UIPasteboard.general.string = code
                    copied = true
                    Task { try? await Task.sleep(for: .seconds(1.5)); copied = false }
                } label: {
                    Label(copied ? "Copied" : "Copy", systemImage: copied ? "checkmark" : "doc.on.doc").font(.caption)
                }
                .buttonStyle(.borderless)
            }
            .padding(.horizontal, 10)
            .padding(.vertical, 6)
            ScrollView(.horizontal, showsIndicators: false) {
                Text(code).font(.system(.footnote, design: .monospaced)).padding(10)
            }
        }
        .background(Color(.secondarySystemBackground), in: RoundedRectangle(cornerRadius: 10))
    }
}
