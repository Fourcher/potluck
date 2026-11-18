"use strict";
// Text out of Word, Excel, PowerPoint and PDF files, in the browser, with no libraries.
// Word, Excel and PowerPoint files are zip archives of XML; PDFs keep their text in
// compressed content streams. Both are unpacked with the browser's own DecompressionStream.
const Extract = (() => {
  const kindOf = name => { const m = /\.(docx|xlsx|pptx|pdf)$/i.exec(name); return m ? m[1].toLowerCase() : ""; };
  const latin1 = b => { let s = ""; for (let i = 0; i < b.length; i += 0x8000) s += String.fromCharCode.apply(null, b.subarray(i, i + 0x8000)); return s; };
  async function inflate(bytes, format) {
    const ds = new DecompressionStream(format), w = ds.writable.getWriter();
    w.write(bytes).catch(() => { }); w.close().catch(() => { });
    const out = [], r = ds.readable.getReader(); let n = 0;
    for (; ;) {
      let res; try { res = await r.read(); } catch (e) { if (n) break; throw e; } // keep what came out before a damaged end
      if (res.done) break;
      out.push(res.value); n += res.value.length;
      if (n > 64 << 20) throw new Error("This file unpacks to more than 64 MB.");
    }
    const all = new Uint8Array(n); let o = 0; for (const c of out) { all.set(c, o); o += c.length; }
    return all;
  }

  // ---------- zip ----------
  async function unzip(bytes) {
    const dv = new DataView(bytes.buffer, bytes.byteOffset, bytes.byteLength);
    let eocd = -1;
    for (let i = bytes.length - 22; i >= Math.max(0, bytes.length - 65557); i--) if (dv.getUint32(i, true) === 0x06054b50) { eocd = i; break; }
    if (eocd < 0) throw new Error("This file is damaged or is not really a Word, Excel or PowerPoint file.");
    const count = dv.getUint16(eocd + 10, true); let p = dv.getUint32(eocd + 16, true);
    const files = new Map(), dec = new TextDecoder();
    for (let k = 0; k < count && p + 46 <= bytes.length; k++) {
      if (dv.getUint32(p, true) !== 0x02014b50) break;
      const method = dv.getUint16(p + 10, true), csize = dv.getUint32(p + 20, true), nlen = dv.getUint16(p + 28, true), xlen = dv.getUint16(p + 30, true), clen = dv.getUint16(p + 32, true), off = dv.getUint32(p + 42, true);
      const name = dec.decode(bytes.subarray(p + 46, p + 46 + nlen));
      files.set(name, { method, csize, off });
      p += 46 + nlen + xlen + clen;
    }
    const read = async name => {
      const f = files.get(name); if (!f) return null;
      const start = f.off + 30 + dv.getUint16(f.off + 26, true) + dv.getUint16(f.off + 28, true), data = bytes.subarray(start, start + f.csize);
      if (f.method === 0) return dec.decode(data);
      if (f.method === 8) return dec.decode(await inflate(data, "deflate-raw"));
      throw new Error("This file uses a kind of compression that cannot be read here.");
    };
    return { names: [...files.keys()], read };
  }
  const unxml = s => s.replace(/<[^>]+>/g, "").replace(/&(lt|gt|quot|apos|amp|#(\d+)|#x([0-9a-f]+));/gi, (m, n, d, x) => d ? String.fromCodePoint(+d) : x ? String.fromCodePoint(parseInt(x, 16)) : { lt: "<", gt: ">", quot: '"', apos: "'", amp: "&" }[n.toLowerCase()]);
  const byNumber = (names, re) => names.filter(n => re.test(n)).sort((a, b) => +a.match(/(\d+)\.xml$/)[1] - +b.match(/(\d+)\.xml$/)[1]);
  const tidy = s => s.replace(/[ \t]+\n/g, "\n").replace(/\n{3,}/g, "\n\n").trim();

  async function docx(z) {
    const x = await z.read("word/document.xml"); if (x == null) throw new Error("This Word file has no document in it.");
    const body = x.replace(/<w:tab\/>/g, "\t").replace(/<w:(br|cr)\b[^>]*\/>/g, "\n").replace(/<\/w:p>/g, "\n").replace(/<\/w:tc>/g, "\t").replace(/<\/w:tr>/g, "\n")
      .replace(/<w:delText\b[^>]*>[\s\S]*?<\/w:delText>/g, "").replace(/<w:instrText\b[^>]*>[\s\S]*?<\/w:instrText>/g, "");
    return tidy(unxml(body));
  }
  async function xlsx(z) {
    const shared = [], ss = await z.read("xl/sharedStrings.xml");
    if (ss) for (const si of ss.match(/<si\b[\s\S]*?<\/si>/g) || []) shared.push(unxml((si.match(/<t\b[^>]*>[\s\S]*?<\/t>/g) || []).join("")));
    const wb = (await z.read("xl/workbook.xml")) || "", names = (wb.match(/<sheet\b[^>]*>/g) || []).map(s => unxml((/name="([^"]*)"/.exec(s) || [])[1] || ""));
    const col = r => { let n = 0; for (const ch of r.replace(/\d+/g, "")) n = n * 26 + ch.charCodeAt(0) - 64; return n - 1; };
    const csv = v => /[",\n]/.test(v) ? '"' + v.replace(/"/g, '""') + '"' : v;
    const out = [];
    for (const [k, path] of byNumber(z.names, /^xl\/worksheets\/sheet\d+\.xml$/).entries()) {
      const xml = await z.read(path), rows = [];
      for (const row of xml.match(/<row\b[\s\S]*?<\/row>/g) || []) {
        const cells = [];
        for (const c of row.match(/<c\b[^>]*?(?:\/>|>[\s\S]*?<\/c>)/g) || []) {
          const ref = (/\br="([A-Z]+)\d+"/.exec(c) || [])[1], t = (/\bt="(\w+)"/.exec(c) || [])[1];
          const v = (/<v>([\s\S]*?)<\/v>/.exec(c) || [])[1];
          let val = t === "s" ? shared[+v] ?? "" : t === "inlineStr" ? unxml((c.match(/<t\b[^>]*>[\s\S]*?<\/t>/g) || []).join("")) : t === "b" ? (v === "1" ? "TRUE" : "FALSE") : v != null ? unxml(v) : "";
          cells[ref ? col(ref) : cells.length] = csv(val);
        }
        if (cells.some(x => x)) rows.push(Array.from(cells, x => x || "").join(","));
      }
      if (rows.length) out.push(`Sheet: ${names[k] || "Sheet " + (k + 1)}\n` + rows.join("\n"));
    }
    return out.join("\n\n");
  }
  async function pptx(z) {
    const out = [];
    for (const [k, path] of byNumber(z.names, /^ppt\/slides\/slide\d+\.xml$/).entries()) {
      const xml = await z.read(path);
      const text = tidy(unxml(xml.replace(/<a:br\b[^>]*\/>/g, "\n").replace(/<\/a:p>/g, "\n").replace(/<a:t>/g, "<a:t>")));
      const notes = await z.read(path.replace("slides/slide", "notesSlides/notesSlide"));
      const n = notes ? tidy(unxml(notes.replace(/<\/a:p>/g, "\n"))).replace(/^\d+\s*$/m, "").trim() : "";
      if (text || n) out.push(`Slide ${k + 1}\n${text}${n ? "\nNotes: " + n : ""}`);
    }
    return out.join("\n\n");
  }

  // ---------- PDF ----------
  // Reads text drawn with ordinary fonts. PDFs that are scans, or whose fonts use their own
  // private encoding, come out empty or as gibberish; those are reported as unreadable.
  function pdfStrings(ops) {
    let out = "", line = "";
    const flush = () => { if (line.trim()) out += line.replace(/\s+$/, "") + "\n"; line = ""; };
    const lit = (s, i) => { // a (literal string) starting at s[i] === "("
      let depth = 1, r = ""; i++;
      while (i < s.length && depth) {
        const ch = s[i];
        if (ch === "\\") {
          const n = s[i + 1];
          if (/[0-7]/.test(n)) { const m = /^[0-7]{1,3}/.exec(s.slice(i + 1, i + 4))[0]; r += String.fromCharCode(parseInt(m, 8)); i += 1 + m.length; continue; }
          r += { n: "\n", r: "", t: "\t", b: "", f: "", "\n": "", "\r": "" }[n] ?? n; i += 2; continue;
        }
        if (ch === "(") depth++; else if (ch === ")") { depth--; if (!depth) break; }
        r += ch; i++;
      }
      return [r, i + 1];
    };
    const hex = (s, i) => { const j = s.indexOf(">", i); const h = s.slice(i + 1, j).replace(/\s/g, ""); let r = ""; for (let k = 0; k < h.length; k += 2) r += String.fromCharCode(parseInt((h.slice(k, k + 2) + "0").slice(0, 2), 16)); return [r, j + 1]; };
    let i = 0, inText = false, pend = [];
    while (i < ops.length) {
      const ch = ops[i];
      if (ch === "%") { while (i < ops.length && ops[i] !== "\n" && ops[i] !== "\r") i++; continue; }
      if (ch === "(") { const [r, j] = lit(ops, i); pend.push(r); i = j; continue; }
      if (ch === "<" && ops[i + 1] !== "<") { const [r, j] = hex(ops, i); pend.push(r); i = j; continue; }
      if (ch === "[") { // TJ array: strings and spacing numbers
        let j = i + 1, r = "";
        while (j < ops.length && ops[j] !== "]") {
          if (ops[j] === "(") { const [t, k] = lit(ops, j); r += t; j = k; }
          else if (ops[j] === "<") { const [t, k] = hex(ops, j); r += t; j = k; }
          else { const m = /^-?\d*\.?\d+/.exec(ops.slice(j, j + 12)); if (m) { if (+m[0] < -180) r += " "; j += m[0].length; } else j++; }
        }
        pend.push(r); i = j + 1; continue;
      }
      const m = /^[A-Za-z'"*]+/.exec(ops.slice(i, i + 4));
      if (m) {
        const op = m[0]; i += op.length;
        if (op === "BT") { inText = true; }
        else if (op === "ET") { inText = false; flush(); }
        else if (inText && (op === "Tj" || op === "TJ")) line += pend.join("");
        else if (inText && (op === "'" || op === '"')) { flush(); line += pend.join(""); }
        else if (inText && (op === "Td" || op === "TD" || op === "T*")) flush();
        else if (inText && op === "Tm") flush();
        pend = []; continue;
      }
      i++;
    }
    flush();
    return out;
  }
  async function pdf(bytes) {
    const s = latin1(bytes);
    if (!s.startsWith("%PDF")) throw new Error("This is not a PDF file.");
    if (/\/Encrypt\s/.test(s)) throw new Error("This PDF is password-protected or encrypted.");
    let text = "", re = /stream\r?\n/g, m;
    while ((m = re.exec(s))) {
      const start = m.index + m[0].length, objAt = s.lastIndexOf(" obj", m.index), dict = s.slice(objAt >= 0 && m.index - objAt < 8192 ? objAt : Math.max(0, m.index - 2048), m.index);
      const end = s.indexOf("endstream", start); if (end < 0) break;
      re.lastIndex = end;
      if (/\/(Subtype\s*\/(Image|Type1C|CIDFontType0C|OpenType)|Length[123]\b|Type\s*\/(XRef|ObjStm|Metadata))/.test(dict)) continue;
      let raw = bytes.subarray(start, end);
      const filters = (/\/Filter\s*(\[[^\]]*\]|\/\w+)/.exec(dict) || [])[1] || "";
      if (filters && !/^\/?FlateDecode$|^\[\s*\/FlateDecode\s*\]$/.test(filters.trim())) continue; // other encodings: images and the like
      if (filters) { try { raw = await inflate(raw, "deflate"); } catch { continue; } }
      const ops = latin1(raw);
      if (!/\bBT\b/.test(ops) || !/\bET\b/.test(ops)) continue;
      text += pdfStrings(ops) + "\n";
    }
    text = tidy(text);
    // Gibberish check: real text is mostly letters, digits, spaces and punctuation.
    const sample = text.slice(0, 4000), good = (sample.match(/[\p{L}\p{N}\s.,;:!?'"()\-–—%$€£/&@#+=*]/gu) || []).length;
    if (sample.length && good / sample.length < 0.85) return "";
    return text;
  }

  async function text(kind, bytes) {
    if (kind === "pdf") return pdf(bytes);
    const z = await unzip(bytes);
    return kind === "docx" ? docx(z) : kind === "xlsx" ? xlsx(z) : pptx(z);
  }
  return { kindOf, text };
})();
if (typeof module !== "undefined") module.exports = Extract;
