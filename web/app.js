"use strict";
const $ = (s, el = document) => el.querySelector(s);
const esc = s => String(s ?? "").replace(/[&<>"']/g, c => ({ "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#39;" }[c]));
const gb = b => (b >= 1073741824 ? (b / 1073741824).toFixed(1) + " GB" : Math.round((b || 0) / 1048576) + " MB");
const COLORS = ["var(--c0)", "var(--c1)", "var(--c2)", "var(--c3)", "var(--c4)", "var(--c5)"];

let S = null;                       // latest state from the app
let tab = location.hash.slice(1) || "computers";
let holdUntil = 0;                  // do not redraw while someone is dragging a slider or typing
let store = { source: "hf", q: "", results: null, loading: false, error: "", files: {}, open: {} };
let sheet = null;                   // plan preview
let kit = { drives: null, msg: "" };
let logText = "";
let disk = { data: null, loading: false, sums: {} };   // what Potluck keeps on each computer's disk
let found = { data: null, loading: false, deep: false, error: "" };   // models other apps keep on this computer

async function api(path, body) {
  const opt = body === undefined ? { headers: { "X-Potluck": "1" } } : { method: "POST", headers: { "Content-Type": "application/json", "X-Potluck": "1" }, body: JSON.stringify(body) };
  const r = await fetch(path, opt);
  const j = await r.json().catch(() => ({}));
  if (!r.ok) throw new Error(j.error || "Something went wrong.");
  return j;
}
let toastTimer;
function toast(msg) {
  const t = $("#toast"); t.textContent = msg; t.classList.add("show");
  clearTimeout(toastTimer); toastTimer = setTimeout(() => t.classList.remove("show"), 4200);
}
async function act(fn) { try { await fn(); holdUntil = 0; await refresh(); } catch (e) { toast(e.message); } }

function pedalWord(p) {
  return p >= 100 ? "Pedal to the metal" : p >= 75 ? "Working hard" : p >= 50 ? "Half throttle" : p >= 25 ? "Light touch" : p > 0 ? "Barely" : "Parked";
}

// ---------- pieces shared by the main computer and helpers ----------

function specs(i) {
  const gpus = (i.gpus || []).map(g => esc(g.name) + (g.vramTotal && !g.unified ? " " + gb(g.vramTotal) : "") + (g.usable ? "" : " (not used yet)")).join(", ") || "none";
  return `<ul class="specs">
    <li><b>${esc(i.osName || i.os)}</b></li>
    <li>${esc(i.cpu || "")}${i.cores ? " · " + i.cores + " cores" : ""}</li>
    <li>Memory <b>${gb(i.ramTotal)}</b>${i.ramAvail ? " · " + gb(i.ramAvail) + " free now" : ""}</li>
    <li>Graphics: ${gpus}</li>
    <li>Disk free: ${gb(i.diskFree)}</li></ul>`;
}

function limitsUI(id, L, isMain) {
  const p = L.pedal ?? 100;
  return `<div class="limits" data-id="${esc(id)}">
    <label class="sw"><input type="checkbox" data-c="enabled" ${L.enabled ? "checked" : ""}> ${isMain ? "Let this computer help run models too" : "Use this computer"}</label>
    <input type="range" min="0" max="100" step="5" value="${p}" data-c="pedal" aria-label="How hard this computer may work" ${L.enabled ? "" : "disabled"}>
    <div class="pedal"><span>Parked</span><b data-pedal>${p}% · ${pedalWord(p)}</b><span>Flat out</span></div>
    <div class="row small" style="margin-top:8px">
      <label class="sw"><input type="checkbox" data-c="schedOn" ${L.schedOn ? "checked" : ""}> Only between</label>
      <input type="time" data-c="schedStart" value="${esc(L.schedStart || "22:00")}" aria-label="Start time">
      <span>and</span>
      <input type="time" data-c="schedEnd" value="${esc(L.schedEnd || "07:00")}" aria-label="End time">
    </div></div>`;
}

function readLimits(box) {
  const v = n => $(`[data-c=${n}]`, box);
  return { enabled: v("enabled").checked, pedal: +v("pedal").value, schedOn: v("schedOn").checked, schedStart: v("schedStart").value, schedEnd: v("schedEnd").value };
}

function progress(msg, done, total) {
  if (!msg) return `<div class="status"></div>`;
  const pct = total > 0 ? Math.min(100, Math.round(done * 100 / total)) : -1;
  return `<div class="status">${esc(msg)}${pct >= 0 ? ` · ${pct}%${total > 1048576 ? " of " + gb(total) : ""}` : ""}${pct >= 0 ? `<div class="bar"><i style="width:${pct}%"></i></div>` : ""}</div>`;
}

// ---------- first run ----------

function viewChoose() {
  return `<h1>What is this computer's job?</h1>
  <p class="muted">You choose once. Everything Potluck stores stays in one folder that you can delete to remove it.</p>
  <div class="choose">
    <div class="card"><h3>Just this computer</h3>
      <p class="muted">Run models here, on this computer alone. Nothing else to set up. Models you already have from LM Studio, Ollama or Hugging Face are found and used where they are. You can add other computers later.</p>
      <p style="margin-top:16px"><button class="btn" data-a="role" data-role="single">Use this computer</button></p></div>
    <div class="card"><h3>This is my main computer</h3>
      <p class="muted">You sit at this one. It finds your other computers, lets you pick models, and shows the chat. It only lends its own muscle if you turn that on.</p>
      <p style="margin-top:16px"><button class="btn ghost" data-a="role" data-role="main">Make this my main computer</button></p></div>
    <div class="card"><h3>This is a spare computer</h3>
      <p class="muted">It lends its memory and processor to your main computer. You decide how much, and at what hours.</p>
      <p style="margin-top:16px"><button class="btn ghost" data-a="role" data-role="helper">Lend this computer</button></p></div>
  </div>
  <div class="card" style="margin-top:18px"><h3>This computer</h3>${specs(S.info || {})}</div>`;
}

// ---------- helper ----------

function viewHelper() {
  const h = S.helper, link = h.link, i = h.info, st = h.status || {};
  let top;
  if (link.phase === "pairing") {
    top = `<div class="card center"><h1>Check this number on your main computer</h1>
      <div class="bigcode">${esc(link.code)}</div>
      <p class="muted">If your main computer shows the same number next to "${esc(i.name)}", click <b>Add it</b> there.</p></div>`;
  } else if (link.phase === "connected") {
    top = `<div class="card"><div class="row"><span class="dot on"></span><h1 style="margin:0">Lending to ${esc(link.mainName || "your main computer")}</h1></div>
      <p class="muted">You can close this page. Potluck keeps running until you close its window.</p>
      ${progress(st.msg, st.done, st.total)}</div>`;
  } else {
    top = `<div class="card"><h1>Looking for your main computer…</h1>
      <p class="muted">${esc(link.note || "")} Both computers need to be on the same network, with Potluck open on the main one.</p>
      <div class="row" style="margin-top:12px"><input type="text" id="manual" placeholder="Main computer's address, e.g. 192.168.1.20" style="flex:1;min-width:220px" aria-label="Main computer address">
      <button class="btn quiet" data-a="manual">Connect</button></div>
      <p class="small muted">Only needed if it is not found by itself. The address is shown on the main computer under "Add a computer".</p></div>`;
  }
  return `${top}
    <h2>How hard may this computer work?</h2>
    <div class="grid"><div class="card">${limitsUI(S.id, i.limits, false)}</div>
    <div class="card"><div class="row spread"><h3>${esc(i.name)}</h3><button class="link" data-a="rename" data-id="${esc(S.id)}" data-name="${esc(i.name)}">Rename</button></div>${specs(i)}
      ${link.mainName ? `<p style="margin-top:12px"><button class="link" data-a="leave">Stop lending to ${esc(link.mainName)}</button></p>` : ""}</div></div>`;
}

// ---------- main computer: computers ----------

// profileLine shows a computer's measured speed, and the button to measure it.
function profileLine(n) {
  if (!n.online) return "";
  const p = (S.main.profiles || {})[n.info.id], busy = n.role && S.main.cluster.phase !== "idle";
  const stale = p && p.signature && p.signature !== [n.info.cpu, n.info.threads, Math.floor((n.info.ramTotal || 0) / 1073741824), (n.info.gpus || []).filter(g => g.usable).map(g => g.name)[0] || ""].join("|");
  const what = p ? `Reads memory at <b>${p.memGBs} GB/s</b>${p.diskMBs ? `, its drive at <b>${Math.round(p.diskMBs)} MB/s</b>` : ""}. <span class="muted">Measured ${new Date(p.when).toLocaleDateString()}${stale ? ", before its hardware changed" : ""}.</span>`
    : `<span class="muted">Not measured yet. Measuring takes about ten seconds and lets Potluck estimate how fast each model will run here.</span>`;
  const btn = busy ? "" : ` <button class="link" data-a="bench" data-id="${esc(n.info.id)}">${p ? "Measure again" : "Measure this computer"}</button>`;
  return `<div class="small" style="margin-top:6px">${benching[n.info.id] ? "Measuring…" : what + btn}</div>`;
}
const benching = {};

function nodeCard(n) {
  const i = n.info, L = i.limits || {};
  const badge = n.role === "head" ? `<span class="badge ok">Runs the model</span>` : n.role === "worker" ? `<span class="badge ok">Holds part of the model</span>` : "";
  const lend = !n.online ? `<span class="muted">Not connected</span>` : !n.active ? `<span class="muted">${L.enabled ? "Resting (outside its hours)" : "Not lending"}</span>` : `Lends <b>${gb(n.offer)}</b> of memory, ${Math.max(1, Math.round((i.perfThreads || i.cores || 1) * (L.pedal || 0) / 100))} of ${i.perfThreads || i.cores || "?"} ${i.perfThreads ? "fast-core threads" : "cores"}`;
  const slow = !n.isMain && n.online && n.linkMs > 30 ? `<div class="note small">Slow connection to this computer: about ${Math.round(n.linkMs)} ms per round trip. A network cable, or a spot nearer the router, would speed it up.</div>` : "";
  return `<div class="card ${n.online ? "" : "off"}" data-node="${esc(i.id)}">
    <div class="row spread"><div class="row"><span class="dot ${n.online ? "on" : ""}" title="${n.online ? "Connected" : "Not connected"}"></span><h3>${esc(i.name)}</h3></div>
      <div class="row">${n.isMain ? `<span class="badge main">Main computer</span>` : ""}${badge}</div></div>
    ${specs(i)}
    <div class="lend">${lend}</div>${slow}${profileLine(n)}
    ${limitsUI(i.id, L, n.isMain)}
    ${progress(n.status, n.done, n.total)}
    <div class="row small" style="margin-top:6px"><button class="link" data-a="rename" data-id="${esc(i.id)}" data-name="${esc(i.name)}">Rename</button>
      ${n.isMain ? "" : `<button class="link" data-a="forget" data-id="${esc(i.id)}" data-name="${esc(i.name)}">Remove</button>`}</div>
  </div>`;
}

function viewComputers() {
  const m = S.main, ready = m.nodes.filter(n => n.active).length;
  const pend = m.pending.map(p => `<div class="banner"><div class="row spread">
      <div><b>${esc(p.name)}</b> (${esc({ darwin: "Mac", windows: "Windows", linux: "Linux" }[p.os] || p.os)}) wants to join. Its screen should show <span class="code">${esc(p.code)}</span></div>
      <div class="row"><button class="btn" data-a="pending" data-id="${esc(p.id)}" data-ok="1">Add it</button>
      <button class="btn ghost" data-a="pending" data-id="${esc(p.id)}" data-ok="">Not mine</button></div></div></div>`).join("");
  if (!kit.drives) { kit.drives = []; api("/api/kit").then(r => { kit.drives = r.drives || []; draw(); }).catch(() => {}); }
  const fw = m.firewall === "blocked" ? `<div class="banner"><div class="row spread"><div><b>This computer's firewall is keeping your other computers out.</b><br><span class="small">Allow Potluck once and they can connect. Your computer will ask for your password.</span></div>
      <button class="btn" data-a="firewall">Allow Potluck</button></div></div>` : "";
  return `${fw}${pend}
    <h1>Your computers</h1>
    <div class="summary">
      <div><b>${ready}</b><span>${ready === 1 ? "computer" : "computers"} ready to work</span></div>
      <div><b>${gb(m.totalOffer)}</b><span>memory pooled together</span></div>
      <div><b>${gb(m.biggestOffer)}</b><span>the most any one of them has alone</span></div>
    </div>
    <div class="grid">${m.nodes.map(nodeCard).join("")}</div>
    <p class="small muted" style="margin-top:14px"><label>The model file lives on
      <select id="headchoice" data-head><option value="">whichever computer lends the most (automatic)</option>${m.nodes.map(n => `<option value="${esc(n.info.id)}" ${m.headChoice === n.info.id ? "selected" : ""}>${esc(n.info.name)}</option>`).join("")}</select></label>
      That computer downloads the whole model and runs it; the others hold their share in memory.</p>
    <h2>Add a computer</h2>
    <div class="how">
      <div class="card"><h3>On the same network</h3>
        <p class="muted small">On the other computer, open a web browser and type this address. One download, one double-click, and it appears here.</p>
        <p class="addr">${esc(m.joinURL ? m.joinURL.replace("http://", "") : "not connected to a network")}</p>
        <p class="muted small">Already have Potluck on it? Just open it and choose "spare computer". It finds this one by itself and asks you to confirm a 4-digit number.</p></div>
      <div class="card"><h3>With a USB stick</h3>
        <p class="muted small">Makes a folder on the stick with everything needed. Plug it into the spare computer and double-click "Join". No typing, no account, no downloads.</p>
        <div class="row"><select id="kitdrive" aria-label="Drive">${(kit.drives || []).map(d => `<option>${esc(d)}</option>`).join("")}<option value="">Another folder…</option></select>
        <button class="btn" data-a="kit">Make setup kit</button></div>
        <p class="small" id="kitmsg">${esc(kit.msg)}</p></div>
    </div>
    ${phonesView()}
    ${contView()}`;
}

// ---------- phones ----------
let phones = { list: null, code: null, error: "" };
function loadPhones() { api("/api/phones").then(r => { phones.list = r.phones || []; phones.push = r.push; if (!phones.list.some(p => p.pairing)) phones.code = null; draw(true); }).catch(e => { phones.error = e.message; draw(true); }); }
function phonesView() {
  if (!phones.list || Date.now() - (phones.at || 0) > 15000) { phones.at = Date.now(); phones.list = phones.list || []; loadPhones(); }
  const paired = phones.list.filter(p => !p.pairing);
  const ago = t => { if (!t) return "not connected yet"; const s = (Date.now() - new Date(t)) / 1000; return s < 90 ? "connected just now" : s < 3600 ? `last connected ${Math.round(s / 60)} minutes ago` : `last connected ${new Date(t).toLocaleString()}`; };
  const rows = paired.map(p => `<div class="file"><span class="q">${esc(p.name)}</span><span></span><span class="n muted small">Paired ${new Date(p.added).toLocaleDateString()} · ${ago(p.seen)}</span>
    <span class="row"><button class="btn sm quiet" data-a="phoneforget" data-id="${esc(p.id)}">Remove</button></span></div>`).join("");
  // One QR at a time: first the app's App Store page, then (once they have the app) the pairing code.
  const c = phones.code;
  const step = phones.step || "app";
  const code = !c ? "" : step === "app" && c.appQR ? `<div class="row" style="gap:18px;align-items:flex-start;margin-top:10px"><img src="${c.appQR}" alt="QR code for the Potluck app on the App Store" width="220" height="220" style="image-rendering:pixelated;background:#fff;border-radius:8px;padding:6px">
      <div class="small"><p><b>Step 1: get the Potluck app.</b> Point your iPhone's camera at this code to open the Potluck app on the App Store, then install it.</p>
      <p class="muted" style="margin-top:6px">Or search the App Store for "Potluck: Private Home AI", or open <a href="${esc(c.appURL)}" target="_blank" rel="noopener">${esc(c.appURL)}</a> on the phone.</p>
      <p style="margin-top:10px"><button class="btn sm" data-a="phonestep" data-step="pair">I have the app: show the pairing code</button> <button class="btn sm quiet" data-a="phonestop">Cancel</button></p></div></div>`
    : `<div class="row" style="gap:18px;align-items:flex-start;margin-top:10px"><img src="${c.qr}" alt="Pairing code" width="220" height="220" style="image-rendering:pixelated;background:#fff;border-radius:8px;padding:6px">
      <div class="small"><p><b>Step 2: pair.</b> In the Potluck app on your iPhone, tap "Scan the pairing code" and point the camera at this code.</p>
      <p class="muted" style="margin-top:6px">It works once, for ${c.minutes} minutes. The phone will reach this computer at ${c.addrs.map(esc).join(" or ")}${c.addrs.some(a => a.startsWith("100.")) ? " (the 100. address works away from home through Tailscale)" : ""}.</p>
      <p class="muted" style="margin-top:6px">No camera handy? Copy this link into the app instead:</p>
      <p><input type="text" readonly value="${esc(c.link)}" style="width:100%;min-width:260px" onclick="this.select()" aria-label="Pairing link"></p>
      <p style="margin-top:8px">${c.appQR ? `<button class="btn sm quiet" data-a="phonestep" data-step="app">Don't have the app yet?</button> ` : ""}<button class="btn sm quiet" data-a="phonestop">Hide the code</button></p></div></div>`;
  return `<h2>Your iPhone</h2><div class="card">
    <p class="muted small">Chat with your model from the Potluck iPhone app, at home or, with Tailscale, anywhere. The phone can chat, see what is running and switch models. It cannot run commands on this computer or change settings. The connection is encrypted and only phones you pair here get in.</p>
    ${phones.error ? `<div class="err">${esc(phones.error)}</div>` : ""}
    ${rows ? `<div class="files" style="margin-top:8px">${rows}</div>` : ""}
    ${code || `<p style="margin-top:10px"><button class="btn sm" data-a="phonepair">${paired.length ? "Pair another phone" : "Pair a phone"}</button></p>`}
    ${paired.length ? pushView() : ""}
  </div>`;
}


// ---------- bringing an existing AI setup over (continuity.go) ----------

let cont = { data: null, at: 0, found: null, pick: {}, busy: "", msg: "", err: "", showPrompt: false, folders: null };
function loadCont() { return api("/api/continuity").then(d => { cont.data = d; cont.at = Date.now(); if (!cont.folders) cont.folders = (d.folders || []).join("\n"); draw(); }).catch(e => { cont.err = e.message; draw(); }); }
const plural = (n, one, many) => `${n} ${n === 1 ? one : many}`;
function contCounts(s) {
  const parts = [];
  if (s.profile) parts.push(plural(s.profile, "instruction file", "instruction files"));
  if (s.memories) parts.push(plural(s.memories, "memory", "memories"));
  if (s.skills) parts.push(plural(s.skills, "skill", "skills"));
  if (s.projects) parts.push(plural(s.projects, "project", "projects"));
  if (s.connectors) parts.push(plural(s.connectors, "connector", "connectors"));
  if (s.rules) parts.push(plural(s.rules, "permission rule", "permission rules"));
  return parts.join(", ") || "nothing";
}
function contView() {
  if (!cont.data && Date.now() - cont.at > 10000) { cont.at = Date.now(); loadCont(); }
  const d = cont.data || {}, have = d.have || {}, srcs = have.sources || [];
  const haveRows = srcs.map(s => `<div class="file"><span class="q">${esc(s.name)}</span><span></span><span class="n muted small">${esc(contCounts(s))} · brought over ${new Date(s.imported).toLocaleString()}</span><span></span></div>`).join("");
  const found = cont.found ? (cont.found.length ? `<div class="files" style="margin-top:8px">${cont.found.map(s => `<label class="file" style="cursor:pointer"><span class="q"><input type="checkbox" data-contpick="${esc(s.id)}" ${cont.pick[s.id] !== false ? "checked" : ""}> ${esc(s.name)}</span><span></span><span class="n muted small">${esc(contCounts(s))}${s.path ? " · " + esc(s.path) : ""}</span><span></span></label>`).join("")}</div>
      <p style="margin-top:8px"><button class="btn sm" data-a="contimport" ${cont.busy ? "disabled" : ""}>Bring these over</button></p>` : `<p class="muted small" style="margin-top:8px">No Claude Code, Claude Desktop, Codex or Cursor setup and no projects found. Add the folder where you keep your projects above, or use your AI's help below.</p>`) : "";
  return `<h2>Bring your AI setup over</h2><div class="card">
    <p class="small">Start where you left off: Potluck brings over your instructions, memories, skills, projects and connectors from the AI you use now, and its chat and agent use them from then on.</p>
    ${srcs.length ? `<div class="files" style="margin-top:8px">${haveRows}</div><p class="muted small" style="margin-top:6px">${plural((have.projects || []).length, "project", "projects")} and ${plural((have.skills || []).length, "skill", "skills")} ready. <button class="link" data-a="contforget">Forget everything brought over</button></p>` : ""}
    ${cont.err ? `<div class="err">${esc(cont.err)}</div>` : ""}${cont.msg ? `<div class="note">${esc(cont.msg)}</div>` : ""}
    <h3 style="margin-top:14px">1. Let Potluck look on this computer</h3>
    <p class="muted small">It reads Claude Code, Claude Desktop, Codex and Cursor's own files. Nothing leaves this computer. It also finds projects in the folders below (one per line): folders with a CLAUDE.md, AGENTS.md or a git repository.</p>
    <textarea data-contfolders rows="3" style="width:100%;margin-top:6px;font:12px/1.4 ui-monospace,Menlo,monospace" aria-label="Folders with projects">${esc(cont.folders || "")}</textarea>
    <p style="margin-top:6px"><button class="btn sm" data-a="contscan" ${cont.busy ? "disabled" : ""}>${cont.busy === "scan" ? "Looking…" : "Look for my setup"}</button></p>
    ${found}
    <h3 style="margin-top:14px">2. Bring over what your AI knows</h3>
    <p class="muted small">Some things live only in your AI's account (ChatGPT's memory, for example). Copy this prompt into a chat with your AI: it gathers what it knows about you and your work and hands it to Potluck, or gives you something to paste below.</p>
    <p style="margin-top:6px"><button class="btn sm" data-a="contcopy">${cont.copied ? "Copied ✓" : "Copy the prompt"}</button> <button class="btn sm ghost" data-a="contshow">${cont.showPrompt ? "Hide it" : "Show it"}</button></p>
    ${cont.showPrompt ? `<textarea readonly rows="14" onclick="this.select()" style="width:100%;margin-top:8px;font:12px/1.45 ui-monospace,Menlo,monospace" aria-label="Prompt for your AI">${esc(d.prompt || "")}</textarea>` : ""}
    <p class="muted small" style="margin-top:10px">Paste what your AI wrote:</p>
    <textarea data-contpaste rows="4" style="width:100%;margin-top:4px;font:12px/1.4 ui-monospace,Menlo,monospace" placeholder='{"potluckImport": 1, ...}' aria-label="What your AI wrote">${esc(cont.paste || "")}</textarea>
    <p style="margin-top:6px"><button class="btn sm" data-a="contpaste" ${cont.busy ? "disabled" : ""}>Bring it over</button></p>
  </div>`;
}

// Alerts with answer buttons: the phone hears about every question a task asks (apns.go).
function pushView() {
  const p = phones.push || {};
  return `<h3 style="margin-top:16px">Alerts on your phone</h3>
    <p class="muted small">When a task needs an answer, Potluck alerts your phone, and you can answer Yes, No or reply from the alert itself. Apple only delivers alerts signed with a push key from the developer account the app was built with: in your Apple developer account, open Certificates, Identifiers &amp; Profiles, then Keys, make a key with "Apple Push Notifications service", and download its .p8 file.</p>
    ${p.error ? `<div class="err">${esc(p.error)}</div>` : ""}
    ${p.set ? `<p class="small" style="margin-top:6px">Alerts are on (key ${esc(p.keyID)}, team ${esc(p.teamID)}). <button class="link" data-a="pushtest">Send a test alert</button> · <button class="link" data-a="pushoff">Turn alerts off</button>${p.testAnswer ? `<br>Your phone answered the test: <b>${esc(p.testAnswer)}</b>` : ""}${p.error ? `<br><span style="color:var(--warn)">${esc(p.error)}</span>` : ""}</p>` : `
    <div class="row small" style="margin-top:8px;gap:8px;flex-wrap:wrap">
      <input data-push="keyPath" placeholder="Full path of the .p8 file" style="flex:2;min-width:240px">
      <input data-push="keyID" placeholder="Key ID (10 letters)" style="flex:1;min-width:120px">
      <input data-push="teamID" placeholder="Team ID" style="flex:1;min-width:110px">
      <button class="btn sm" data-a="pushon">Turn alerts on</button>
    </div>`}`;
}

// ---------- main computer: models ----------

// speedLine: how fast this file is expected to write here, or how fast it did.
function speedLine(f) {
  if (f.measured) return `<br><span class="small"><b>${f.measured.gen} words/s</b> measured on ${esc(f.measured.layout)}</span>`;
  const est = (f.estimates || []).filter(e => e.high > 0), none = (f.estimates || []).find(e => !e.high);
  if (!est.length) return none ? `<br><span class="small muted">${esc(none.basis)}</span>` : "";
  const label = { one: "on one computer", pool: "pooled", stream: "streamed from disk" };
  return `<br><span class="small">Estimate: ${est.map(e => `<b>${e.low}–${e.high} words/s</b> ${label[e.route]}${e.machine ? " (" + esc(e.machine) + ")" : ""}`).join(", ")}</span>`;
}

function verdictBadge(f) {
  const map = { one: ["ok", "Runs on one computer"], pool: ["main", "Runs pooled"], stream: ["warn", "Streams from disk"], no: ["bad", "Too big for now"], split: ["warn", "Not supported yet"] };
  const [cls, text] = map[f.verdict] || ["", ""];
  return `<span class="badge ${cls}" title="${esc(f.note)}">${text}</span>`;
}

function modelCard(mod) {
  if (mod.source === "pirateface") {
    return `<div class="model"><div class="row spread"><h3>${esc(mod.repo)}</h3><div class="row"><span class="badge">${esc(mod.sizeNote)}</span><span class="badge ${mod.seeds > 2 ? "ok" : "warn"}">${mod.seeds} sharing</span></div></div>
      <div class="row" style="margin-top:10px"><button class="btn sm quiet" data-a="magnet" data-magnet="${esc(mod.magnet)}">Copy torrent link</button>
      <button class="btn sm ghost" data-a="tohf" data-repo="${esc(mod.repo)}">Find it on Hugging Face</button></div></div>`;
  }
  if (mod.files && !store.files[mod.repo]) store.files[mod.repo] = mod.files;
  const files = store.files[mod.repo];
  const open = !!store.open[mod.repo];
  const runs = f => f.verdict === "one" || f.verdict === "pool" || f.verdict === "stream";
  // The best pick is the biggest size that runs here, leaving out the uncompressed originals.
  // Prefer a size that fits in memory; streaming from disk only when nothing else runs.
  const pick = v => files ? files.filter(f => f.verdict === v && !/^(BF16|F16|F32)$/.test(f.quant)).slice(-1)[0] : null;
  const best = pick("one") || pick("pool") || pick("stream");
  const rows = files && open ? `<div class="files">${files.map(f => `<div class="file">
      <span class="q">${esc(f.quant)}${best && f.file === best.file ? ` <span class="badge main">Best fit</span>` : ""}</span><span>${gb(f.size)}</span><span class="n">${verdictBadge(f)} <span class="muted small">${esc(f.note)}</span>${speedLine(f)}</span>
      <button class="btn sm ${runs(f) ? "" : "quiet"}" data-a="plan" data-repo="${esc(mod.repo)}" data-file="${esc(f.file)}" ${f.verdict === "split" ? "disabled" : ""}>${f.verdict === "no" ? "Why not?" : "Set up"}</button>
    </div>`).join("")}</div>` : "";
  const fit = files ? (() => { const n = files.filter(runs).length; return n ? `<span class="badge ${best && best.verdict === "pool" ? "main" : best && best.verdict === "stream" ? "warn" : "ok"}">${best && best.verdict === "pool" ? "Runs pooled across your computers" : best && best.verdict === "stream" ? "Only by streaming from disk (slow)" : "Runs here"}</span>` : `<span class="badge bad">Too big for your setup</span>`; })() : "";
  return `<div class="model${mod.recommended ? " recommended" : ""}"><div class="row spread"><div><h3>${mod.recommended ? `<span class="badge main" title="Potluck's recommended model to start with">★ Recommended</span> ` : ""}${esc(mod.repo)}</h3>${mod.blurb ? `<p class="muted small">${esc(mod.blurb)}</p>` : ""}
      ${mod.goodFor && mod.goodFor.length ? `<p class="small">${mod.goodFor.map(g => `<span class="badge">${esc(g)}</span>`).join(" ")}</p>` : ""}
      ${mod.downloads || mod.likes ? `<p class="muted small">${mod.downloads ? mod.downloads.toLocaleString() + " downloads" : ""}${mod.likes ? " · " + mod.likes.toLocaleString() + " likes" : ""}${mod.created ? " · released " + esc(mod.created) : ""} · <a href="https://huggingface.co/${esc(mod.repo)}" target="_blank" rel="noopener">model page</a></p>` : ""}
      ${mod.sources && mod.sources.length ? `<p class="muted small">${mod.sources.map(u => `<a href="${esc(u)}" target="_blank" rel="noopener">${esc(u.replace(/^https?:\/\//, "").split("/").slice(0, 2).join("/"))}</a>`).join(" · ")}</p>` : ""}</div>
      <div class="row">${fit}${best ? `<button class="btn sm" data-a="plan" data-repo="${esc(mod.repo)}" data-file="${esc(best.file)}">Set up best fit (${gb(best.size)})</button>` : ""}
      <button class="btn sm ghost" data-a="files" data-repo="${esc(mod.repo)}">${open ? "Hide sizes" : files ? `All ${files.length} sizes` : "Show sizes"}</button></div></div>${rows}</div>`;
}

// The starting shelf comes in sections (from the daily catalog); search results don't.
function shelf(list) {
  if (!list.some(m => m.section)) return list.map(modelCard).join("");
  const parts = [
    ["pick", "Our picks", "Chosen for quality and how well they run at home. Every size is checked against your computers."],
    ["trending", "Trending on Hugging Face", "What people are downloading right now. Open one to see which sizes fit."],
    ["new", "New this month", "Released in the last 30 days."],
    ["popular", "Most downloaded", "The most downloaded models of all time."],
  ];
  return parts.map(([key, title, note]) => {
    const ms = list.filter(m => m.section === key);
    return ms.length ? `<h3 style="margin-top:22px">${title}</h3><p class="muted small">${note}</p>${ms.map(modelCard).join("")}` : "";
  }).join("");
}

// Ranked lists other people made (catalog/lists.json), with how old each one is.
let lists = null, listsLoading = false;
function listsView() {
  if (lists === null) {
    if (!listsLoading) { listsLoading = true; api("/api/lists").then(r => { lists = r || []; }).catch(() => { lists = []; }).finally(() => { listsLoading = false; draw(true); }); }
    return "";
  }
  if (!lists.length) return "";
  const day = 864e5, today = Date.now();
  return `<h2>Ranked lists</h2><p class="muted small">Rankings other people made, with their age: models move fast. Potluck has not measured these; open one to check its sizes against your computers.</p>` + lists.map(l => {
    const age = Math.max(0, Math.floor((today - Date.parse(l.added)) / day));
    const old = l.expires && today > Date.parse(l.expires) + day;
    const ageText = age === 0 ? "added today" : age === 1 ? "added 1 day ago" : `added ${age} days ago`;
    const tiers = (l.tiers || []).map(t => `<div class="tier"><span class="tier-name tier-${esc(t.tier)}">${esc(t.tier)}</span><div class="tier-items">${(t.items || []).map(i => {
      const open = i.repo || i.search;
      return `<div class="tier-item">${open ? `<button class="btn sm ghost" data-a="listopen" data-q="${esc(i.repo || i.search)}">${esc(i.name)}</button>` : `<b class="small">${esc(i.name)}</b>`}
        <span class="muted small">${i.maker ? esc(i.maker) + ". " : ""}${esc(i.note || "")}</span></div>`;
    }).join("")}</div></div>`).join("");
    return `<details class="model"${old ? "" : " open"}><summary><b>${esc(l.title)}</b> <span class="muted small">${esc(l.by)}, ${esc(l.asOf)}</span>
        <span class="badge ${old ? "bad" : age > 30 ? "warn" : ""}">${old ? "Out of date: " : ""}${ageText}</span></summary>
      <p class="muted small" style="margin:8px 0">${esc(l.about)}</p>${tiers}</details>`;
  }).join("");
}

// The person's other AI setups (connections.go).
let conns = null, connsLoading = false;
function loadConns() {
  if (connsLoading) return; connsLoading = true;
  api("/api/connections").then(r => { conns = r || { connections: [], found: [] }; }).catch(e => { conns = { connections: [], found: [], error: e.message }; }).finally(() => { connsLoading = false; draw(true); });
}
function connsView() {
  if (conns === null) { loadConns(); return `<h2>Your other AI setups</h2><p class="muted">Looking…</p>`; }
  const list = (conns.connections || []).map(c => `<div class="file"><span class="q">${esc(c.name)}</span><span class="small muted">${esc(c.baseURL)}${c.hasKey ? " · with key" : ""}</span>
      <span class="n small">${c.error ? `<span class="badge bad">Not answering</span> <span class="muted">${esc(c.error)}</span>` : `${(c.models || []).length} models: ${esc((c.models || []).slice(0, 4).join(", "))}${(c.models || []).length > 4 ? "…" : ""}`}</span>
      <button class="btn sm quiet" data-a="connrm" data-id="${esc(c.id)}">Remove</button></div>`).join("");
  const found = (conns.found || []).map(f => `<p class="small">Found <b>${esc(f.name)}</b> running on this computer. <button class="btn sm" data-a="connadd" data-name="${esc(f.name)}" data-url="${esc(f.baseURL)}">Use its models in Potluck</button></p>`).join("");
  return `<h2>Your other AI setups</h2>
    <p class="muted">Use models from LM Studio, Ollama, a router (LiteLLM, OpenRouter) or any OpenAI-compatible server in Potluck's chat, next to your own. Chats go straight to that server.</p>
    ${conns.error ? `<div class="err">${esc(conns.error)}</div>` : ""}${found}
    ${list ? `<div class="files">${list}</div>` : ""}
    <div class="tools" style="margin-top:10px"><input type="text" id="conn-name" placeholder="Name, e.g. OpenRouter" style="width:160px" aria-label="Name">
      <input type="text" id="conn-url" placeholder="Address, e.g. http://192.168.1.20:1234/v1" style="flex:1;min-width:240px" aria-label="Server address">
      <input type="password" id="conn-key" placeholder="Key (if it needs one)" style="width:180px" aria-label="Key" autocomplete="off">
      <button class="btn quiet" data-a="connadd">Add</button></div>`;
}

function viewModels() {
  if (store.results === null && !store.loading) searchStore();
  const pf = store.source === "pirateface";
  return `<h1>Models</h1>
    ${foundView()}
    ${connsView()}
    <h2>Get a model</h2>
    <p class="muted">Every size is checked against the computers you have connected right now.</p>
    <div class="tools">
      <div class="seg"><button class="${pf ? "" : "on"}" data-a="source" data-source="hf">Hugging Face</button><button class="${pf ? "on" : ""}" data-a="source" data-source="pirateface">PirateFace</button></div>
      <input type="search" id="q" placeholder="Search models, e.g. qwen 27b" value="${esc(store.q)}" aria-label="Search models">
      <button class="btn" data-a="search">Search</button>
    </div>
    ${pf ? `<div class="note">PirateFace lists community torrents of Hugging Face models, for when Hugging Face is unreachable. Potluck only reads the list. Download with your own torrent app, then compare the file's SHA-256 with the one Hugging Face publishes before you trust it. Downloading torrents inside Potluck is planned.</div>` : ""}
    ${store.error ? `<div class="err">${esc(store.error)}</div>` : ""}
    ${store.loading ? `<p class="muted">Searching…</p>` : shelf(store.results || []) || `<p class="muted">Nothing found.</p>`}
    ${pf || store.q ? "" : listsView()}
    ${pf ? "" : diskView()}
    ${pf ? "" : localSettings()}
    ${pf ? "" : `<h2>Have a direct link?</h2><div class="tools"><input type="text" id="url" placeholder="https://…/model.gguf" style="flex:1;min-width:260px" aria-label="Direct link to a model file"><button class="btn quiet" data-a="planurl">Check it</button></div>`}`;
}

function loadDisk() {
  disk.loading = true;
  api("/api/storage").then(r => { disk.data = r; disk.loading = false; draw(); }).catch(() => { disk.loading = false; });
}

function diskView() {
  if (!disk.data && !disk.loading) loadDisk();
  if (!disk.data) return "";
  const rows = S.main.nodes.filter(n => disk.data[n.info.id]).map(n => {
    const d = disk.data[n.info.id], id = n.info.id;
    const models = (d.models || []).map(f => {
      const later = /-(?!00001)\d{5}-of-\d{5}\.gguf$/.test(f.file);   // a later piece of a multi-file model
      const sum = disk.sums[id + "/" + f.file];
      if (f.found) return `<div class="file"><span class="q">${esc(f.file.replace(/\.gguf$/, ""))}</span><span>${gb(f.size)}</span>
        <span class="n muted small">Kept by another app at <span style="overflow-wrap:anywhere">${esc(f.found)}</span>. Potluck reads it there and never changes it.</span>
        <span class="row"><button class="btn sm" data-a="planlocal" data-node="${esc(id)}" data-file="${esc(f.file)}">Set up</button>
        <button class="btn sm quiet" data-a="forget" data-node="${esc(id)}" data-file="${esc(f.file)}">Remove from list</button></span></div>`;
      return `<div class="file"><span class="q">${esc(f.file.replace(/\.gguf$/, ""))}</span><span>${gb(f.size)}</span>
        <span class="n muted small">${f.complete ? (f.addOn ? "A model's helper file (image add-on or similar)" : later ? "Part of a model that comes in several files" : "On " + esc(n.info.name)) : "Unfinished download"}${sum ? `<br>SHA-256: <span style="user-select:all;overflow-wrap:anywhere">${esc(sum)}</span>` : ""}</span>
        <span class="row">${f.complete && !later && !f.addOn ? `<button class="btn sm" data-a="planlocal" data-node="${esc(id)}" data-file="${esc(f.file)}">Set up</button>` : ""}
        ${f.complete ? `<button class="btn sm quiet" data-a="checksum" data-node="${esc(id)}" data-file="${esc(f.file)}">${sum === "…" ? "Working…" : "Checksum"}</button>` : ""}
        <button class="btn sm quiet" data-a="delmodel" data-node="${esc(id)}" data-file="${esc(f.file)}" data-name="${esc(n.info.name)}">Delete</button></span></div>`;
    }).join("");
    const cache = d.cache > 1048576 ? `<div class="file"><span class="q">Its share of the current model</span><span>${gb(d.cache)}</span>
        <span class="n muted small">Replaced automatically when you run a different model</span>
        <button class="btn sm quiet" data-a="clearcache" data-node="${esc(id)}" data-name="${esc(n.info.name)}">Clear</button></div>` : "";
    return `<div class="model"><div class="row spread"><h3>${esc(n.info.name)}</h3><span class="muted small">${gb(d.free)} free · AI engine ${gb(d.engine)}</span></div>
      ${models || cache ? `<div class="files">${models}${cache}</div>` : ""}
      <p class="muted small" style="margin-top:8px">Have a model file already, for example one fetched with a torrent app? Put the .gguf file in this folder on ${esc(n.info.name)} and it appears here: <span style="user-select:all;overflow-wrap:anywhere">${esc(d.dir || "")}</span></p></div>`;
  }).join("");
  return `<h2>On your computers' disks</h2>${rows || `<p class="muted">Nothing downloaded yet.</p>`}`;
}

// ---------- models already on this computer ----------

function loadFound(deep) {
  found.loading = true; found.deep = !!deep; found.error = ""; draw(true);
  api("/api/discover", { deep: !!deep }).then(r => { found.data = r; found.loading = false; draw(true); })
    .catch(e => { found.loading = false; found.error = e.message; draw(true); });
}

function foundView() {
  if (!found.data && !found.loading && !found.error) loadFound(false);
  const d = found.data || { models: [], engines: [] }, models = d.models || [];
  const here = { fits: ["ok", "Fits this computer"], tight: ["warn", "Tight on this computer"], no: ["bad", "Too big for this computer alone"] };
  const rows = models.map(f => {
    const [cls, word] = here[f.here] || ["bad", "Cannot be read"];
    const facts = [f.quant, f.arch, f.ctx ? `remembers up to ${f.ctx.toLocaleString()} tokens` : "", f.params ? (f.params / 1e9).toFixed(f.params < 1e10 ? 1 : 0) + " billion weights" : ""].filter(Boolean).map(esc).join(" · ");
    return `<div class="file"><span class="q">${esc(f.title)}</span><span>${gb(f.size)}</span>
      <span class="n small"><span class="badge ${cls}">${word}</span> <span class="muted">${facts}<br>${esc(f.error || f.hereNote)}${S.main.nodes.length > 1 && f.verdict === "pool" ? " " + esc(f.note) : ""}<br>From ${esc(f.from)}: <span style="overflow-wrap:anywhere">${esc(f.path)}</span></span>${speedLine(f)}</span>
      <span class="row">${f.error ? "" : `<button class="btn sm ${f.here === "no" && f.verdict !== "pool" ? "quiet" : ""}" data-a="usefound" data-path="${esc(f.path)}">${f.inUse ? "Set up" : "Use it"}</button>`}</span></div>`;
  }).join("");
  const eng = (d.engines || []).map(e => `<li><span style="overflow-wrap:anywhere">${esc(e.path)}</span> <span class="muted">${esc(e.note)}</span></li>`).join("");
  return `<h2>Already on this computer</h2>
    <p class="muted small">Models that LM Studio, Ollama, Hugging Face, llama.cpp or you put on this computer. Potluck uses them where they are: nothing is copied, moved or downloaded again.</p>
    ${found.error ? `<div class="err">${esc(found.error)}</div>` : ""}
    ${found.loading ? `<p class="muted">${found.deep ? "Searching the whole computer…" : "Looking…"}</p>` : rows ? `<div class="files">${rows}</div>` : `<p class="muted">No models found in the usual places.</p>`}
    <div class="row" style="margin-top:10px"><button class="btn sm ghost" data-a="founddeep" ${found.loading ? "disabled" : ""}>Search the whole computer</button>
      <button class="btn sm quiet" data-a="foundagain" ${found.loading ? "disabled" : ""}>Look again</button>
      <input type="text" id="adddir" placeholder="Another folder to look in" style="min-width:240px" aria-label="Another folder to look in"><button class="btn sm quiet" data-a="adddir">Add folder</button></div>
    ${(S.main.modelDirs || []).length ? `<p class="small muted" style="margin-top:6px">Also looking in: ${S.main.modelDirs.map(x => `${esc(x)} <button class="link" data-a="deldir" data-dir="${esc(x)}">remove</button>`).join(", ")}</p>` : ""}
    ${eng ? `<details style="margin-top:8px"><summary class="small muted">AI engines found on this computer</summary><ul class="small" style="margin:6px 0 0 18px">${eng}</ul></details>` : ""}`;
}

function localSettings() {
  const m = S.main, engines = (found.data && found.data.engines) || [];
  const opt = (v, label, on) => `<option value="${esc(v)}" ${on ? "selected" : ""}>${esc(label)}</option>`;
  const engSel = opt("", "Automatic: one already on this computer if it is new enough, else Potluck's own", !m.enginePath) + opt("potluck", "Always Potluck's own", m.enginePath === "potluck") +
    engines.filter(e => e.build).map(e => opt(e.path, `${e.path} (version ${e.build})`, m.enginePath === e.path)).join("");
  return `<h2>On this computer</h2><div class="card">
    <label class="sw"><input type="checkbox" data-local="single" ${m.single ? "checked" : ""}> Use this computer by itself: run models here, with all the memory it can spare</label>
    <p class="small" style="margin-top:10px"><label>Free the memory when a model has not been used for
      <select data-local="idle">${[[0, "never"], [10, "10 minutes"], [30, "30 minutes"], [60, "an hour"], [180, "3 hours"]].map(([v, t]) => opt(v, t, (m.idleUnload || 0) === v)).join("")}</select></label>
      <span class="muted">It loads again by itself with your next message.</span></p>
    <p class="small" style="margin-top:10px"><label>AI engine when a model runs here alone <select data-local="engine" style="max-width:100%">${engSel}</select></label></p>
    <p class="small" style="margin-top:10px"><label>Agent mode in the chat starts commands in <input type="text" data-local="agentdir" value="${esc(m.agentDir || "")}" style="min-width:300px"></label> <button class="btn sm quiet" data-a="agentdir">Save</button></p>
    <p class="small" style="margin-top:10px"><label>Hugging Face key, for gated models <input type="password" data-local="hfkey" placeholder="${m.hasHFKey ? "Saved. Type a new one to replace it" : "hf_…"}" autocomplete="off" style="min-width:260px"></label> <button class="btn sm quiet" data-a="hfkey">Save</button>${m.hasHFKey ? ` <button class="btn sm quiet" data-a="hfkeyrm">Remove</button>` : ""}</p>
    <p class="muted small">Some models only download after you accept their terms on their Hugging Face page while signed in. Then make a key that can read them at <a href="https://huggingface.co/settings/tokens" target="_blank" rel="noopener">huggingface.co/settings/tokens</a> and paste it here. Potluck sends it only to Hugging Face, and to your own computers when they download for you.</p>
  </div>`;
}

async function searchStore() {
  store.loading = true; store.error = ""; draw();
  try { store.results = await api(`/api/store?source=${store.source}&q=${encodeURIComponent(store.q)}`) || []; }
  catch (e) { store.results = []; store.error = e.message; }
  store.loading = false; draw();
}

function planBar(plan) {
  if (!plan || !plan.shares || !plan.shares.length) return "";
  const head = plan.shares[0], segs = [], legend = [];
  const total = plan.layers || 1, layerGB = plan.layerBytes || 0;
  const stripes = "background-image:repeating-linear-gradient(45deg,rgba(255,255,255,.35) 0 6px,transparent 6px 12px)";
  const seg = (n, color, striped) => n > 0 ? `<div style="flex:${n};background:${color};${striped ? stripes : ""}"></div>` : "";
  const bits = s => [s.cpuLayers ? `${s.cpuLayers} layers in memory` : "", s.gpuLayers ? (plan.cardTrunk && s === head ? `${s.gpuLayers} split between its graphics card and memory` : `${s.gpuLayers} on its graphics card`) : ""].filter(Boolean).join(" + ");
  segs.push(seg(head.cpuLayers, COLORS[0]));
  plan.shares.slice(1).forEach((s, k) => {
    const c = COLORS[(k + 1) % COLORS.length];
    segs.push(seg(s.gpuLayers, c, true), seg(s.cpuLayers, c));
    legend.push(`<div><i style="background:${c}"></i><b>${esc(s.name)}</b>: ${bits(s)}, about ${gb((s.cpuLayers + s.gpuLayers) * layerGB)}</div>`);
  });
  segs.push(seg(head.gpuLayers, COLORS[0], true));
  legend.unshift(`<div><i style="background:${COLORS[0]}"></i><b>${esc(head.name)}</b> (runs the model): ${bits(head) || "coordinates only"}, about ${gb((head.cpuLayers + head.gpuLayers) * layerGB)}${plan.solo && plan.overflow > 0 ? `, of which about ${gb(plan.overflow)} is read from its disk as needed` : ""}</div>`);
  return `<div class="plan" role="img" aria-label="How the model's layers are spread over the computers">${segs.join("")}</div><div class="legend">${legend.join("")}</div><p class="muted small">Striped parts sit on a graphics card, out of ${total} layers in all.${plan.cardTrunk ? " For this kind of model the card holds the part of each layer that is used for every word, and memory holds the rest." : ""}</p>`;
}

// details tucks the technical picture (which computer holds which layers) behind a click.
function details(inner) {
  return `<details class="small" style="margin-top:10px"><summary>Details: how it is split between your computers</summary>${inner}</details>`;
}

function viewSheet() {
  if (!sheet) return "";
  if (sheet.loading) return `<div class="sheet"><div class="card"><p>Reading the model's blueprint and working out a layout…</p></div></div>`;
  const p = sheet.plan, solo = sheet.solo, stream = sheet.stream, m = sheet.model, info = sheet.info;
  const isLocal = m.source === "local";
  const go = isLocal ? "Run it" : "Download and run";
  const ctx = `<label class="small">Conversation memory <select id="ctx"><option value="0" selected>Automatic (as long as fits without slowing down)</option><option value="8192">8,192 tokens</option><option value="16384">16,384</option><option value="32768">32,768</option><option value="65536">65,536</option></select></label>`;
  let body;
  if (p.fits) {
    const verdict = p.needsPool ? `<div class="banner" style="border-color:var(--ok);background:var(--ok-soft)"><b>Too big for the memory of any one of your computers. Runs across ${p.machines} of them together.</b></div>`
      : `<div class="note">Fits in ${esc(p.shares[0].name)}'s memory, so it runs there${p.machines > 1 ? " with help from the others" : ""}.</div>`;
    body = `${verdict}${p.kvKeyQ8 ? `<p class="small muted">To fit, Potluck stores the conversation memory a little more compactly. Answers are practically the same.</p>` : ""}${details(planBar(p))}
      <div class="row" style="margin-top:16px">${ctx}<button class="btn" data-a="start">${go}</button></div>
      <p class="muted small">${isLocal ? "The file is already on " + esc(p.shares[0].name) + (p.machines > 1 ? ". Helpers receive their share from it over your network, which is slower the first time than a download." : ", so nothing is downloaded.")
        : "Each computer downloads its own share. The first start takes as long as the download; later starts take a few minutes."}</p>`;
    if (p.needsPool && solo && solo.fits) {
      body += `<h2 style="font-size:16px">Or: ${esc(solo.shares[0].name)} alone, with help from its disk</h2>
        <p class="muted small">The model is about ${gb(solo.overflow)} bigger than ${esc(solo.shares[0].name)}'s free memory. It can keep most of it in memory and read the rest from disk as needed.
        No network in the way: with an SSD and this kind of model that can match or beat pooling; with a hard disk it is slow. Try both and compare with "Measure speed".</p>
        <button class="btn ghost" data-a="start" data-solo="${esc(solo.head)}">Run on ${esc(solo.shares[0].name)} alone</button>`;
    }
  } else if (solo && solo.fits) {
    body = `<div class="note"><b>Does not fit in your computers' pooled memory.</b> ${esc(p.reason)}</div>
      <h2 style="font-size:16px">It can still run on ${esc(solo.shares[0].name)}${solo.overflow > 0 ? ", with help from its disk" : ""}</h2>
      <p class="muted small">${solo.overflow > 0 ? `About ${gb(solo.overflow)} of it will not fit in memory and is read from disk as needed. Fine with an SSD, slow with a hard disk.` : "It needs nearly all the memory this computer has; close big apps first if it is slow."}</p>${details(planBar(solo))}
      <div class="row" style="margin-top:16px">${ctx}<button class="btn" data-a="start" data-solo="${esc(solo.head)}">${go} on ${esc(solo.shares[0].name)}</button></div>`;
  } else if (stream && stream.fits) {
    body = `<div class="note"><b>Too big for your computers' memory, pooled or alone.</b> ${esc(p.reason)}</div>`;
  } else {
    body = `<div class="err"><b>This one does not fit yet.</b> ${esc(p.reason)} Add a computer, press a pedal further down, or pick a smaller size.${stream && stream.reason ? `<br><span class="small">Streaming from disk: ${esc(stream.reason)}</span>` : ""}</div>`;
  }
  // Third way, for the model families InferSSD knows: one computer streams the experts from
  // its disk. It reaches far bigger models, at a few words a second on a fast drive.
  if (stream && stream.fits) {
    const n = esc(stream.shares[0].name), alone = !p.fits && !(solo && solo.fits);
    body += `<h2 style="font-size:16px">${alone ? "It can run on " + n + " by streaming from its disk" : "Or: stream it from " + n + "'s disk"}</h2>
      <p class="muted small">Uses the InferSSD engine: the part of the model used for every word stays in memory, and the experts each word needs are read from ${n}'s drive as they are needed${stream.overflow > 0 ? `, about ${gb(stream.overflow)} more than its free memory` : ""}.
      Expect a few words a second with a fast (NVMe) SSD and less with an older one; a hard disk is far too slow. The first start measures the drive for a few minutes. Runs on that one computer: the others are not used.${alone ? "" : " For a model that fits the options above, they are usually faster: streaming pays off when a model is far bigger than memory, on a computer with a fast drive and a modern graphics card (measured on a 4-core PC with an old 4 GB card: about 1 word a second, against 2 to 3 with the option above)."}</p>
      ${alone ? `<div class="row" style="margin-top:12px">${ctx}<button class="btn" data-a="start" data-solo="stream:${esc(stream.head)}">${go} with streaming</button></div>`
        : `<button class="btn ghost" data-a="start" data-solo="stream:${esc(stream.head)}">Stream from ${n}'s disk instead</button>`}`;
  }
  return `<div class="sheet" data-a="closesheet"><div class="card" data-stop>
    <div class="row spread"><h1 style="font-size:21px">${esc(m.title || m.file)}</h1><button class="link" data-a="closesheet">Close</button></div>
    <p class="muted small">${gb(m.size)} · ${esc(isLocal ? "already on one of your computers" : m.repo || "direct link")}</p>
    ${body}
  </div></div>`;
}

async function openPlan(model) {
  sheet = { loading: true }; draw();
  try { const r = await api("/api/plan", model); sheet = r; }
  catch (e) { sheet = null; toast(e.message); }
  draw();
}

// ---------- main computer: running ----------

function viewRun() { return viewRunMain() + crewView() + aiCard(); }

// ---------- the crew: extra models, each on one computer (crew.go) ----------
let crew = { list: null, at: 0, storage: null, msg: "" };
function loadCrew() {
  crew.at = Date.now();
  Promise.all([api("/api/crew"), crew.storage ? Promise.resolve(crew.storage) : api("/api/storage")])
    .then(([c, st]) => { crew.list = c.crew || []; crew.storage = st; draw(true); }).catch(e => { crew.msg = e.message; draw(true); });
}
function crewView() {
  if (!S.main) return "";
  if (crew.list === null || Date.now() - crew.at > 5000) loadCrew();
  const nodes = (S.main.nodes || []).filter(n => n.online && n.info.limits && n.info.limits.enabled);
  const list = (crew.list || []).map(c => `<div class="file"><span class="q">${esc(c.name)}</span><span class="small muted">${esc(c.nodeName)}</span>
      <span class="n small">${esc((c.model.title || c.model.file || "").replace(/\.gguf$/, ""))} · ${c.phase === "running" ? `<span class="badge ok">Running</span>` : c.phase === "error" ? `<span class="badge bad">Stopped</span> ${esc(c.error || "")}` : `${esc(c.note || "Starting")}…`}</span>
      <button class="btn sm quiet" data-a="crewstop" data-name="${esc(c.name)}">Stop</button></div>`).join("");
  // Models already on each computer, to start one without downloading.
  const local = [];
  for (const [id, rep] of Object.entries(crew.storage || {})) for (const m of (rep.models || [])) if (m.complete && !/-0000[2-9]-of-/.test(m.file)) local.push({ id, file: m.file, size: m.size });
  const nodeOpts = nodes.map(n => `<option value="${esc(n.info.id)}">${esc(n.info.name)}</option>`).join("");
  const modelOpts = local.map(m => `<option value="${esc(m.id + "|" + m.file)}">${esc(m.file.replace(/\.gguf$/, ""))} (${gb(m.size)}, on ${esc(((S.main.nodes || []).find(n => n.info.id === m.id) || { info: { name: "?" } }).info.name)})</option>`).join("");
  return `<div class="card" style="margin-top:16px"><h3>Crew: more models at once</h3>
    <p class="muted small">Run extra models on your other computers, next to the main one. Chat with any of them from the chat's model menu, or let a task hand them parts of a big job at the same time (it uses them by itself when they are running).</p>
    ${crew.msg ? `<div class="err">${esc(crew.msg)}</div>` : ""}
    ${list ? `<div class="files" style="margin-top:8px">${list}</div>` : `<p class="small muted" style="margin-top:8px">No crew members yet.</p>`}
    <div class="tools" style="margin-top:10px;flex-wrap:wrap">
      <select id="crew-node" aria-label="Computer">${nodeOpts}</select>
      <select id="crew-model" aria-label="Model" style="min-width:240px"><option value="">A model already downloaded…</option>${modelOpts}</select>
      <input type="text" id="crew-repo" placeholder="or Hugging Face repo, e.g. unsloth/gemma-4-12B-it-GGUF" style="min-width:260px" aria-label="Hugging Face repository">
      <input type="text" id="crew-file" placeholder="file, e.g. gemma-4-12B-it-Q4_K_M.gguf" style="min-width:220px" aria-label="Model file">
      <button class="btn" data-a="crewstart">Start</button></div>
    <p class="small muted">A model already on a computer runs there; a Hugging Face model is downloaded to the computer you pick.</p></div>`;
}

const quick = { busy: false, error: "" };

function viewRunMain() {
  const m = S.main, c = m.cluster;
  if (c.phase === "idle") {
    if (m.pauseWhy) return `<h1>Waiting for your computers</h1><div class="note">${esc(m.pauseWhy)}</div>
      <p style="margin-top:14px"><button class="btn ghost" data-a="stop">Do not restart it</button></p>`;
    if (m.asleep) return `<h1>${esc(m.asleep.title || m.asleep.file)} is resting</h1>
      <p class="muted">It was let go after ${m.idleUnload} minutes without use, so its memory is free for other apps. It loads again by itself with your next chat message.</p>
      <p style="margin-top:14px"><a class="btn" href="${esc(m.chatURL)}" target="_blank" rel="noopener">Open chat</a> <button class="btn ghost" data-a="again">Load it now</button></p>`;
    if (!m.lastModel) return `<h1>Ready when you are</h1><p class="muted">One click picks the best model your computers can run, downloads it and starts it. A model you already have (from LM Studio, Ollama or Hugging Face) is used where it is.</p>
      ${quick.error ? `<div class="note">${esc(quick.error)}</div>` : ""}
      <p style="margin-top:14px"><button class="btn" data-a="quick" ${quick.busy ? "disabled" : ""}>${quick.busy ? "Choosing a model…" : "Download and start"}</button> <button class="btn ghost" data-a="tab" data-tab="models">Choose a model myself</button></p>`;
    return `<h1>Nothing is running</h1><p class="muted">Pick a model and Potluck spreads it over your computers.</p>
      <p style="margin-top:14px"><button class="btn" data-a="tab" data-tab="models">Choose a model</button>
      ${m.lastModel ? ` <button class="btn ghost" data-a="again">Run ${esc(m.lastModel.title || m.lastModel.file)} again</button>` : ""}</p>`;
  }
  const title = c.model ? esc(c.model.title || c.model.file) : "Model";
  const steps = `<ul class="steps">${(c.steps || []).map(s => `<li class="${esc(s.state)}">${esc(s.text)}</li>`).join("")}</ul>`;
  const nodes = m.nodes.filter(n => n.role).map(n => `<div class="card"><div class="row spread"><h3>${esc(n.info.name)}</h3><span class="badge ok">${n.role === "head" ? "Runs the model" : "Holds part of it"}</span></div>${progress(n.status, n.done, n.total)}</div>`).join("");
  if (c.phase === "starting" || c.phase === "stopping") {
    return `<h1>${c.phase === "stopping" ? "Stopping" : "Getting " + title + " ready"}</h1>${steps}
      ${c.plan && c.plan.fits ? planBar(c.plan) : ""}<div class="grid" style="margin-top:16px">${nodes}</div>
      <p style="margin-top:16px"><button class="btn ghost" data-a="stop">Cancel</button></p>`;
  }
  if (c.phase === "error") {
    if (!logText) api("/api/log").then(r => { logText = r.log || " "; draw(); }).catch(() => { logText = " "; });
    return `<h1>${title} did not start</h1><div class="err">${esc(c.error)}</div>${steps}
      ${logText.trim() ? `<details><summary class="small muted">Technical details</summary><pre class="log">${esc(logText.slice(-3500))}</pre></details>` : ""}
      <p style="margin-top:16px"><button class="btn" data-a="again">Try again</button> <button class="btn ghost" data-a="stop">Dismiss</button></p>`;
  }
  const p = c.plan || {};
  return `<div class="row spread"><div><h1>${title} is running</h1>
      <p class="muted">${p.stream ? `Running on ${esc(p.shares[0].name)}, streaming from its disk with InferSSD.` : p.solo ? `Running on ${esc(p.shares[0].name)} alone${p.overflow > 0 ? `, reading about ${gb(p.overflow)} of it from disk as needed` : ""}.` : p.needsPool ? `Too big for the memory of any one of your computers. Pooled across ${p.machines}.` : `Running on ${p.machines || 1} ${p.machines > 1 ? "computers" : "computer"}.`}</p></div>
      <div class="row"><a class="btn" href="${esc(m.chatURL)}" target="_blank" rel="noopener">Open chat</a><button class="btn ghost" data-a="stop">Stop</button></div></div>
    ${m.stale ? `<div class="banner"><div class="row spread"><div>A pedal changed on a computer that is part of this model. It takes effect when the model is laid out again.</div><button class="btn" data-a="again">Apply now</button></div></div>` : ""}
    ${planBar(p)}
    <div class="grid" style="margin-top:18px">
      <div class="card speed"><h3>Speed</h3>
        ${c.tps ? `<p><b>${c.tps.toFixed(1)}</b> tokens per second</p><p class="muted small">Measured on a short question. A token is about three quarters of a word; people read roughly 5 per second.</p>` : `<p class="muted">Not measured yet.</p>`}
        <p><button class="btn quiet sm" data-a="test" ${c.testing ? "disabled" : ""}>${c.testing ? "Measuring…" : "Measure speed"}</button></p>
        ${c.reply ? `<p class="small muted" style="margin-top:8px">"${esc(c.reply.slice(0, 360))}"</p>` : ""}</div>
      <div class="card"><h3>Use it from other apps and devices</h3>
        <p class="muted small">Anything that speaks the OpenAI API can use your pooled model. On this computer the address is:</p>
        <p class="addr" style="font-size:17px">${esc(m.chatURL)}/v1</p>
        <label class="sw" style="margin-top:10px"><input type="checkbox" data-share ${m.shareChat ? "checked" : ""}> Let my other devices and apps use it (home network and Tailscale, with a key)</label>
        ${m.shareChat ? `<div style="margin-top:8px">
          <p class="muted small">In an app such as Apollo, Open WebUI, Msty or a router, add a custom OpenAI-compatible server with:</p>
          <p class="small">Server: <span class="addr">${esc((m.shareURLs && m.shareURLs[0]) || "")}/v1</span> <button class="link" data-a="copytext" data-text="${esc((m.shareURLs && m.shareURLs[0]) || "")}/v1">Copy</button></p>
          <p class="small">API key: <span class="addr">${esc(m.shareKey || "")}</span> <button class="link" data-a="copytext" data-text="${esc(m.shareKey || "")}">Copy</button></p>
          ${m.shareURLs && m.shareURLs.length > 1 ? `<p class="muted small">Also reachable at ${m.shareURLs.slice(1).map(u => esc(u)).join(", ")} (Tailscale addresses work away from home).</p>` : ""}
          <p class="muted small">In a browser on another device, open <span class="addr">${esc((m.shareURLs && m.shareURLs[0]) || "")}/?key=${esc(m.shareKey || "")}</span> once; it remembers the key.</p>
          <p class="small"><button class="link" data-a="newsharekey">Make a new key</button> <span class="muted">(locks out every device and app using the old one)</span></p></div>` : ""}</div>
    </div>
    <div class="grid" style="margin-top:16px">${nodes}</div>`;
}

// ---------- the prompt for the user's own AI ----------

let ai = { text: "", guide: "", at: 0, open: false, copied: false };
function loadAI() { return api("/api/aiprompt").then(r => { ai.text = r.text; ai.guide = r.guide; ai.at = Date.now(); draw(); }).catch(() => {}); }

function aiCard() {
  if (!ai.text || Date.now() - ai.at > 15000) { ai.at = Date.now(); loadAI(); }
  return `<h2 style="margin-top:28px">Use Potluck from your own AI</h2><div class="card">
    <p class="small">Teach the AI you already use, such as Claude, ChatGPT, Codex or Cursor, about Potluck. Copy this note and paste it into a chat with it. It saves the note to its memory, so later you can just say <b>"use Potluck to…"</b> or <b>"run this on the local AI"</b> and it knows what to do.</p>
    <p class="muted small" style="margin-top:6px">Your AI must run on this computer and be able to make web requests or run commands, as desktop apps with tools and coding agents can. A chat website can't reach your computer. When it needs details, your AI reads them from Potluck itself: the model loaded now, how to send it work and the ground rules.</p>
    <div class="row" style="margin-top:10px"><button class="btn sm" data-a="aicopy">${ai.copied ? "Copied ✓" : "Copy the note for your AI"}</button>
      <button class="btn sm ghost" data-a="aishow">${ai.open ? "Hide it" : "Show it"}</button>
      <a class="btn sm ghost" href="${esc(S.main.chatURL)}/potluck/ai" target="_blank" rel="noopener">What your AI reads</a></div>
    ${ai.open ? `<textarea readonly rows="14" aria-label="Note for your AI" onclick="this.select()" style="margin-top:10px;width:100%;font:12px/1.45 ui-monospace,Menlo,monospace">${esc(ai.text || "Loading…")}</textarea>` : ""}
  </div>`;
}

// ---------- drawing ----------

function busy() {
  if (Date.now() < holdUntil) return true;
  const a = document.activeElement;
  return a && a.closest("#app") && /^(INPUT|SELECT|TEXTAREA)$/.test(a.tagName) && a.type !== "checkbox";
}

function draw(force) {
  if (!S) return;
  const tabs = $("#tabs"), pill = $("#pill");
  tabs.hidden = !S.main; pill.hidden = !S.main && !S.helper;
  let html;
  if (S.main) {
    const c = S.main.cluster;
    pill.className = "pill " + (c.phase === "running" ? "ok" : c.phase === "error" ? "bad" : c.phase === "idle" ? "" : "busy");
    pill.textContent = c.phase === "running" ? "Model running" : c.phase === "starting" ? "Getting ready…" : c.phase === "error" ? "Needs attention" : c.phase === "stopping" ? "Stopping…" : "Idle";
    for (const b of tabs.children) b.classList.toggle("on", b.dataset.tab === tab);
    html = (tab === "models" ? viewModels() : tab === "run" ? viewRun() : viewComputers()) + viewSheet();
  } else if (S.helper) {
    const ph = S.helper.link.phase;
    pill.className = "pill " + (ph === "connected" ? "ok" : "busy");
    pill.textContent = ph === "connected" ? "Connected" : ph === "pairing" ? "Waiting for approval" : "Searching";
    html = viewHelper();
  } else {
    html = viewChoose();
  }
  const where = (S.main && S.main.dataDir) || (S.helper && S.helper.dataDir) || "";
  html += `<footer class="muted small">Potluck ${esc(S.version)}${where ? ` · everything it stores is in ${esc(where)}` : ""} · <button class="link" data-a="quit">Quit Potluck</button></footer>`;
  if (!force && busy()) return;
  const app = $("#app"), y = window.scrollY;
  app.innerHTML = html;
  window.scrollTo(0, y);
}

async function refresh() {
  try { S = await api("/api/state"); draw(); }
  catch (e) { $("#pill").hidden = false; $("#pill").className = "pill bad"; $("#pill").textContent = "Potluck is not running"; }
}

// ---------- actions ----------

const actions = {
  tab(t) { tab = t.dataset.tab; location.hash = tab; logText = ""; if (tab === "models") disk.data = null; draw(true); },
  role(t) { act(() => api("/api/role", { role: t.dataset.role })); },
  pending(t) { act(() => api("/api/pending", { id: t.dataset.id, approve: !!t.dataset.ok })); },
  rename(t) {
    const name = prompt("Name for this computer:", t.dataset.name);
    if (!name || !name.trim()) return;
    const n = S.main ? S.main.nodes.find(x => x.info.id === t.dataset.id) : null;
    const limits = n ? n.info.limits : S.helper.info.limits;
    act(() => api("/api/node", { id: t.dataset.id, name: name.trim(), limits }));
  },
  bench(t) {
    const id = t.dataset.id; benching[id] = true; draw(true);
    api("/api/bench", { id }).then(() => toast("Measured.")).catch(e => toast(e.message)).finally(() => { delete benching[id]; refresh && refresh(); });
  },
  forget(t) { if (confirm(`Remove ${t.dataset.name} from your setup? It can join again later.`)) act(() => api("/api/forget", { id: t.dataset.id })); },
  leave() { if (confirm("Stop lending this computer? It will look for a main computer again.")) act(() => api("/api/forget", {})); },
  firewall() { toast("Look for the password dialog from your computer."); act(() => api("/api/firewall", {})); },
  manual() { const v = $("#manual").value.trim(); if (v) act(() => api("/api/manual", { addr: v })); },
  kit() {
    let path = $("#kitdrive").value;
    if (!path) { path = prompt("Folder to put the setup kit in:", ""); if (!path) return; }
    kit.msg = "Writing…"; draw(true);
    api("/api/kit", { path }).then(r => { kit.msg = `Done. The kit is in ${r.dir}.` + (r.missing && r.missing.length ? ` Not included (program missing): ${r.missing.join(", ")}.` : ""); draw(true); })
      .catch(e => { kit.msg = e.message; draw(true); });
  },
  source(t) { store.source = t.dataset.source; store.results = null; store.q = $("#q") ? $("#q").value : store.q; draw(true); },
  search() { store.q = $("#q").value; store.results = null; draw(true); },
  async files(t) {
    const repo = t.dataset.repo;
    store.open[repo] = !store.open[repo];
    if (store.open[repo] && !store.files[repo]) {
      try { store.files[repo] = await api("/api/files?repo=" + encodeURIComponent(repo)); } catch (e) { store.open[repo] = false; toast(e.message); }
    }
    draw(true);
  },
  plan(t) { openPlan({ source: "hf", repo: t.dataset.repo, file: t.dataset.file }); },
  planurl() { const u = $("#url").value.trim(); if (u) openPlan({ source: "url", url: u }); },
  closesheet(t, e) { if (e.target.closest("[data-stop]") && !e.target.closest("[data-a=closesheet]:not(.sheet)")) return; sheet = null; draw(true); },
  start(t) {
    const m = sheet.model, ctx = +($("#ctx") ? $("#ctx").value : 0), solo = t.dataset.solo || "";
    sheet = null; tab = "run"; location.hash = tab;
    act(() => api("/api/start", { ...m, ctx, solo }));
  },
  quick() {
    quick.busy = true; quick.error = ""; draw(true);
    api("/api/quickstart", {}).then(() => { quick.busy = false; tab = "run"; draw(true); }).catch(e => { quick.busy = false; quick.error = e.message; draw(true); });
  },
  again() {
    const c = S.main.cluster, m = c.model || S.main.lastModel;
    if (m) { logText = ""; act(() => api("/api/start", { ...m, ctx: S.main.lastCtx || 0, solo: c.plan && c.plan.stream ? "stream:" + c.plan.head : c.plan && c.plan.solo ? c.plan.head : "" })); }
  },
  stop() { logText = ""; act(() => api("/api/stop", {})); },
  test() { act(() => api("/api/test", {})); },
  planlocal(t) { openPlan({ source: "local", node: t.dataset.node, file: t.dataset.file }); },
  phonepair() { phones.error = ""; api("/api/phones", { action: "pair" }).then(r => { phones.code = r; phones.step = phones.list && phones.list.some(p => !p.pairing) ? "pair" : "app"; loadPhones(); }).catch(e => { phones.error = e.message; draw(true); }); },
  phonestep(t) { phones.step = t.dataset.step; draw(true); },
  phonestop() { phones.code = null; api("/api/phones", { action: "stop" }).then(loadPhones).catch(e => toast(e.message)); },
  pushon() {
    const v = k => (document.querySelector(`[data-push=${k}]`) || {}).value || "";
    api("/api/phones", { action: "push", keyPath: v("keyPath").trim(), keyID: v("keyID").trim(), teamID: v("teamID").trim() }).then(() => { toast("Alerts are on. Your phone hears about every question from now on."); loadPhones(); }).catch(e => toast(e.message));
  },
  connadd(t) {
    const name = t.dataset.name || $("#conn-name").value, baseURL = t.dataset.url || $("#conn-url").value, key = t.dataset.url ? "" : $("#conn-key").value;
    if (!baseURL.trim()) return toast("Type the server's address first.");
    api("/api/connections", { action: "add", name, baseURL, key }).then(r => { toast(`Added ${r.name}. Its models are in the chat's model menu.`); conns = null; draw(true); }).catch(e => toast(e.message));
  },
  connrm(t) { if (confirm("Remove this connection? Its models leave Potluck's chat.")) api("/api/connections", { action: "remove", id: t.dataset.id }).then(() => { conns = null; draw(true); }).catch(e => toast(e.message)); },
  crewstart() {
    const node = $("#crew-node").value, pick = $("#crew-model").value, repo = $("#crew-repo").value.trim(), file = $("#crew-file").value.trim();
    let body;
    if (pick) { const [id, f] = pick.split("|"); body = { action: "start", node: id, file: f }; }
    else if (repo && file) body = { action: "start", node, repo, file };
    else return toast("Pick a downloaded model, or type a Hugging Face repo and file.");
    crew.msg = "";
    api("/api/crew", body).then(r => { toast("Starting " + r.name + "…"); loadCrew(); }).catch(e => { crew.msg = e.message; draw(true); });
  },
  crewstop(t) { api("/api/crew", { action: "stop", name: t.dataset.name }).then(loadCrew).catch(e => toast(e.message)); },
  copytext(t) { navigator.clipboard.writeText(t.dataset.text).then(() => toast("Copied.")).catch(() => toast(t.dataset.text)); },
  newsharekey() { if (confirm("Make a new key? Devices and apps using the old key stop working until you give them the new one.")) act(() => api("/api/share", { rotate: true })); },
  pushtest() { api("/api/phones", { action: "test" }).then(() => { toast("Test alert sent. Answer it on your phone."); setTimeout(loadPhones, 8000); setTimeout(loadPhones, 30000); }).catch(e => toast(e.message)); },
  pushoff() { api("/api/phones", { action: "push", keyPath: "" }).then(loadPhones).catch(e => toast(e.message)); },
  phoneforget(t) { if (confirm("Remove this phone? It will need to be paired again to connect.")) api("/api/phones", { action: "forget", id: t.dataset.id }).then(loadPhones).catch(e => toast(e.message)); },
  async usefound(t) {
    try {
      const r = await api("/api/use_found", { path: t.dataset.path });
      disk.data = null;
      ((found.data && found.data.models) || []).forEach(f => { if (f.path === t.dataset.path) f.inUse = true; });
      openPlan({ source: "local", node: S.id, file: r.file });
    } catch (e) { toast(e.message); }
  },
  forget(t) { api("/api/storage", { node: t.dataset.node, action: "delete_model", file: t.dataset.file }).then(() => { loadDisk(); found.data = null; }).catch(e => toast(e.message)); },
  founddeep() { loadFound(true); },
  foundagain() { loadFound(false); },
  adddir() { const v = $("#adddir").value.trim(); if (v) act(() => api("/api/local", { modelDirs: [...(S.main.modelDirs || []), v] }).then(() => loadFound(false))); },
  deldir(t) { act(() => api("/api/local", { modelDirs: (S.main.modelDirs || []).filter(x => x !== t.dataset.dir) }).then(() => loadFound(false))); },
  listopen(t) { store.source = "hf"; store.q = t.dataset.q; store.results = null; draw(true); window.scrollTo(0, 0); },
  hfkey() { const v = $("[data-local=hfkey]").value.trim(); if (!v) return; act(() => api("/api/local", { hfKey: v }).then(() => toast("Hugging Face key saved"))); },
  hfkeyrm() { act(() => api("/api/local", { hfKey: "" }).then(() => toast("Hugging Face key removed"))); },
  agentdir() { const v = $("[data-local=agentdir]").value.trim(); act(() => api("/api/local", { agentDir: v }).then(() => toast("Saved"))); },
  checksum(t) {
    const k = t.dataset.node + "/" + t.dataset.file;
    disk.sums[k] = "…"; draw(true); toast("Reading the whole file. A big model takes a few minutes.");
    api("/api/storage", { node: t.dataset.node, action: "hash_model", file: t.dataset.file }).then(r => { disk.sums[k] = r.sha256; draw(true); })
      .catch(e => { delete disk.sums[k]; toast(e.message); draw(true); });
  },
  delmodel(t) { if (confirm(`Delete ${t.dataset.file} from ${t.dataset.name}? You can download it again later.`)) api("/api/storage", { node: t.dataset.node, action: "delete_model", file: t.dataset.file }).then(loadDisk).catch(e => toast(e.message)); },
  clearcache(t) { if (confirm(`Clear ${t.dataset.name}'s share of the model? It is downloaded again the next time it is needed.`)) api("/api/storage", { node: t.dataset.node, action: "clear_cache" }).then(loadDisk).catch(e => toast(e.message)); },
  quit() { if (confirm("Quit Potluck on this computer? Any running model stops.")) api("/api/quit", {}).then(() => { $("#app").innerHTML = `<p class="muted center">Potluck has quit. You can close this tab.</p>`; S = null; }); },
  magnet(t) { navigator.clipboard.writeText(t.dataset.magnet).then(() => toast("Torrent link copied. Paste it into your torrent app."), () => toast("Could not copy. Your browser blocked it.")); },
  async aicopy() {
    if (!ai.text) await loadAI();
    // Copy right away, inside the click, from the text already here (it is at most 15 s old).
    let ok = false;
    try { await navigator.clipboard.writeText(ai.text); ok = true; } catch (e) {
      const t = document.createElement("textarea");
      t.value = ai.text; t.setAttribute("readonly", ""); t.style.cssText = "position:fixed;top:-1000px";
      document.body.appendChild(t); t.select();
      try { ok = document.execCommand("copy"); } catch (e2) { ok = false; }
      t.remove();
    }
    if (!ok) { ai.open = true; draw(true); toast("Your browser blocked copying. Select the text below and copy it."); return; }
    ai.copied = true; draw(true); toast("Copied. Paste it into your AI.");
    setTimeout(() => { ai.copied = false; draw(true); }, 3000);
    loadAI();
  },
  aishow() { ai.open = !ai.open; draw(true); },
  contscan() {
    const t = $("[data-contfolders]"); if (t) cont.folders = t.value;
    cont.busy = "scan"; cont.err = cont.msg = ""; draw(true);
    api("/api/continuity", { action: "scan", folders: (cont.folders || "").split("\n").map(x => x.trim()).filter(Boolean) })
      .then(r => { cont.found = r.found || []; cont.pick = {}; }).catch(e => { cont.err = e.message; }).finally(() => { cont.busy = ""; draw(true); });
  },
  contimport() {
    const ids = (cont.found || []).map(s => s.id).filter(id => { const b = document.querySelector(`[data-contpick="${CSS.escape(id)}"]`); return b ? b.checked : true; });
    cont.busy = "import"; draw(true);
    api("/api/continuity", { action: "import", ids }).then(() => { cont.found = null; cont.msg = "Brought over. Potluck's chat and agent use it from the next message."; return loadCont(); })
      .catch(e => { cont.err = e.message; }).finally(() => { cont.busy = ""; draw(true); });
  },
  contpaste() {
    const t = $("[data-contpaste]"), text = t ? t.value.trim() : ""; if (!text) { cont.err = "Paste what your AI wrote first."; draw(true); return; }
    cont.busy = "paste"; cont.err = cont.msg = ""; draw(true);
    api("/api/continuity", { action: "bundle", text }).then(() => { cont.paste = ""; cont.msg = "Brought over from your AI."; return loadCont(); })
      .catch(e => { cont.err = e.message; }).finally(() => { cont.busy = ""; draw(true); });
  },
  async contcopy() {
    if (!cont.data) await loadCont();
    const text = (cont.data && cont.data.prompt) || ""; let ok = false;
    try { await navigator.clipboard.writeText(text); ok = true; } catch (e) {
      const t = document.createElement("textarea"); t.value = text; t.style.cssText = "position:fixed;top:-1000px"; document.body.appendChild(t); t.select();
      try { ok = document.execCommand("copy"); } catch (e2) { ok = false; } t.remove();
    }
    if (!ok) { cont.showPrompt = true; draw(true); toast("Your browser blocked copying. Select the text and copy it."); return; }
    cont.copied = true; draw(true); toast("Copied. Paste it into a chat with your AI."); setTimeout(() => { cont.copied = false; draw(true); }, 3000);
  },
  contshow() { cont.showPrompt = !cont.showPrompt; draw(true); },
  contforget() { if (confirm("Forget everything brought over from your earlier AI setup? Your original files are not touched.")) api("/api/continuity", { action: "forget" }).then(() => { cont.msg = ""; return loadCont(); }).catch(e => toast(e.message)); },
  tohf(t) { store.source = "hf"; store.q = t.dataset.repo.split("/").pop(); store.results = null; draw(true); },
};

document.addEventListener("click", e => {
  const t = e.target.closest("[data-a]");
  if (t && actions[t.dataset.a]) { e.preventDefault(); actions[t.dataset.a](t, e); }
});
$("#tabs").addEventListener("click", e => { const b = e.target.closest("button[data-tab]"); if (b) actions.tab(b); });
document.addEventListener("input", e => {
  holdUntil = Date.now() + 2500;
  if (e.target.matches("[data-contfolders]")) cont.folders = e.target.value;
  if (e.target.matches("[data-contpaste]")) cont.paste = e.target.value;
  if (e.target.matches("[data-c=pedal]")) { const b = $("[data-pedal]", e.target.closest(".limits")); b.textContent = `${e.target.value}% · ${pedalWord(+e.target.value)}`; }
});
document.addEventListener("change", e => {
  if (e.target.matches("[data-share]")) { const on = e.target.checked; holdUntil = 0; return act(() => api("/api/share", { on })); }
  if (e.target.matches("[data-head]")) { const id = e.target.value; holdUntil = 0; return act(() => api("/api/head", { id })); }
  const loc = e.target.dataset.local;
  if (loc === "single") { holdUntil = 0; return act(() => api("/api/local", { single: e.target.checked })); }
  if (loc === "idle") { holdUntil = 0; return act(() => api("/api/local", { idleUnload: +e.target.value })); }
  if (loc === "engine") { holdUntil = 0; return act(() => api("/api/local", { enginePath: e.target.value })); }
  const box = e.target.closest(".limits");
  if (!box) return;
  const id = box.dataset.id, limits = readLimits(box);
  holdUntil = 0;
  act(() => api("/api/node", { id, limits, name: "" }));
});
document.addEventListener("keydown", e => {
  if (e.key === "Enter" && e.target.id === "q") actions.search();
  if (e.key === "Enter" && e.target.id === "manual") actions.manual();
  if (e.key === "Escape" && sheet) { sheet = null; draw(true); }
});
window.addEventListener("hashchange", () => { const h = location.hash.slice(1); if (h && h !== tab) { tab = h; draw(true); } });

refresh();
setInterval(refresh, 1500);

// ---------- Ask Potluck: the built-in helper (helper.go) ----------
// A small model of Potluck's own sets Potluck up and answers questions, using Potluck's own
// controls. The Mac app has its own window for this; here it is a drawer in the corner.
(function askPotluck() {
  const ACTIONS = { get_status: "Looked at your computers", list_drives: "Looked at your drives", set_data_folder: "Moving Potluck's folder",
    recommend_model: "Compared models for your computers", install_model: "Starting a model", stop_model: "Stopping the model",
    add_computer_steps: "Looked up how to add a computer", pair_phone: "Made a phone pairing code", add_ai_connection: "Connecting your other AI app" };
  const EXAMPLES = ["Install the best model my computers can run", "Help me add my other computers", "Put Potluck on another drive", "Connect my iPhone"];
  let id = null, timer = null, last = null;
  // Bold, code and list bullets; everything escaped first.
  const lightMd = t => esc(t).replace(/\*\*(.+?)\*\*/g, "<b>$1</b>").replace(/`([^`]+)`/g, "<code>$1</code>").replace(/^\s*[-*] /gm, "• ");
  const css = document.createElement("style");
  css.textContent = `.ask-btn{position:fixed;right:20px;bottom:20px;z-index:20;border:0;border-radius:999px;padding:12px 18px;background:var(--accent);color:var(--accent-ink);font:600 15px system-ui;box-shadow:0 4px 16px rgba(0,0,0,.2);cursor:pointer}
  .ask{position:fixed;right:20px;bottom:76px;z-index:21;width:min(420px,calc(100vw - 40px));height:min(560px,calc(100vh - 120px));display:none;flex-direction:column;background:var(--card);border:1px solid var(--line);border-radius:14px;box-shadow:0 8px 32px rgba(0,0,0,.25);overflow:hidden}
  .ask.open{display:flex}.ask-log{flex:1;overflow:auto;padding:14px;display:flex;flex-direction:column;gap:8px}
  .ask-me{align-self:flex-end;background:var(--soft);padding:8px 11px;border-radius:10px;max-width:85%;white-space:pre-wrap}
  .ask-it{background:var(--bg);padding:8px 11px;border-radius:10px;max-width:92%;white-space:pre-wrap}
  .ask-act{color:var(--muted);font-size:13px}.ask-q{padding:12px;background:var(--soft);border-top:1px solid var(--line)}
  .ask-in{display:flex;gap:8px;padding:10px;border-top:1px solid var(--line)}.ask-in textarea{flex:1;resize:none;font:inherit;border:1px solid var(--line);border-radius:8px;padding:8px;background:var(--bg);color:var(--ink)}`;
  document.head.appendChild(css);
  const btn = document.createElement("button");
  btn.className = "ask-btn"; btn.textContent = "Ask Potluck"; btn.setAttribute("aria-expanded", "false");
  const box = document.createElement("div");
  box.className = "ask"; box.setAttribute("role", "dialog"); box.setAttribute("aria-label", "Ask Potluck");
  box.innerHTML = `<div class="row spread" style="padding:10px 14px;border-bottom:1px solid var(--line)"><b>Ask Potluck</b><span><button class="link" data-x="new">New</button> · <button class="link" data-x="close">Close</button></span></div>
    <div class="ask-log" aria-live="polite"></div><div class="ask-q" hidden></div>
    <div class="ask-in"><textarea rows="2" placeholder="Ask anything, like &quot;install the best model I can run&quot;" aria-label="Ask Potluck"></textarea><button class="btn" data-x="send">Send</button></div>`;
  document.body.append(btn, box);
  const log = box.querySelector(".ask-log"), qbox = box.querySelector(".ask-q"), ta = box.querySelector("textarea");
  const intro = () => { log.innerHTML = `<p class="muted small">Potluck's own helper sets things up for you and answers questions. It asks before anything big.</p>` + EXAMPLES.map(e => `<button class="link" data-ex="${esc(e)}" style="text-align:left">${esc(e)}</button>`).join(""); };
  intro();
  btn.onclick = () => { const o = box.classList.toggle("open"); btn.setAttribute("aria-expanded", String(o)); if (o) ta.focus(); };
  const send = async text => {
    text = (text || "").trim(); if (!text) return;
    if (!id) log.innerHTML = "";
    log.insertAdjacentHTML("beforeend", `<div class="ask-me">${esc(text)}</div><div class="ask-act" data-wait>Working…</div>`);
    try { const r = await api("/api/ask", { id, text }); id = r.id; poll(); } catch (e) { log.insertAdjacentHTML("beforeend", `<div class="err">${esc(e.message)}</div>`); }
  };
  const poll = () => { clearTimeout(timer); timer = setTimeout(tick, 900); };
  async function tick() {
    if (!id) return;
    let s; try { s = await api("/api/ask?id=" + encodeURIComponent(id)); } catch (e) { return poll(); }
    const key = JSON.stringify([s.messages.length, s.status, s.question && s.question.id, s.helper && s.helper.note]);
    if (key !== last) {
      last = key;
      log.innerHTML = s.messages.map(m => m.role === "user" ? `<div class="ask-me">${esc(m.content)}</div>`
        : m.role === "assistant" ? (m.content && m.content.trim() ? `<div class="ask-it">${lightMd(m.content.trim())}</div>` : "")
        : m.name && m.name !== "ask_user" ? `<div class="ask-act">✓ ${esc(ACTIONS[m.name] || m.name)}${m.state === "error" ? " (didn't work)" : ""}</div>` : "").join("")
        + (s.status === "working" ? `<div class="ask-act">${s.helper && s.helper.phase !== "ready" ? "Getting the helper ready" + (s.helper.note ? ": " + esc(s.helper.note) : "…") : "Working…"}</div>` : "")
        + (s.error ? `<div class="err">${esc(s.error)}</div>` : "");
      log.scrollTop = log.scrollHeight;
      if (s.question) {
        qbox.hidden = false;
        qbox.innerHTML = `<b>${esc(s.question.title)}</b>${s.question.detail ? `<p class="small muted">${esc(s.question.detail)}</p>` : ""}
          <div class="row" style="gap:8px;margin-top:6px"><button class="btn sm" data-ans="yes">Yes</button><button class="btn sm ghost" data-ans="no">No</button>
          <input type="text" placeholder="Or type your answer" style="flex:1" aria-label="Your answer"></div>`;
        qbox.dataset.q = s.question.id;
      } else { qbox.hidden = true; }
    }
    if (s.status === "working" || s.status === "waiting") poll();
  }
  const answer = async a => { const q = qbox.dataset.q; qbox.hidden = true; try { await api("/api/ask", { id, question: q, answer: a }); } catch (e) {} last = null; poll(); };
  box.addEventListener("click", e => {
    const t = e.target.closest("button"); if (!t) return;
    if (t.dataset.ex) return send(t.dataset.ex);
    if (t.dataset.ans) return answer(t.dataset.ans);
    if (t.dataset.x === "send") { const v = ta.value; ta.value = ""; return send(v); }
    if (t.dataset.x === "close") { box.classList.remove("open"); btn.setAttribute("aria-expanded", "false"); return; }
    if (t.dataset.x === "new") { if (id) api("/api/ask", { id, stop: true }).catch(() => {}); id = null; last = null; clearTimeout(timer); qbox.hidden = true; intro(); }
  });
  ta.addEventListener("keydown", e => { if (e.key === "Enter" && !e.shiftKey) { e.preventDefault(); const v = ta.value; ta.value = ""; send(v); } });
  qbox.addEventListener("keydown", e => { if (e.key === "Enter" && e.target.matches("input") && e.target.value.trim()) answer(e.target.value.trim()); });
})();
