"use strict";
// A small, safe Markdown renderer. Everything is escaped first; only the tags written here
// can reach the page, and links are limited to web and mail addresses.
const MD = (() => {
  const esc = s => String(s).replace(/[&<>"']/g, c => ({ "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#39;" }[c]));

  function inline(src) {
    const keep = [];
    const stash = html => { keep.push(html); return "\u0000" + (keep.length - 1) + "\u0000"; };
    let s = src.replace(/(`+)([\s\S]*?[^`])\1(?!`)/g, (_, t, code) => stash("<code>" + esc(code) + "</code>"));
    s = esc(s);
    s = s.replace(/!\[([^\]]*)\]\(([^)\s]+)\)/g, (_, alt, url) => stash("<span class=\"muted\">[image: " + alt + "]</span>"));
    s = s.replace(/\[([^\]]+)\]\(([^)\s]+)\)/g, (m, text, url) => {
      const u = url.replace(/&amp;/g, "&");
      if (!/^(https?:\/\/|mailto:)/i.test(u)) return text;
      return stash("<a href=\"" + esc(u) + "\" target=\"_blank\" rel=\"noopener noreferrer\">") + text + stash("</a>");
    });
    s = s.replace(/(^|[\s(])(https?:\/\/[^\s<)]+[^\s<).,;:!?])/g, (_, pre, u) => pre + stash("<a href=\"" + u + "\" target=\"_blank\" rel=\"noopener noreferrer\">" + u + "</a>"));
    s = s.replace(/\*\*([^*\n]+?)\*\*/g, "<strong>$1</strong>").replace(/__([^_\n]+?)__/g, "<strong>$1</strong>");
    s = s.replace(/(^|[^*\w])\*([^*\n]+?)\*(?!\w)/g, "$1<em>$2</em>").replace(/(^|[^_\w])_([^_\n]+?)_(?!\w)/g, "$1<em>$2</em>");
    s = s.replace(/~~([^~\n]+?)~~/g, "<del>$1</del>");
    return s.replace(/\u0000(\d+)\u0000/g, (_, i) => keep[+i]);
  }

  function table(lines) {
    const cells = l => l.trim().replace(/^\||\|$/g, "").split("|").map(c => c.trim());
    const head = cells(lines[0]), align = cells(lines[1]).map(c => /^:-+:$/.test(c) ? "center" : /-+:$/.test(c) ? "right" : "");
    const td = (c, i, tag) => "<" + tag + (align[i] ? " style=\"text-align:" + align[i] + "\"" : "") + ">" + inline(c) + "</" + tag + ">";
    let h = "<div class=\"tablewrap\"><table><thead><tr>" + head.map((c, i) => td(c, i, "th")).join("") + "</tr></thead><tbody>";
    for (const l of lines.slice(2)) h += "<tr>" + cells(l).map((c, i) => td(c, i, "td")).join("") + "</tr>";
    return h + "</tbody></table></div>";
  }

  // list renders consecutive list lines, nesting by indentation.
  function list(lines) {
    const items = lines.map(l => {
      const m = /^(\s*)([-*+]|\d+[.)])\s+(.*)$/.exec(l);
      return m ? { indent: m[1].replace(/\t/g, "    ").length, ordered: /\d/.test(m[2]), start: parseInt(m[2], 10) || 1, text: m[3] } : { cont: l.trim() };
    });
    let i = 0;
    const build = indent => {
      const first = items[i], tag = first.ordered ? "ol" : "ul";
      let h = "<" + tag + (first.ordered && first.start !== 1 ? " start=\"" + first.start + "\"" : "") + ">";
      while (i < items.length) {
        const it = items[i];
        if (it.cont !== undefined) { i++; continue; }
        if (it.indent < indent) break;
        if (it.indent > indent) { h = h.replace(/<\/li>$/, "") + build(it.indent) + "</li>"; continue; }
        let text = it.text; i++;
        while (i < items.length && items[i].cont !== undefined) { text += " " + items[i].cont; i++; }
        const task = /^\[( |x|X)\]\s+/.exec(text);
        if (task) text = "<input type=\"checkbox\" disabled" + (task[1] !== " " ? " checked" : "") + "> " + inline(text.slice(task[0].length)); else text = inline(text);
        h += "<li>" + text + "</li>";
      }
      return h + "</" + tag + ">";
    };
    return build(items[0].indent || 0);
  }

  function render(src) {
    const lines = String(src).replace(/\r\n?/g, "\n").split("\n");
    let out = "", i = 0, para = [];
    const flush = () => { if (para.length) { out += "<p>" + inline(para.join("\n")).replace(/\n/g, "<br>") + "</p>"; para = []; } };
    while (i < lines.length) {
      const l = lines[i];
      const fence = /^\s*(```+|~~~+)\s*([\w+#.-]*)\s*$/.exec(l);
      if (fence) {
        flush();
        const body = []; i++;
        while (i < lines.length && !new RegExp("^\\s*" + fence[1][0] + "{" + fence[1].length + ",}\\s*$").test(lines[i])) body.push(lines[i++]);
        i++; // closing fence (or the end, while the answer is still being written)
        const lang = fence[2].toLowerCase(), code = body.join("\n");
        const canPreview = lang === "html" || lang === "svg";
        out += "<div class=\"code\" data-lang=\"" + esc(lang) + "\"><div class=\"code-top\"><span>" + esc(lang || "text") + "</span><span class=\"grow\"></span>" +
          (canPreview ? "<button type=\"button\" data-a=\"preview\">Preview</button>" : "") +
          "<button type=\"button\" data-a=\"copy-code\">Copy</button></div><pre><code>" + esc(code) + "</code></pre></div>";
        continue;
      }
      if (/^\s*$/.test(l)) { flush(); i++; continue; }
      const h = /^(#{1,6})\s+(.*?)\s*#*\s*$/.exec(l);
      if (h) { flush(); const n = Math.min(h[1].length + 1, 6); out += "<h" + n + ">" + inline(h[2]) + "</h" + n + ">"; i++; continue; }
      if (/^\s*([-*_])(\s*\1){2,}\s*$/.test(l)) { flush(); out += "<hr>"; i++; continue; }
      if (/^\s*>/.test(l)) {
        flush(); const q = [];
        while (i < lines.length && /^\s*>/.test(lines[i])) q.push(lines[i++].replace(/^\s*>\s?/, ""));
        out += "<blockquote>" + render(q.join("\n")) + "</blockquote>"; continue;
      }
      if (/\|/.test(l) && i + 1 < lines.length && /^\s*\|?\s*:?-{2,}:?\s*(\|\s*:?-{2,}:?\s*)*\|?\s*$/.test(lines[i + 1])) {
        flush(); const t = [l, lines[i + 1]]; i += 2;
        while (i < lines.length && /\|/.test(lines[i]) && !/^\s*$/.test(lines[i])) t.push(lines[i++]);
        out += table(t); continue;
      }
      if (/^\s*([-*+]|\d+[.)])\s+\S/.test(l)) {
        flush(); const ls = [];
        while (i < lines.length && (/^\s*([-*+]|\d+[.)])\s+\S/.test(lines[i]) || (/^\s{2,}\S/.test(lines[i]) && ls.length))) ls.push(lines[i++]);
        out += list(ls); continue;
      }
      para.push(l); i++;
    }
    flush();
    return out;
  }
  return { render, esc, inline };
})();
