"use strict";
// Potluck chat: a full chat page for the model running on your computers.
// Chats, settings and usage live in this browser. The page talks to the model through the
// OpenAI-compatible API on this same address, and to Potluck through /potluck/*.

if (window.innerWidth <= 820) document.body.classList.add("side-closed"); // before anything is drawn, so a phone never sees the sidebar flash by
const $ = (s, r = document) => r.querySelector(s);
const $$ = (s, r = document) => [...r.querySelectorAll(s)];
const esc = MD.esc;
const isMac = /Mac|iPhone|iPad/.test(navigator.platform);
const MOD = isMac ? "⌘" : "Ctrl+";

const ICONS = {
  plus: "M12 5v14M5 12h14", search: "M11 4a7 7 0 1 0 0 14 7 7 0 0 0 0-14zM20 20l-4-4",
  side: "M4 5h16v14H4zM9 5v14", star: "M12 3.5l2.6 5.4 5.9.8-4.3 4.1 1 5.8L12 16.9 6.8 19.6l1-5.8L3.5 9.7l5.9-.8z",
  more: "M5 12h.01M12 12h.01M19 12h.01", clip: "M20 11.5l-8 8a5 5 0 0 1-7-7l8.5-8.5a3.3 3.3 0 0 1 4.7 4.7L9.8 17a1.7 1.7 0 0 1-2.4-2.4l7.6-7.6",
  mic: "M12 3a3 3 0 0 0-3 3v6a3 3 0 0 0 6 0V6a3 3 0 0 0-3-3zM5.5 11.5a6.5 6.5 0 0 0 13 0M12 18v3",
  send: "M12 19V5M6 11l6-6 6 6", stop: "M7 7h10v10H7z", copy: "M9 9h10v11H9zM5 15V4h10",
  retry: "M4 12a8 8 0 0 1 13.7-5.6L20 9M20 4v5h-5M20 12a8 8 0 0 1-13.7 5.6L4 15M4 20v-5h5",
  edit: "M4 20h4L19 9l-4-4L4 16zM13.5 6.5l4 4", speak: "M4 10v4h3l5 4V6L7 10zM16 9a4 4 0 0 1 0 6M18.5 6.5a8 8 0 0 1 0 11",
  x: "M6 6l12 12M18 6L6 18", folder: "M3 7a2 2 0 0 1 2-2h4l2 2h8a2 2 0 0 1 2 2v8a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2z",
  file: "M7 3h7l4 4v14H7zM14 3v4h4", shield: "M12 3l7 3v5c0 4.5-3 8.3-7 9.5C8 19.3 5 15.5 5 11V6z",
  bulb: "M9 18h6M10 21h4M12 3a6 6 0 0 0-4 10.5c.7.7 1 1.5 1 2.5h6c0-1 .3-1.8 1-2.5A6 6 0 0 0 12 3z",
  chip: "M8 8h8v8H8zM4 10h4M4 14h4M16 10h4M16 14h4M10 4v4M14 4v4M10 16v4M14 16v4", check: "M5 12.5l4.5 4.5L19 7.5",
  trash: "M5 7h14M10 7V4h4v3M7 7l1 13h8l1-13", term: "M4 5h16v14H4zM7 9l3 3-3 3M12 15h5", down: "M6 9l6 6 6-6", tool: "M14 6a4 4 0 0 0 5 5l-8.5 8.5a2.1 2.1 0 0 1-3-3L16 8",
};
const icon = n => `<svg viewBox="0 0 24 24" aria-hidden="true"><path d="${ICONS[n]}"/></svg>`;
const ic = n => `<span class="ic">${icon(n)}</span>`;

const STYLES = {
  normal: { name: "Normal", text: "" },
  concise: { name: "Concise", text: "Keep answers short and to the point. Skip preamble and repetition." },
  explain: { name: "Explanatory", text: "Explain clearly, step by step, as a patient teacher would, with a short example where it helps." },
  formal: { name: "Formal", text: "Write in a formal, professional register." },
};
const PERMS = {
  none: { name: "Chat only", d: "The model cannot look at or change any files." },
  ask: { name: "Ask every time", d: "Ask me before each look at, or change to, my folder." },
  read: { name: "Read freely", d: "Read my folder without asking. Ask before changing anything." },
  edit: { name: "Edit freely", d: "Read and change files in my folder without asking." },
};
// Agent mode: the model may run commands and scripts on the computer running Potluck.
const AGENT = {
  off: { name: "Agent off", d: "The model cannot run anything on this computer." },
  ask: { name: "Agent: ask first", d: "The model may run commands and drive apps. You see each one and say yes or no first." },
  auto: { name: "Agent: run freely", d: "The model runs commands and drives apps without asking. It can change or delete anything you can. Only for tasks you trust it with." },
};
const DEFAULTS = { agent: "off", theme: "auto", style: "normal", instructions: "", perm: "read", thinking: false, enterSends: true, speak: false, autoSend: false, temperature: "", maxTokens: "", sideClosed: false };

const S = {
  settings: { ...DEFAULTS }, chats: [], cur: null, server: null, props: null, models: null,
  gen: null,        // {abort, chatId} while a reply is being written
  pending: [],      // attachments waiting in the composer
  folder: null,     // the working folder of the current chat
  usage: { total: { in: 0, out: 0, ms: 0, replies: 0 }, days: {} },
  rec: null, slashSel: 0, filter: "",
};

// ---------- small helpers ----------
const uid = () => Date.now().toString(36) + Math.random().toString(36).slice(2, 8);
const num = n => Math.round(n || 0).toLocaleString();
const bytes = n => n >= 1 << 20 ? (n / (1 << 20)).toFixed(1) + " MB" : n >= 1024 ? Math.round(n / 1024) + " KB" : n + " bytes";
const gb = n => (n / 1e9).toFixed(1) + " GB";
const today = () => new Date().toISOString().slice(0, 10);
const estTokens = chars => Math.ceil(chars / 3.5);
const dur = ms => ms < 60000 ? Math.round(ms / 1000) + " s" : ms < 3600000 ? Math.round(ms / 60000) + " min" : (ms / 3600000).toFixed(1) + " h";
let toastTimer;
function toast(t) { const el = $("#toast"); el.textContent = t; el.classList.add("show"); clearTimeout(toastTimer); toastTimer = setTimeout(() => el.classList.remove("show"), 2600); }
async function copyText(t) { try { await navigator.clipboard.writeText(t); toast("Copied"); } catch { toast("Could not copy"); } }
function download(name, text, type = "text/plain") { const a = document.createElement("a"); a.href = URL.createObjectURL(new Blob([text], { type })); a.download = name; a.click(); setTimeout(() => URL.revokeObjectURL(a.href), 2000); }
const saveSettings = () => Store.set("settings", S.settings);
function applyTheme() { const t = S.settings.theme; if (t === "auto") document.documentElement.removeAttribute("data-theme"); else document.documentElement.dataset.theme = t; }

// ---------- Potluck and the model ----------
async function api(path, body) {
  const r = await fetch(path, body ? { method: "POST", headers: { "Content-Type": "application/json", "X-Potluck": "1" }, body: JSON.stringify(body) } : {});
  const j = await r.json().catch(() => ({}));
  if (!r.ok) throw new Error(j.error || r.statusText);
  return j;
}
const running = () => !!S.settings.extModel || (S.server && (S.server.phase === "running" || S.server.phase === "resting")); // resting: loads again with the next message
const ctxSize = () => (S.props && S.props.default_generation_settings && S.props.default_generation_settings.n_ctx) || (S.server && S.server.ctx) || 4096;
const modelTitle = () => S.settings.extModel ? (S.settings.extTitle || S.settings.extModel) : (S.server && S.server.model && (S.server.model.title || S.server.model.file)) || "";
// A model from one of the person's other AI setups (LM Studio, Ollama, a router…), when chosen.
const extModel = () => S.settings.extModel ? { model: S.settings.extModel } : {};

let pollTimer;
async function poll() {
  clearTimeout(pollTimer);
  const was = S.server && S.server.phase;
  try { S.server = await api("/potluck/state"); } catch { S.server = { phase: "offline", error: "Potluck is not answering." }; }
  if (running() && (was !== "running" || !S.props)) { try { S.props = await (await fetch("/props")).json(); } catch { S.props = null; } }
  if (!running()) S.props = null;
  // The setup the user brought over from their earlier AI (Computers > Bring your AI setup
  // over). Only this computer's own browser gets it.
  if (S.server.owner) loadTasks();
  if (S.server.owner && (!S.ctxAt || Date.now() - S.ctxAt > 60000)) { S.ctxAt = Date.now(); try { S.ctx = await api("/potluck/context"); } catch { S.ctx = null; } drawProjects(); }
  drawStatus();
  if (was !== S.server.phase && S.cur && !S.cur.messages.length && !S.gen) drawThread(); // the greeting says whether a model is ready
  pollTimer = setTimeout(poll, running() ? 12000 : 3000);
}

function drawStatus() {
  const s = S.server || {}, chipEl = $("#cluster-chip"), b = $("#banner");
  const helpers = (s.nodes || []).filter(n => n.role === "worker").length;
  let dot = "", line = "", sub = "";
  if (s.phase === "running") { dot = "ok"; line = modelTitle(); sub = helpers ? `Running on ${helpers + 1} computers` : "Running on one computer"; }
  else if (s.phase === "resting") { dot = "ok"; line = modelTitle(); sub = "Resting to free memory · wakes with your message"; }
  else if (s.phase === "starting") { dot = "busy"; line = modelTitle() || "Starting"; sub = "Getting ready…"; }
  else if (s.phase === "error") { dot = "bad"; line = "Could not start"; sub = "See the note above the chat"; }
  else if (s.phase === "offline") { dot = "bad"; line = "Not connected"; sub = "Potluck is not answering"; }
  else { line = "No model running"; sub = s.owner ? "Choose one to begin" : "Ask the owner to start one"; }
  chipEl.innerHTML = `<span class="dot ${dot}"></span><span><b>${esc(line)}</b><small>${esc(sub)}</small></span>`;
  $("#panel-link").hidden = !s.owner; if (s.owner) $("#panel-link").href = s.panel;
  // banner above the thread when the model is not ready
  if (S.compacting) { b.hidden = false; b.className = ""; b.innerHTML = `<span class="waiting"><span class="spin"></span><span>Summarizing this chat so it can continue in a fresh one. This takes a minute.</span></span>`; }
  else if (s.phase === "running" || s.phase === "resting") b.hidden = true;
  else {
    b.hidden = false; b.className = s.phase === "error" || s.phase === "offline" ? "bad" : "";
    if (s.phase === "starting") {
      const steps = (s.steps || []).map(x => `<li class="${esc(x.state)}">${esc(x.text)}</li>`).join("");
      const prog = (s.nodes || []).filter(n => n.total > 0).map(n => `<div class="small">${esc(n.name)}: ${esc(n.status || "")}<div class="bar"><i style="width:${Math.min(100, 100 * n.done / n.total).toFixed(1)}%"></i></div></div>`).join("");
      b.innerHTML = `<b>Getting ${esc(modelTitle() || "the model")} ready.</b> You can type your message meanwhile.<ul class="steps">${steps}</ul>${prog}`;
    } else if (s.phase === "error") b.innerHTML = `<b>The model could not start.</b> ${esc(s.error || "")} ${s.owner ? `<button class="btn small ghost" data-a="models">Choose a model</button>` : ""}`;
    else if (s.phase === "offline") b.innerHTML = `<b>Potluck is not answering.</b> Check that it is still open on the main computer.`;
    else b.innerHTML = `<b>No model is running.</b> ${esc(s.pauseWhy || "")} ${s.owner ? `<button class="btn small" data-a="models">Choose a model</button>` : "Ask the person who runs Potluck to start one."}`;
  }
  drawTools();
}

// ---------- chats ----------
function newChat() { return { id: uid(), title: "", titled: false, created: Date.now(), updated: Date.now(), starred: false, messages: [], totals: { in: 0, out: 0, ms: 0, replies: 0 }, used: 0, grants: {}, summary: "" }; }
async function saveChat(c = S.cur) {
  if (!c || !c.messages.length && !c.summary) return;
  c.updated = Date.now();
  await Store.putChat(c);
  const i = S.chats.findIndex(x => x.id === c.id);
  const meta = { id: c.id, title: c.title, updated: c.updated, starred: c.starred, text: c.messages.filter(m => m.role !== "tool").map(m => m.content || "").join(" ").slice(0, 4000).toLowerCase() };
  if (i >= 0) S.chats[i] = meta; else S.chats.unshift(meta);
  drawList();
}
// ---------- connectors: the user's MCP servers (mcp.go) ----------
// Each connector is switched on once (Settings is per browser): that is the up-front
// permission to start it. Its tools then follow the agent mode's ask / run-freely choice.
const connOn = name => !!(S.settings.connectors || {})[name];
const conns = () => ((S.ctx && S.ctx.connectors) || []).filter(k => k.kind === "mcp-stdio" || k.kind === "mcp-http");
async function loadMCP(force) {
  const root = S.cur && S.cur.project ? S.cur.project.root : "";
  if (!S.server || !S.server.owner || !conns().some(k => connOn(k.name))) { S.mcp = { tools: [], problems: [], root }; return; }
  if (!force && S.mcp && S.mcp.root === root && Date.now() - S.mcp.at < 600000) return;
  try {
    const r = await fetch("/potluck/mcp", { method: "POST", headers: { "Content-Type": "application/json", "X-Potluck": "1" }, body: JSON.stringify({ op: "tools", root, servers: conns().filter(k => connOn(k.name)).map(k => k.name) }) });
    const j = await r.json(); if (!r.ok) throw new Error(j.error || "Potluck could not start the connectors.");
    S.mcp = { tools: (j.tools || []).filter(t => connOn(t.server)), problems: j.problems || [], root, at: Date.now() };
    if (S.mcp.problems.length) toast("Some connectors did not start: " + S.mcp.problems.join("; "));
  } catch (e) { S.mcp = { tools: [], problems: [e.message], root, at: Date.now() }; toast(e.message); }
}
const mcpName = t => ("mcp__" + t.server + "__" + t.name).replace(/[^A-Za-z0-9_-]/g, "_").slice(0, 64);
function connMenu(a) {
  const ks = conns();
  const h = `<h5>Connectors: which of your MCP servers may the model use?</h5>` + (ks.length ? ks.map(k => mi("c:" + k.name, esc(k.name), { on: connOn(k.name), check: 1, d: esc(k.kind === "mcp-http" ? k.url || "" : [k.command, ...(k.args || [])].join(" ")) + (k.source ? " · from " + esc(k.source) : "") })).join("") + `<hr>` + mi("all", "Switch them all on", { icon: "check" }) + mi("none", "Switch them all off", { icon: "x" })
    : `<div class="note">No connectors yet. Bring them over in the control panel: Computers, then "Bring your AI setup over".</div>`) +
    `<div class="note">Switching one on lets Potluck start it on this computer. Using its tools then asks you first, unless agent mode is set to run freely.</div>`;
  showPop(a, h, async v => {
    const c = { ...(S.settings.connectors || {}) };
    if (v === "all") ks.forEach(k => c[k.name] = true); else if (v === "none") ks.forEach(k => c[k.name] = false); else if (v.startsWith("c:")) c[v.slice(2)] = !c[v.slice(2)];
    S.settings.connectors = c; saveSettings(); drawTools();
    await loadMCP(true); drawTools();
    if (S.mcp.tools.length) toast(`${S.mcp.tools.length} tools ready from ${new Set(S.mcp.tools.map(t => t.server)).size} connector(s).`);
  });
}
function askConnector(call, tool) {
  return new Promise(resolve => {
    const el = $(`.tool[data-call="${CSS.escape(call.id)}"]`); if (!el) return resolve("deny");
    el.classList.add("ask"); el.open = true;
    $(".out", el).innerHTML = `<b>The model wants to use ${esc(tool.name)} from the connector ${esc(tool.server)}:</b><pre class="cmd">${esc(String(call.args || "{}").slice(0, 3000))}</pre>`;
    const row = document.createElement("div"); row.className = "ask-row";
    row.innerHTML = `<button class="btn small" data-v="allow">Allow</button><button class="btn small ghost" data-v="always">Always allow ${esc(tool.server)} in this chat</button><button class="btn small ghost" data-v="deny">Don't allow</button>`;
    el.appendChild(row); scrollDown(true);
    row.addEventListener("click", e => { const v = e.target.dataset.v; if (!v) return; row.remove(); el.classList.remove("ask"); resolve(v); });
    S.gen.cancelAsk = () => { row.remove(); resolve("deny"); };
  });
}

// ---------- tasks: project chats run inside Potluck (sessions.go) ----------
// The task keeps going with this page closed. The page follows it through a live event stream
// and answers its questions: Yes (Enter), No (Backspace or Delete), or your own words typed in
// the message box.
const isTask = () => !!(S.cur && (S.cur.project || S.cur.task));
function taskPolicy() {
  return { files: S.settings.perm === "none" ? "ask" : S.settings.perm, commands: S.settings.agent === "auto" ? "auto" : "ask",
    connectors: { ...(S.settings.connectors || {}) }, all: !!(S.cur && S.cur.approveAll), thinking: !!S.settings.thinking };
}
async function taskApi(path, body) {
  const r = await fetch("/potluck/" + path, body === undefined ? {} : { method: "POST", headers: { "Content-Type": "application/json", "X-Potluck": "1" }, body: JSON.stringify(body) });
  const j = await r.json().catch(() => ({}));
  if (!r.ok) throw new Error(j.error || "Potluck could not do that.");
  return j;
}
const fromSMsg = m => m.role === "tool" ? { id: uid(), role: "tool", callId: m.callId, content: m.content, state: m.state } :
  { id: uid(), role: m.role, content: m.content || "", reasoning: m.reasoning || "", calls: (m.calls || []).map(c => ({ id: c.id, name: c.name, args: c.args })), t: Date.parse(m.at) || Date.now() };
async function taskLoad() {
  closeTask();
  if (!isTask() || !S.cur.session) return;
  try {
    const ss = await taskApi("sessions/" + S.cur.session);
    S.cur.messages = (ss.messages || []).map(fromSMsg); S.task = { status: ss.status, error: ss.error, q: ss.question, live: null };
    drawThread(); scrollDown(true);
  } catch (e) { S.task = { status: "error", error: "This task is gone from Potluck: " + e.message }; }
  taskFollow();
}
function closeTask() { if (S.es) { S.es.close(); S.es = null; } S.task = S.task && isTask() ? S.task : null; }
function taskFollow() {
  if (!S.cur.session || S.es) return;
  const id = S.cur.session, chat = S.cur;
  S.es = new EventSource("/potluck/sessions/" + id + "/events");
  S.es.onmessage = e => {
    if (S.cur !== chat) return;
    const ev = JSON.parse(e.data), t = S.task = S.task || {};
    if (ev.type === "delta") {
      if (!t.live) { t.live = { id: uid(), role: "assistant", content: "", reasoning: "", calls: [], t: Date.now() }; chat.messages.push(t.live); drawThread(); }
      t.live.content += ev.text || ""; t.live.reasoning += ev.reasoning || "";
      const i = chat.messages.indexOf(t.live), el = $(`.msg[data-i="${i}"] .body`);
      if (el) el.innerHTML = assistantHTML(t.live, true); scrollDown();
    } else if (ev.type === "message") {
      const m = fromSMsg(ev.msg);
      if (ev.msg.role === "assistant" && t.live) chat.messages[chat.messages.indexOf(t.live)] = m; else if (ev.msg.role !== "user") chat.messages.push(m);
      t.live = null; drawThread(); scrollDown(); saveChat(chat);
    } else if (ev.type === "question") {
      t.q = ev.question && !ev.question.answer ? ev.question : null; drawThread(); drawTools(); scrollDown(true);
      if (t.q) { $("#input").focus(); notifyQuestion(t.q); }
    } else if (ev.type === "status") {
      t.status = ev.status; t.error = ev.error; drawTools(); drawThread();
    }
  };
}
async function taskSend(text) {
  const c = S.cur;
  if (S.task && S.task.q) return taskAnswer(text);
  c.messages.push({ id: uid(), role: "user", content: text, t: Date.now() });
  if (!c.title || (c.project && c.title === c.project.name)) c.title = (c.project && c.project.name ? c.project.name + ": " : "") + titleFrom(text);
  $("#input").value = ""; autosize(); drawHeader(); drawThread(); scrollDown(true);
  try {
    if (!c.session) { const ss = await taskApi("sessions", { project: c.project && c.project.root ? c.project : null, policy: taskPolicy(), text }); c.session = ss.id; }
    else await taskApi("sessions/" + c.session + "/message", { text });
    S.task = { status: "working" }; await saveChat(c); taskFollow(); drawTools(); drawThread();
  } catch (e) { toast(e.message); }
}
async function taskAnswer(answer) {
  const q = S.task && S.task.q; if (!q) return;
  try { await taskApi("sessions/" + S.cur.session + "/answer", { question: q.id, answer }); S.task.q = null; $("#input").value = ""; autosize(); drawThread(); drawTools(); }
  catch (e) { toast(e.message); }
}
async function taskStop() { if (S.cur.session) { try { await taskApi("sessions/" + S.cur.session + "/stop", {}); } catch (e) { toast(e.message); } } }
async function taskPolicySync() { if (isTask() && S.cur.session) { try { await taskApi("sessions/" + S.cur.session + "/policy", { policy: taskPolicy() }); } catch { } } }
function questionCard(q) {
  const kinds = { run: "term", write: "edit", read: "file", connector: "tool", ask: "bulb", plan: "shield" };
  return `<div class="wrap"><div class="qcard" role="alertdialog" aria-label="${esc(q.title)}"><div class="qhead">${ic(kinds[q.kind] || "bulb")}<b>${esc(q.title)}</b></div>` +
    (q.detail ? `<pre class="cmd">${esc(q.detail)}</pre>` : "") +
    `<div class="qrow"><button class="btn" data-a="q-yes">Yes <kbd>Enter</kbd></button><button class="btn no" data-a="q-no">No <kbd>⌫</kbd></button>` +
    (q.kind !== "ask" && q.kind !== "plan" ? `<button class="btn ghost small" data-a="q-always">Yes, and don't ask again in this task</button>` : "") +
    `</div><div class="small muted">Or type your own answer in the message box and press Enter.</div></div></div>`;
}
// A question while the page is in the background: a desktop notification (phones get theirs from Potluck).
function notifyQuestion(q) {
  if (document.visibilityState === "visible" || !("Notification" in window)) return;
  const show = () => { try { const n = new Notification("Potluck needs you", { body: q.title, tag: q.id }); n.onclick = () => { window.focus(); n.close(); }; } catch { } };
  if (Notification.permission === "granted") show(); else if (Notification.permission !== "denied") Notification.requestPermission().then(p => p === "granted" && show());
}
function approveAllSheet() {
  sheet(`<h3>Approve everything in this task?</h3><p>The model will run commands, change and delete files, use your connectors and control apps <b>without asking you anything</b>, until you switch this off. It can do anything you can do on this computer, and mistakes or a misunderstanding can't be taken back.</p><p class="muted small">This is meant for tasks you would leave running unattended and trust the model with. It is experimental.</p><div class="foot"><button class="btn ghost" data-a="close">Cancel</button><button class="btn danger" data-a="yes">I understand, approve everything</button></div>`,
    { yes: () => { S.cur.approveAll = true; saveChat(S.cur); taskPolicySync(); closeSheet(); drawTools(); toast("Approve everything is on for this task."); } });
}

// ---------- projects: chats that work inside a code project on this computer ----------
async function fsCall(body) {
  const r = await fetch("/potluck/fs", { method: "POST", headers: { "Content-Type": "application/json", "X-Potluck": "1" }, body: JSON.stringify(body), signal: S.gen && S.gen.abort.signal });
  const j = await r.json().catch(() => ({}));
  if (!r.ok) throw new Error(j.error || "Potluck could not do that.");
  return j;
}
async function loadProject() {
  S.proj = null;
  if (!S.cur || !S.cur.project || !S.cur.project.root) return;
  try { S.proj = await fsCall({ op: "project", root: S.cur.project.root }); } catch (e) { S.proj = { root: S.cur.project.root, name: S.cur.project.name, error: e.message }; }
}
async function openProject(root, name) {
  if (S.gen) return toast("Wait for the reply to finish, or stop it first.");
  S.cur = newChat(); S.cur.project = { root, name: name || root.split(/[\\/]/).pop() }; S.cur.title = S.cur.project.name;
  S.pending = []; S.folder = null;
  await loadProject();
  closeTask(); S.task = null;
  drawAll(); $("#input").focus();
  if (window.innerWidth <= 820) document.body.classList.add("side-closed");
}
// Tasks running in Potluck (from this page, the phone or elsewhere), waiting ones first.
async function loadTasks() {
  if (!S.server || !S.server.owner) return;
  try { S.tasks = (await taskApi("sessions")).sessions || []; } catch { S.tasks = []; }
  drawProjects();
}
async function openTask(id) {
  if (S.gen) return toast("Wait for the reply to finish, or stop it first.");
  const all = await Store.allChats();
  let c = all.find(x => x.session === id);
  if (!c) {
    const t = (S.tasks || []).find(x => x.id === id) || {};
    c = newChat(); c.session = id; c.title = t.title || "Task"; c.project = t.project || null; c.task = true;
  }
  S.cur = c; S.cur.grants = S.cur.grants || {}; S.pending = []; S.folder = null;
  await loadProject(); S.task = null; await taskLoad(); drawAll();
}
function newTaskChat() {
  if (S.gen) return toast("Wait for the reply to finish, or stop it first.");
  S.cur = newChat(); S.cur.task = true; S.cur.project = null; S.pending = []; S.folder = null; closeTask(); S.task = null; S.proj = null;
  drawAll(); $("#input").focus(); toast("New task: it runs in Potluck and keeps going if you close this page.");
}
function drawProjects() {
  const el = $("#project-list"); if (!el) return;
  const ps = ((S.ctx && S.ctx.projects) || []).filter(p => p.path);
  if (!S.server || !S.server.owner) { el.innerHTML = ""; return; }
  const ts = (S.tasks || []).slice().sort((a, b) => (b.status === "waiting") - (a.status === "waiting") || Date.parse(b.updated) - Date.parse(a.updated)).slice(0, 6);
  const dot = st => st === "waiting" ? `<span class="tdot wait" title="Waiting for you"></span>` : st === "working" ? `<span class="tdot work" title="Working"></span>` : `<span class="tdot" title="${esc(st)}"></span>`;
  const tasksHTML = `<h4>Tasks <button class="icon small" data-a="new-task" title="New task" aria-label="New task">${ic("plus")}</button></h4>` +
    (ts.length ? ts.map(t => `<div class="chat-item ${S.cur && S.cur.session === t.id ? "on" : ""}" data-task="${esc(t.id)}" tabindex="0" role="button" title="${esc(t.question ? "Waiting: " + t.question.title : t.status)}">${dot(t.status)}<span class="t">${esc(t.title || "Task")}</span></div>`).join("") : `<div class="empty-list small">Tasks keep going in Potluck when you close this page.</div>`);
  el.innerHTML = tasksHTML + `<h4>Projects</h4>` + ps.slice(0, 6).map(p => `<div class="chat-item proj ${S.cur && S.cur.project && S.cur.project.root === p.path ? "on" : ""}" data-proj="${esc(p.path)}" data-name="${esc(p.name)}" tabindex="0" role="button" title="${esc(p.path)}"><span class="ic">${icon("folder")}</span><span class="t">${esc(p.name)}</span></div>`).join("") +
    `<div class="chat-item proj" data-a="all-projects" tabindex="0" role="button"><span class="t muted">${ps.length > 6 ? `All ${ps.length} projects…` : "Open a project…"}</span></div>`;
}
function projectsSheet() {
  const ps = ((S.ctx && S.ctx.projects) || []).filter(p => p.path);
  sheet(`<h3>Work on a project</h3><p class="muted small">The agent works inside the project's folder: it reads and edits files, runs its commands and tests, and follows its CLAUDE.md or AGENTS.md.${ps.length ? "" : " Bring your projects over in the control panel (Computers, then Bring your AI setup over), or type a folder below."}</p>
    <input id="proj-q" type="search" placeholder="Search projects, or type a folder path" style="width:100%;margin:8px 0">
    <div id="proj-all" style="max-height:50vh;overflow:auto"></div>
    <div class="foot"><button class="btn ghost" data-a="close">Cancel</button><button class="btn" data-a="path">Open this folder</button></div>`, {
    path: () => { const v = $("#proj-q").value.trim(); if (!/^(\/|[A-Za-z]:\\|~)/.test(v)) return toast("Type the folder's full path."); closeSheet(); openProject(v); },
  });
  const draw = () => {
    const q = ($("#proj-q").value || "").toLowerCase();
    $("#proj-all").innerHTML = ps.filter(p => !q || p.name.toLowerCase().includes(q) || p.path.toLowerCase().includes(q)).slice(0, 200).map(p => `<div class="chat-item" data-pick="${esc(p.path)}" data-name="${esc(p.name)}" role="button" tabindex="0"><span class="t"><b>${esc(p.name)}</b> <span class="muted small">${esc(p.path)}</span></span></div>`).join("") || `<p class="muted small">No project matches.</p>`;
  };
  $("#proj-q").addEventListener("input", draw); draw();
  $("#proj-all").addEventListener("click", e => { const it = e.target.closest("[data-pick]"); if (it) { closeSheet(); openProject(it.dataset.pick, it.dataset.name); } });
}

async function openChat(id) {
  if (S.gen) return toast("Wait for the reply to finish, or stop it first.");
  const c = id ? await Store.getChat(id) : null;
  S.cur = c || newChat(); S.cur.grants = S.cur.grants || {};
  S.pending = []; S.folder = null;
  await restoreFolder();
  await loadProject();
  S.task = null; await taskLoad();
  drawAll();
  $("#input").focus();
  if (window.innerWidth <= 820) document.body.classList.add("side-closed");
}
function titleFrom(text) { const t = text.replace(/\s+/g, " ").trim(); return t.length > 48 ? t.slice(0, 46).replace(/\s+\S*$/, "") + "…" : t || "New chat"; }
async function deleteChat(id) {
  await Store.delChat(id); await Store.del("folder:" + id);
  S.chats = S.chats.filter(c => c.id !== id);
  if (S.cur && S.cur.id === id) { S.gen && stop(); S.cur = newChat(); S.folder = null; S.pending = []; drawAll(); } else drawList();
}

function drawList() {
  const f = S.filter.trim().toLowerCase();
  const list = S.chats.filter(c => !f || (c.title || "").toLowerCase().includes(f) || (c.text || "").includes(f)).sort((a, b) => b.updated - a.updated);
  const day = 86400000, now = Date.now(), start = new Date().setHours(0, 0, 0, 0);
  const groups = [["Starred", c => c.starred], ["Today", c => c.updated >= start], ["Yesterday", c => c.updated >= start - day], ["Last 7 days", c => c.updated >= now - 7 * day], ["Older", () => true]];
  const used = new Set(); let h = "";
  for (const [name, test] of groups) {
    const items = list.filter(c => !used.has(c.id) && test(c)); items.forEach(c => used.add(c.id));
    if (!items.length) continue;
    h += `<h4>${name}</h4>` + items.map(c => `<div class="chat-item ${S.cur && c.id === S.cur.id ? "on" : ""}" data-id="${c.id}" tabindex="0" role="button">${c.starred ? `<span class="ic star">${icon("star")}</span>` : ""}<span class="t">${esc(c.title || "New chat")}</span><button class="icon" data-a="chat-more" data-id="${c.id}" title="More" aria-label="More">${ic("more")}</button></div>`).join("");
  }
  $("#chat-list").innerHTML = h || `<div class="empty-list">${f ? "No chats match." : "Your chats will appear here. They are kept in this browser only."}</div>`;
}

// ---------- thread ----------
function statLine(st) {
  if (!st) return "";
  const p = [];
  if (st.out) p.push(`${num(st.out)} tokens`);
  if (st.tps) p.push(`${st.tps.toFixed(1)}/s`);
  if (st.fresh && st.ptps) p.push(`read ${num(st.fresh)}${st.fresh < st.in ? " new" : ""} at ${st.ptps.toFixed(1)}/s`);
  if (st.stopped) p.push("stopped");
  return p.join(" · ");
}
function toolLabel(c, res) {
  let a = {}; try { a = JSON.parse(c.args || "{}"); } catch { }
  const p = `<span class="path">${esc(a.path || "a file")}</span>`, q = `<span class="path">${esc(a.query || "")}</span>`, where = a.path ? "in " + p : "in your folder";
  const forms = { // while it happens, once done, when not allowed
    list_files: [`Looking at the files ${where}`, `Looked at the files ${where}`, `Asked to look at the files ${where}`],
    read_file: [`Reading ${p}`, `Read ${p}`, `Asked to read ${p}`],
    search_files: [`Searching your folder for ${q}`, `Searched your folder for ${q}`, `Asked to search your folder for ${q}`],
    write_file: [`Writing ${p}`, `Wrote ${p}`, `Asked to write ${p}`],
    edit_file: [`Editing ${p}`, `Edited ${p}`, `Asked to edit ${p}`],
    run_command: [`Running <span class="path">${esc(shortCmd(a.command))}</span>`, `Ran <span class="path">${esc(shortCmd(a.command))}</span>`, `Asked to run <span class="path">${esc(shortCmd(a.command))}</span>`],
    use_skill: [`Loading the skill <span class="path">${esc(a.name || "")}</span>`, `Loaded the skill <span class="path">${esc(a.name || "")}</span>`, `Asked to load the skill <span class="path">${esc(a.name || "")}</span>`],
    run_applescript: [`Driving an app: <span class="path">${esc(shortCmd(a.script))}</span>`, `Drove an app: <span class="path">${esc(shortCmd(a.script))}</span>`, `Asked to drive an app: <span class="path">${esc(shortCmd(a.script))}</span>`],
  }[c.name] || (c.name.startsWith("mcp__") ? (n => [`Using ${n}`, `Used ${n}`, `Asked to use ${n}`])(esc(c.name.slice(5).replace("__", ": "))) : [`Using ${esc(c.name)}`, `Used ${esc(c.name)}`, `Asked to use ${esc(c.name)}`]);
  return forms[!res ? 0 : res.state === "ok" ? 1 : 2];
}
function shortCmd(s) { s = String(s || "").replace(/\s+/g, " ").trim(); return s.length > 70 ? s.slice(0, 68) + "…" : s; }
function toolCard(c, res) {
  const state = !res ? `<span class="state"><span class="spin"></span></span>` : res.state === "denied" ? `<span class="state no">Not allowed</span>` : res.state === "error" ? `<span class="state no">Did not work</span>` : `<span class="state ok">Done</span>`;
  return `<details class="tool" data-call="${esc(c.id)}"><summary>${ic(c.name === "write_file" || c.name === "edit_file" ? "edit" : c.name.startsWith("run_") ? "term" : "tool")}<span>${toolLabel(c, res)}</span>${state}</summary><div class="out">${esc(res ? res.content : "Working…")}</div></details>`;
}
function assistantHTML(m, live) {
  let h = "";
  if (m.reasoning) h += `<details class="think"${live && !m.content ? " open" : ""}><summary>${ic("bulb")}<span>${live && !m.content ? "Thinking…" : "Thought about it"}</span></summary><div class="t">${esc(m.reasoning)}</div></details>`;
  if (m.content) h += MD.render(m.content);
  return h;
}
function drawThread() {
  const c = S.cur, el = $("#thread");
  if (!c.messages.length) {
    const pooled = ((S.server && S.server.nodes) || []).some(n => n.role === "worker");
    const sugg = c.project && c.project.root
      ? ["Explain how this project works and where it stands", "Run the tests and fix whatever fails", "Find bugs and fix the worst ones", "Suggest the most valuable next improvement, then build it"]
      : [pooled ? "Explain how this model is running across my computers" : "What can you do on this computer?", "Help me plan my week", "Draft a polite email declining a meeting", "Summarize a file I attach"];
    el.innerHTML = (c.summary ? `<div class="wrap"><details class="think"><summary>${ic("file")}<span>Continued from an earlier chat. Its summary is included.</span></summary><div class="t">${esc(c.summary)}</div></details></div>` : "") +
      `<div class="hello"><h2>${c.project && c.project.root ? "What shall we do in " + esc(c.project.name) + "?" : c.task ? "What should this task do?" : "What shall we work on?"}</h2><p>${running() ? (pooled ? "Running privately on your own computers." : "Running privately on this computer.") : "Type away; your message is sent once a model is ready."}</p><div class="suggest">${sugg.map(s => `<button data-a="suggest">${esc(s)}</button>`).join("")}</div></div>`;
    return;
  }
  const results = {}; c.messages.forEach(m => { if (m.role === "tool") results[m.callId] = m; });
  let h = c.summary ? `<div class="wrap"><details class="think"><summary>${ic("file")}<span>Continued from an earlier chat. Its summary is included.</span></summary><div class="t">${esc(c.summary)}</div></details></div>` : "";
  const lastUser = c.messages.map(m => m.role).lastIndexOf("user");
  c.messages.forEach((m, i) => {
    if (m.role === "user") {
      const files = (m.files || []).map(f => `<span class="chip ${f.skipped ? "bad" : ""}" title="${esc(f.skipped || "")}">${ic("file")}<span class="n">${esc(f.name)}</span><small>${f.skipped ? "not included" : bytes(f.size)}</small></span>`).join("");
      h += `<div class="msg user ${i === lastUser ? "last" : ""}" data-i="${i}">${files ? `<div class="files">${files}</div>` : ""}${m.content ? `<div class="bubble">${esc(m.content)}</div>` : ""}<div class="acts">${m.branches && m.branches.length > 1 ? `<span class="versions"><button class="icon" data-a="branch" data-d="-1" title="Earlier version" aria-label="Earlier version" ${m.bi === 0 ? "disabled" : ""}>‹</button><span>${m.bi + 1} / ${m.branches.length}</span><button class="icon" data-a="branch" data-d="1" title="Later version" aria-label="Later version" ${m.bi === m.branches.length - 1 ? "disabled" : ""}>›</button></span>` : ""}<button class="icon" data-a="edit-msg" title="Edit and resend" aria-label="Edit and resend">${ic("edit")}</button><button class="icon" data-a="copy-msg" title="Copy" aria-label="Copy">${ic("copy")}</button></div></div>`;
    } else if (m.role === "assistant") {
      const next = c.messages[i + 1], endOfTurn = !next || next.role === "user";
      const live = S.gen && S.gen.msg === m;
      h += `<div class="msg assistant ${endOfTurn && i > lastUser ? "last" : ""}" data-i="${i}"><div class="body">${assistantHTML(m, live)}</div>${(m.calls || []).map(cl => toolCard(cl, results[cl.id])).join("")}` +
        (m.error ? `<div class="err">${esc(m.error)}</div>` : "") +
        (live ? `<div class="waiting" data-wait>${m.content || m.reasoning ? "" : `<span class="spin"></span><span>${esc(S.gen.note || "Reading your message…")}</span>`}</div>` :
          endOfTurn ? `<div class="acts"><button class="icon" data-a="copy-msg" title="Copy" aria-label="Copy">${ic("copy")}</button><button class="icon" data-a="retry" title="Try again" aria-label="Try again">${ic("retry")}</button><button class="icon" data-a="speak" title="Read aloud" aria-label="Read aloud">${ic("speak")}</button><span class="stat">${esc(statLine(m.stats))}</span></div>` : "") + `</div>`;
    }
  });
  if (isTask() && S.task) {
    if (S.task.q) h += questionCard(S.task.q);
    else if (S.task.status === "working" && !S.task.live) h += `<div class="wrap"><div class="waiting"><span class="spin"></span><span>${esc(S.task.error || "Working on it. You can close this page; the task keeps going in Potluck.")}</span></div></div>`;
    else if (S.task.status === "error" || S.task.status === "stopped") h += `<div class="wrap"><div class="err">${esc(S.task.error || (S.task.status === "stopped" ? "Stopped." : "Something went wrong."))}</div></div>`;
  }
  el.innerHTML = h;
}
function scrollDown(force) { const el = $("#thread"); if (force || el.scrollHeight - el.scrollTop - el.clientHeight < 160) el.scrollTop = el.scrollHeight; }
let rafPending = false;
function drawLive() { // redraw only the message being written
  if (rafPending) return; rafPending = true;
  requestAnimationFrame(() => {
    rafPending = false;
    if (!S.gen || !S.gen.msg || S.gen.chatId !== S.cur.id) return; // nothing being written (a permission question may be on screen)
    const i = S.cur.messages.indexOf(S.gen.msg), el = $(`.msg[data-i="${i}"]`);
    if (!el) return;
    const m = S.gen.msg, open = $(".think", el) && $(".think", el).open;
    $(".body", el).innerHTML = assistantHTML(m, true);
    if (m.reasoning && m.content && $(".think", el)) $(".think", el).open = open && !m.content;
    const last = $(".body", el).lastElementChild; if (last && m.content) (last.tagName === "DIV" ? $("code", last) || last : last).classList.add("cursor");
    const w = $("[data-wait]", el); if (w) w.innerHTML = m.content || m.reasoning ? "" : `<span class="spin"></span><span>${esc(S.gen.note || "Reading your message…")}</span>`;
    scrollDown();
  });
}

// ---------- composer ----------
function drawChips() {
  let h = "";
  if (S.cur && S.cur.project && S.cur.project.root) h += `<span class="chip folder" title="${esc(S.cur.project.root)}">${ic("folder")}<span class="n">${esc(S.cur.project.name)}</span><small>${S.proj && S.proj.error ? esc(S.proj.error) : "project" + (S.proj && S.proj.instrFile ? " · follows " + esc(S.proj.instrFile) : "")}</small></span>`;
  if (S.folder) h += `<span class="chip folder" title="${S.folder.kind === "handle" ? "The model can work in this folder, within the permission level you chose." : "A read-only copy of this folder."}">${ic("folder")}<span class="n">${esc(S.folder.name)}</span><small>${S.folder.needsAccess ? "click to reconnect" : S.folder.kind === "snapshot" ? S.folder.files.size + " files, read-only" : "working folder"}</small><button type="button" data-a="detach" title="Remove folder" aria-label="Remove folder">×</button></span>`;
  h += S.pending.map((f, i) => `<span class="chip ${f.skipped ? "bad" : ""}" title="${esc(f.skipped || (f.pasted ? "Click to put this text back in the message box" : f.name))}"${f.pasted ? ` data-pasted="${i}" style="cursor:pointer"` : ""}>${ic("file")}<span class="n">${esc(f.name)}</span><small>${f.skipped ? "cannot be read" : f.image ? "picture" : "about " + num(estTokens(f.text.length)) + " tokens"}</small><button type="button" data-a="unattach" data-i="${i}" title="Remove" aria-label="Remove">×</button></span>`).join("");
  $("#chips").innerHTML = h;
  drawFine();
}
function drawFine() {
  const need = S.pending.reduce((n, f) => n + (f.skipped ? 0 : estTokens(f.text.length)), 0), ctx = ctxSize();
  let t = "";
  if (need + (S.cur ? S.cur.used : 0) > ctx * 0.9) t = `These attachments are about ${num(need)} tokens; this model remembers ${num(ctx)} per conversation. Attach less, or reload the model with a longer memory from the model menu.`;
  else if (S.rec) t = S.recLocal ? "Listening. Speech is recognised on this computer." : "Listening. Your browser's speech service turns your voice into text.";
  else if (S.folder && S.settings.perm !== "none" && S.cur && !S.cur.messages.length) t = "With a folder attached the first reply takes longer: the model first reads how to use it.";
  $("#fine").textContent = t;
}
function drawTools() {
  const s = S.settings, g = !!S.gen;
  $("#perm").innerHTML = `${ic("shield")}<span>${PERMS[s.perm].name}</span>`;
  $("#perm").hidden = !S.folder;
  const ag = $("#agent"), canAgent = S.server && S.server.owner;
  ag.hidden = !canAgent; ag.innerHTML = `${ic("term")}<span>${AGENT[s.agent] ? AGENT[s.agent].name : AGENT.off.name}</span>`;
  ag.classList.toggle("on", canAgent && s.agent !== "off"); ag.classList.toggle("warn", canAgent && s.agent === "auto");
  const cb = $("#conn"), nk = conns().length, non = conns().filter(k => connOn(k.name)).length;
  if (cb) { cb.hidden = !(S.server && S.server.owner) || !nk; cb.innerHTML = `${ic("tool")}<span>${non ? non + " connector" + (non > 1 ? "s" : "") : "Connectors"}</span>`; cb.classList.toggle("on", non > 0); }
  $("#think").innerHTML = `${ic("bulb")}<span>Think</span>`; $("#think").classList.toggle("on", s.thinking);
  $("#model").innerHTML = `<span>${esc(modelTitle() || "No model")}</span>${ic("down")}`;
  $("#mic").classList.toggle("rec", !!S.rec);
  const tw = isTask() && S.task && S.task.status === "working" && !S.task.q && !$("#input").value.trim();
  const send = $("#send"); send.classList.toggle("stop", g || tw); send.innerHTML = ic(g || tw ? "stop" : "send"); send.title = g || tw ? "Stop" : "Send";
  send.disabled = !g && !tw && !$("#input").value.trim() && !S.pending.some(f => !f.skipped);
  $("#input").placeholder = isTask() && S.task && S.task.q ? "Enter = Yes · Backspace = No · or type your own answer" : isTask() ? "Give the task to Potluck; it keeps going if you close this page" : "Message Potluck";
  if (isTask() && S.cur.approveAll) { ag.innerHTML = `${ic("term")}<span>Approving everything</span>`; ag.classList.add("warn"); }
  const used = S.cur ? S.cur.used : 0, frac = Math.min(1, used / ctxSize()), C = 2 * Math.PI * 9;
  const ring = $("#usage"); ring.className = "ring" + (frac > 0.92 ? " full" : frac > 0.75 ? " warn" : "");
  ring.innerHTML = `<svg viewBox="0 0 24 24"><circle class="bg" cx="12" cy="12" r="9"/><circle class="fg" cx="12" cy="12" r="9" stroke-dasharray="${C}" stroke-dashoffset="${C * (1 - frac)}"/></svg>`;
  ring.title = `Conversation memory ${Math.round(frac * 100)}% used`;
}
function drawHeader() {
  const c = S.cur;
  $("#chat-title").textContent = c.title || "New chat";
  $("#chat-star").innerHTML = ic("star"); $("#chat-star").classList.toggle("on", c.starred);
  document.title = (c.title ? c.title + " · " : "") + "Potluck chat";
}
function drawAll() { drawHeader(); drawList(); drawProjects(); drawThread(); drawChips(); drawTools(); scrollDown(true); }
function autosize() { const t = $("#input"); t.style.height = "auto"; t.style.height = Math.min(t.scrollHeight, window.innerHeight * 0.38) + "px"; }

// ---------- attachments ----------
const TEXT_LIMIT = 400000;
async function readAttachment(file) {
  const a = { name: file.webkitRelativePath || file.name, size: file.size, text: "" };
  if (/^image\//.test(file.type)) {
    if (!S.server || !S.server.vision) { a.skipped = "The model running now cannot look at pictures. Models loaded with their image add-on can."; return a; }
    if (file.size > 20 << 20) { a.skipped = "Bigger than 20 MB: too big a picture."; return a; }
    a.image = await new Promise((ok, no) => { const r = new FileReader(); r.onload = () => ok(r.result); r.onerror = no; r.readAsDataURL(file); });
    return a;
  }
  const kind = Extract.kindOf(file.name);
  if (kind) {
    if (file.size > 25 << 20) { a.skipped = "Bigger than 25 MB: too much to read here."; return a; }
    try { a.text = await Extract.text(kind, new Uint8Array(await file.arrayBuffer())); }
    catch (e) { a.skipped = e.message || "Its text could not be read."; return a; }
    if (!a.text.trim()) { a.skipped = kind === "pdf" ? "No readable text in this PDF. It may be a scan, or use a font this page cannot decode." : "This file has no text in it."; return a; }
    if (a.text.length > TEXT_LIMIT) a.text = a.text.slice(0, TEXT_LIMIT) + "\n[cut off here]";
    return a;
  }
  if (/\.(doc|xls|ppt|zip|gz|7z|rar|mp3|mp4|mov|wav|exe|dmg|gguf|bin|key|pages|numbers)$/i.test(file.name)) { a.skipped = "This kind of file cannot be read. Plain text, PDF, Word, Excel and PowerPoint files (the newer .docx, .xlsx, .pptx) can."; return a; }
  if (file.size > 2 << 20) { a.skipped = "Bigger than 2 MB: too much for the model to read at once."; return a; }
  const text = await file.text();
  if (/\u0000/.test(text.slice(0, 8000))) { a.skipped = "This is not a text file."; return a; }
  a.text = text.length > TEXT_LIMIT ? text.slice(0, TEXT_LIMIT) + "\n[cut off here]" : text;
  return a;
}
async function addFiles(files) {
  for (const f of files) S.pending.push(await readAttachment(f));
  const bad = S.pending.filter(f => f.skipped).length;
  if (bad) toast(bad === 1 ? "One file cannot be read; hover over it to see why." : bad + " files cannot be read; hover to see why.");
  drawChips(); drawTools();
}

// ----- the working folder -----
const SKIP_DIRS = new Set(["node_modules", ".git", ".hg", ".svn", "dist", "build", "__pycache__", ".venv", "venv", ".next", "target", ".DS_Store"]);
const cleanPath = p => { const parts = String(p || "").replace(/\\/g, "/").split("/").filter(x => x && x !== "."); if (parts.some(x => x === "..")) throw new Error("Paths must stay inside the folder."); return parts; };
async function pickFolder() {
  if (window.showDirectoryPicker) {
    let h; try { h = await window.showDirectoryPicker({ mode: S.settings.perm === "edit" ? "readwrite" : "read" }); } catch { return; }
    await setFolder({ name: h.name, kind: "handle", handle: h });
  } else $("#dir-input").click();
}
async function setFolder(f) {
  S.folder = f;
  if (f && f.kind === "handle") await Store.set("folder:" + S.cur.id, f.handle).catch(() => { });
  if (!f) await Store.del("folder:" + S.cur.id);
  S.cur.folderName = f ? f.name : "";
  if (f && S.settings.perm === "none") { S.settings.perm = "read"; saveSettings(); }
  drawChips(); drawTools();
  if (f) toast(f.kind === "handle" ? `${f.name} is now this chat's working folder.` : `${f.name}: ${f.files.size} files attached, read-only.`);
}
async function restoreFolder() {
  const h = await Store.get("folder:" + S.cur.id).catch(() => null);
  if (!h || !h.queryPermission) return;
  const ok = (await h.queryPermission({ mode: "read" })) === "granted";
  S.folder = { name: h.name, kind: "handle", handle: h, needsAccess: !ok };
}
async function reconnectFolder() {
  const f = S.folder; if (!f || !f.needsAccess) return true;
  const r = await f.handle.requestPermission({ mode: S.settings.perm === "edit" ? "readwrite" : "read" });
  f.needsAccess = r !== "granted"; drawChips(); return !f.needsAccess;
}
async function dirAt(parts, create) { let d = S.folder.handle; for (const p of parts) d = await d.getDirectoryHandle(p, { create }); return d; }
async function folderWalk(base, limit = 400, depth = 6) {
  const out = [], f = S.folder, root = cleanPath(base);
  if (f.kind === "snapshot") { const pre = root.length ? root.join("/") + "/" : ""; for (const [p, file] of f.files) if (p.startsWith(pre) && out.length < limit) out.push({ path: p, size: file.size }); return out; }
  const walk = async (dir, pre, d) => {
    for await (const [name, h] of dir.entries()) {
      if (out.length >= limit) return;
      if (h.kind === "directory") { if (!SKIP_DIRS.has(name) && !name.startsWith(".") && d < depth) await walk(h, pre + name + "/", d + 1); else out.push({ path: pre + name + "/", size: -1 }); }
      else out.push({ path: pre + name, size: (await h.getFile()).size });
    }
  };
  await walk(await dirAt(root, false), root.length ? root.join("/") + "/" : "", 0);
  return out.sort((a, b) => a.path.localeCompare(b.path));
}
async function folderFile(path) {
  const parts = cleanPath(path), f = S.folder; if (!parts.length) throw new Error("Which file?");
  if (f.kind === "snapshot") { const file = f.files.get(parts.join("/")); if (!file) throw new Error("There is no file " + parts.join("/")); return file; }
  return (await (await dirAt(parts.slice(0, -1), false)).getFileHandle(parts[parts.length - 1])).getFile();
}
// agentOn: the model may run things on this computer (only from the computer running Potluck).
// A project chat is a coding agent, so it can always run commands (asking first unless the
// user chose to let it run freely).
const agentOn = () => (S.settings.agent !== "off" || !!(S.cur && S.cur.project)) && S.server && S.server.owner;
async function runOnComputer(kind, command, dir, timeout) {
  const r = await fetch("/potluck/run", { method: "POST", headers: { "Content-Type": "application/json", "X-Potluck": "1" }, body: JSON.stringify({ kind, command, dir: dir || "", timeout: +timeout || 0 }), signal: S.gen && S.gen.abort.signal });
  const j = await r.json().catch(() => ({}));
  if (!r.ok) throw new Error(j.error || "Could not run it.");
  const head = `Exit code ${j.exit}${j.timedOut ? " (stopped: it ran out of time)" : ""}, ${j.seconds.toFixed(1)} s, in ${j.dir}`;
  return head + "\n" + (j.output.trim() ? j.output : "(no output)");
}
const TOOLS = {
  async run_command(a) { return runOnComputer("shell", a.command, a.folder || (S.cur && S.cur.project ? S.cur.project.root : ""), a.timeout_seconds); },
  async run_applescript(a) { return runOnComputer("applescript", a.script, "", a.timeout_seconds); },
  async use_skill(a) { const r = await api("/potluck/skill?name=" + encodeURIComponent(a.name || "")); return r.text; },
  async list_files(a) {
    const list = await folderWalk(a.path || "");
    if (!list.length) return "(no files here)";
    return list.map(e => e.size < 0 ? e.path + "  (folder, not opened)" : `${e.path}  (${bytes(e.size)})`).join("\n") + (list.length >= 400 ? "\n(there are more; list a subfolder to see them)" : "");
  },
  async read_file(a) {
    const file = await folderFile(a.path);
    if (file.size > 2 << 20) return "That file is " + bytes(file.size) + ", too big to read whole. Use search_files to find the part you need.";
    const text = await file.text();
    if (/\u0000/.test(text.slice(0, 8000))) return "That is not a text file.";
    const lines = text.split("\n"), from = Math.max(1, parseInt(a.start_line, 10) || 1), max = Math.min(400, parseInt(a.max_lines, 10) || 200);
    const part = lines.slice(from - 1, from - 1 + max).join("\n").slice(0, 12000);
    const more = from - 1 + max < lines.length ? `\n(lines ${from} to ${from - 1 + max} of ${lines.length}; ask for start_line ${from + max} to read on)` : "";
    return part + more;
  },
  async search_files(a) {
    const q = String(a.query || "").toLowerCase(); if (!q) return "Give some text to search for.";
    const hits = []; let scanned = 0;
    for (const e of await folderWalk("", 600)) {
      if (e.size < 0 || e.size > 1 << 20 || hits.length >= 40) continue;
      let text; try { text = await (await folderFile(e.path)).text(); } catch { continue; }
      if (/\u0000/.test(text.slice(0, 2000))) continue; scanned++;
      text.split("\n").forEach((l, i) => { if (hits.length < 40 && l.toLowerCase().includes(q)) hits.push(`${e.path}:${i + 1}: ${l.trim().slice(0, 200)}`); });
    }
    return hits.length ? hits.join("\n") : `No match in ${scanned} text files.`;
  },
  async write_file(a) {
    if (S.folder.kind !== "handle") throw new Error("This folder was attached read-only.");
    const parts = cleanPath(a.path); if (!parts.length) throw new Error("Which file?");
    if ((await S.folder.handle.queryPermission({ mode: "readwrite" })) !== "granted" && (await S.folder.handle.requestPermission({ mode: "readwrite" })) !== "granted") throw new Error("The browser did not allow changing files in this folder.");
    const fh = await (await dirAt(parts.slice(0, -1), true)).getFileHandle(parts[parts.length - 1], { create: true });
    const w = await fh.createWritable(); await w.write(String(a.content ?? "")); await w.close();
    return `Wrote ${parts.join("/")} (${bytes(new Blob([String(a.content ?? "")]).size)}).`;
  },
};
function toolSpecs() {
  const fn = (name, description, properties, required = []) => ({ type: "function", function: { name, description, parameters: { type: "object", properties, required } } });
  const agent = [];
  if (agentOn()) {
    const mac = (S.server.os || "") === "darwin", win = (S.server.os || "") === "windows";
    agent.push(fn("run_command", `Run a command in ${win ? "PowerShell" : mac ? "the zsh shell" : "the shell"} on the user's computer and get back what it printed and its exit code. Use it to look around, run scripts and programs, install or build things, and open apps${mac ? " (open -a AppName, open URL)" : ""}. It cannot answer questions a command asks, so use options that do not ask.`,
      { command: { type: "string", description: "The command line" }, folder: { type: "string", description: "Folder to run it in. Leave out for the agent folder." }, timeout_seconds: { type: "integer", description: "Give up after this many seconds (default 120, at most 900)" } }, ["command"]));
    if (mac) agent.push(fn("run_applescript", "Run an AppleScript on the user's Mac to control apps: open documents, read or change what an app shows (Finder, Safari, Mail, Notes, Music, System Events for clicks and keystrokes). macOS asks the user once per app before allowing it.",
      { script: { type: "string", description: "The AppleScript source" }, timeout_seconds: { type: "integer" } }, ["script"]));
  }
  if (S.cur && S.cur.project && S.server && S.server.owner) {
    if (!agent.some(t => t.function.name === "run_command")) agent.unshift(fn("run_command", "Run a command on the user's computer, in the project folder unless you give another folder. Use it for builds, tests, git, package managers and anything else. It cannot answer questions a command asks.",
      { command: { type: "string" }, folder: { type: "string" }, timeout_seconds: { type: "integer", description: "Default 120, at most 900" } }, ["command"]));
    agent.push(
      fn("list_files", "List the project's files (dependency and build folders are left out).", { path: { type: "string", description: "Subfolder, relative to the project. Leave out for all." }, pattern: { type: "string", description: "Only files matching this, e.g. *.go or src/**/*.ts" } }),
      fn("read_file", "Read a file from the project, with line numbers.", { path: { type: "string" }, start_line: { type: "integer" }, max_lines: { type: "integer", description: "Default 2000" } }, ["path"]),
      fn("search_files", "Find lines in the project's files containing some text, or matching /a regular expression/.", { query: { type: "string" }, pattern: { type: "string", description: "Only files matching this, e.g. *.py" } }, ["query"]),
      fn("edit_file", "Replace exact text in a file. old must match the file exactly (copy it from read_file, without the line numbers) and be unique unless all is true.", { path: { type: "string" }, old: { type: "string" }, new: { type: "string" }, all: { type: "boolean" } }, ["path", "old", "new"]),
      fn("write_file", "Create a file in the project, or replace its whole content.", { path: { type: "string" }, content: { type: "string" } }, ["path", "content"]));
  }
  for (const t of (S.mcp && S.mcp.tools) || []) {
    let params = t.inputSchema; if (!params || params.type !== "object") params = { type: "object", properties: {} };
    agent.push({ type: "function", function: { name: mcpName(t), description: `[${t.server}] ${t.description || t.name}`.slice(0, 1024), parameters: params } });
  }
  if (S.ctx && (S.ctx.skills || []).length) agent.push(fn("use_skill", "Load one of the user's skills (listed in your instructions) and get its full instructions.", { name: { type: "string", description: "The skill's name" } }, ["name"]));
  if (S.cur && S.cur.project) return agent;
  if (!S.folder || S.folder.needsAccess || S.settings.perm === "none") return agent.length ? agent : null;
  const t = [...agent,
    fn("list_files", "List the files in the user's folder, or in a subfolder of it.", { path: { type: "string", description: "Subfolder, relative to the folder. Leave out for everything." } }),
    fn("read_file", "Read a text file from the user's folder.", { path: { type: "string", description: "File path relative to the folder" }, start_line: { type: "integer" }, max_lines: { type: "integer" } }, ["path"]),
    fn("search_files", "Find lines containing some text in the files of the user's folder.", { query: { type: "string" } }, ["query"]),
  ];
  if (S.folder.kind === "handle") t.push(fn("write_file", "Create a file, or replace its whole content, in the user's folder.", { path: { type: "string" }, content: { type: "string", description: "The complete new content of the file" } }, ["path", "content"]));
  return t;
}
// askPermission shows a card in the thread and waits for the user's answer.
function askPermission(call, kind) {
  return new Promise(resolve => {
    let a = {}; try { a = JSON.parse(call.args || "{}"); } catch { }
    const el = $(`.tool[data-call="${CSS.escape(call.id)}"]`); if (!el) return resolve("deny");
    el.classList.add("ask"); el.open = true;
    const where = esc(S.folder ? S.folder.name : S.cur.project ? S.cur.project.name : "your folder");
    const verb = call.name === "edit_file" ? "change " + esc(a.path || "a file") : "write " + esc(a.path || "a file");
    const what = kind === "write" ? `The model wants to ${verb} in ${where}.` : `The model wants to ${call.name === "search_files" ? "search" : call.name === "list_files" ? "list the files in" : "read " + esc(a.path || "a file") + " in"} ${where}.`;
    const shown = call.name === "edit_file" ? `Replace:\n${String(a.old || "").slice(0, 1500)}\n\nWith:\n${String(a.new || "").slice(0, 1500)}` : String(a.content || "");
    $(".out", el).innerHTML = what + (kind === "write" ? `\n\n${esc(shown.slice(0, 3000))}${shown.length > 3000 ? "\n…" : ""}` : "");
    const row = document.createElement("div"); row.className = "ask-row";
    row.innerHTML = `<button class="btn small" data-v="allow">Allow</button><button class="btn small ghost" data-v="always">Always allow ${kind === "write" ? "changes" : "reading"} in this chat</button><button class="btn small ghost" data-v="deny">Don't allow</button>`;
    el.appendChild(row); scrollDown(true);
    row.addEventListener("click", e => { const v = e.target.dataset.v; if (!v) return; row.remove(); el.classList.remove("ask"); resolve(v); });
    S.gen.cancelAsk = () => { row.remove(); resolve("deny"); };
  });
}
// askRun shows what the model wants to run and waits for the user's answer.
function askRun(call) {
  return new Promise(resolve => {
    let a = {}; try { a = JSON.parse(call.args || "{}"); } catch { }
    const el = $(`.tool[data-call="${CSS.escape(call.id)}"]`); if (!el) return resolve("deny");
    el.classList.add("ask"); el.open = true;
    const script = call.name === "run_applescript", text = String((script ? a.script : a.command) || "");
    const where = script ? "" : `<div class="small muted">In ${esc(a.folder || (S.server && S.server.agentDir) || "the agent folder")}${a.timeout_seconds ? `, for at most ${esc(a.timeout_seconds)} s` : ""}</div>`;
    $(".out", el).innerHTML = `<b>The model wants to ${script ? "run this AppleScript to control an app" : "run this command"} on this computer:</b><pre class="cmd">${esc(text)}</pre>${where}` +
      `<div class="small muted">It runs with your permissions: it can read, change and delete what you can. Check it before you say yes.</div>`;
    const row = document.createElement("div"); row.className = "ask-row";
    row.innerHTML = `<button class="btn small" data-v="allow">Run it</button><button class="btn small ghost" data-v="always">Run commands without asking in this chat</button><button class="btn small ghost" data-v="deny">Don't run</button>`;
    el.appendChild(row); scrollDown(true);
    row.addEventListener("click", e => { const v = e.target.dataset.v; if (!v) return; row.remove(); el.classList.remove("ask"); resolve(v); });
    S.gen.cancelAsk = () => { row.remove(); resolve("deny"); };
  });
}
const PTOOLS = { // project chats: the files live on this computer, reached through Potluck
  list_files: a => fsCall({ op: "list", root: S.cur.project.root, path: a.path || "", glob: a.pattern || "" }),
  read_file: a => fsCall({ op: "read", root: S.cur.project.root, path: a.path, start: +a.start_line || 0, max: +a.max_lines || 0 }),
  search_files: a => fsCall({ op: "search", root: S.cur.project.root, query: a.query, glob: a.pattern || "" }),
  edit_file: a => fsCall({ op: "edit", root: S.cur.project.root, path: a.path, old: a.old, new: a.new, all: !!a.all }),
  write_file: a => fsCall({ op: "write", root: S.cur.project.root, path: a.path, content: String(a.content ?? "") }),
};
async function runTool(call) {
  const c = S.cur;
  const mt = call.name.startsWith("mcp__") && ((S.mcp && S.mcp.tools) || []).find(t => mcpName(t) === call.name);
  if (call.name.startsWith("mcp__")) {
    if (!mt) return { state: "error", content: "That connector tool is not available now." };
    if (S.settings.agent !== "auto" && !c.grants["mcp:" + mt.server]) {
      const v = await askConnector(call, mt);
      if (v === "deny") return { state: "denied", content: "The user did not allow this." };
      if (v === "always") c.grants["mcp:" + mt.server] = true;
    }
    try {
      const r = await fetch("/potluck/mcp", { method: "POST", headers: { "Content-Type": "application/json", "X-Potluck": "1" }, signal: S.gen && S.gen.abort.signal,
        body: JSON.stringify({ op: "call", root: c.project ? c.project.root : "", server: mt.server, tool: mt.name, args: JSON.parse(call.args || "{}") }) });
      const j = await r.json().catch(() => ({})); if (!r.ok) throw new Error(j.error || "The connector did not answer.");
      return { state: "ok", content: j.text || "(no output)" };
    } catch (e) { return { state: "error", content: "Could not do that: " + (e.message || e) }; }
  }
  if (c.project && PTOOLS[call.name]) {
    const kind = call.name === "write_file" || call.name === "edit_file" ? "write" : "read", level = S.settings.perm;
    const needAsk = level === "ask" || level === "none" ? !c.grants[kind] : level === "read" ? kind === "write" && !c.grants.write : false;
    if (needAsk) {
      const v = await askPermission(call, kind);
      if (v === "deny") return { state: "denied", content: "The user did not allow this." };
      if (v === "always") c.grants[kind] = true;
    }
    try { let a = {}; try { a = JSON.parse(call.args || "{}"); } catch { throw new Error("The tool request was not valid."); } return { state: "ok", content: (await PTOOLS[call.name](a)).text }; }
    catch (e) { return { state: "error", content: "Could not do that: " + (e.message || e) }; }
  }
  if (call.name === "use_skill") { // reads only Potluck's own copy of the user's skills
    try { let a = {}; try { a = JSON.parse(call.args || "{}"); } catch { } return { state: "ok", content: await TOOLS.use_skill(a) }; }
    catch (e) { return { state: "error", content: "Could not load that skill: " + (e.message || e) }; }
  }
  if (call.name.startsWith("run_")) {
    if (!TOOLS[call.name] || !agentOn()) return { state: "denied", content: "Agent mode is off, so nothing can be run." };
    if (S.settings.agent !== "auto" && !c.grants.run) {
      const v = await askRun(call);
      if (v === "deny") return { state: "denied", content: "The user did not allow this command. Do not try it another way; ask what they would like instead." };
      if (v === "always") c.grants.run = true;
    }
    try { let a = {}; try { a = JSON.parse(call.args || "{}"); } catch { throw new Error("The tool request was not valid."); } return { state: "ok", content: await TOOLS[call.name](a) }; }
    catch (e) { return { state: "error", content: "Could not do that: " + (e.message || e) }; }
  }
  const kind = call.name === "write_file" ? "write" : "read", level = S.settings.perm;
  const needAsk = level === "ask" ? !c.grants[kind] : level === "read" ? kind === "write" && !c.grants.write : false;
  if (!TOOLS[call.name]) return { state: "error", content: "There is no tool called " + call.name + "." };
  if (needAsk) {
    const v = await askPermission(call, kind);
    if (v === "deny") return { state: "denied", content: "The user did not allow this." };
    if (v === "always") c.grants[kind] = true;
  }
  try { let a = {}; try { a = JSON.parse(call.args || "{}"); } catch { throw new Error("The tool request was not valid."); } return { state: "ok", content: await TOOLS[call.name](a) }; }
  catch (e) { return { state: "error", content: "Could not do that: " + (e.message || e) }; }
}

// ---------- talking to the model ----------
function systemPrompt() {
  const s = S.settings, p = ["You are a helpful assistant. You run privately on the user's own computers through an app called Potluck."];
  if (STYLES[s.style].text) p.push(STYLES[s.style].text);
  if (s.instructions.trim()) p.push("The user's standing instructions:\n" + s.instructions.trim());
  // Imported text gets about a quarter of the model's context (4 characters a token), so it
  // never crowds out the conversation on a small model.
  const budget = Math.max(2000, ctxSize()), cx = S.ctx || {}, cap = (t, n) => { n = Math.min(n, budget * 0.5); return t.length > n ? t.slice(0, n) + "\n(… shortened; the rest is in Potluck's continuity folder)" : t; };
  if ((cx.profile || "").trim()) p.push("What the user brought over from their earlier AI setup: who they are and how they like to work. Treat it as their standing preferences.\n\n" + cap(cx.profile.trim(), 16000));
  if ((cx.memory || "").trim()) p.push("Things you remember about the user:\n\n" + cap(cx.memory.trim(), 12000));
  if ((cx.skills || []).length) p.push("Skills the user has set up. When a request matches one, load it with use_skill and follow it:\n" + cx.skills.slice(0, budget >= 16000 ? 60 : 15).map(k => `- ${k.name}: ${k.description || ""}`).join("\n"));
  if (agentOn() && (cx.projects || []).length) p.push("The user's projects (folder paths on this computer), most recent first. When they name one, work in its folder and read its CLAUDE.md or AGENTS.md first if it has one:\n" + cx.projects.slice(0, budget >= 16000 ? 40 : 10).map(q => `- ${q.name}: ${q.path}${q.instructions ? " (instructions: " + q.instructions + ")" : ""}`).join("\n"));
  if (S.cur.summary) p.push("This conversation continues an earlier one. Summary of the earlier part:\n" + S.cur.summary);
  if (agentOn()) {
    const os = { darwin: "macOS", windows: "Windows", linux: "Linux" }[S.server.os] || S.server.os || "the computer";
    p.push(`Agent mode is on. You can act on the user's ${os} computer with run_command${S.server.os === "darwin" ? " and run_applescript" : ""}. Commands start in ${S.server.agentDir || "the agent folder"} unless you give a folder. ` +
      `Work in small steps: run something, read the result, then decide the next step. Prefer commands that only look before ones that change things. ` +
      (S.settings.agent === "auto"
        ? `The user has chosen to let you act without asking, so carry out what they ask, changes included. When you are done, say plainly what you did and what you found.`
        : `Before anything that deletes, overwrites, sends, buys or cannot be undone, stop and ask the user in your reply instead of running it. When you are done, say plainly what you did and what you found.`));
  }
  if (S.cur.project && S.proj) {
    const pj = S.proj;
    p.push(`You are working in the user's project "${pj.name}", the folder ${pj.root} on this computer${pj.git ? " (a git repository)" : ""}, ${pj.files || "some"} files. ` +
      `Use list_files, read_file, search_files, edit_file and write_file with paths relative to that folder, and run_command for builds, tests, git and anything else (it starts in the project folder). ` +
      `Work like a careful senior engineer: look before you change anything, read the files you edit, make focused edits with edit_file, then build or run the tests to check your work, and fix what fails. ` +
      `When you are done, say plainly what you changed and how you checked it.`);
    if ((pj.instructions || "").trim()) p.push(`The project's own instructions (${pj.instrFile}), which you follow:\n\n` + cap(pj.instructions.trim(), 24000));
    if ((pj.memory || "").trim()) p.push("What you remember about this project:\n\n" + cap(pj.memory.trim(), 16000));
    if (pj.error) p.push("Note: " + pj.error);
  }
  if (S.folder && toolSpecs() && toolSpecs().some(t => t.function.name === "list_files")) p.push(`The user has shared a folder named "${S.folder.name}". Use the tools to look at${S.folder.kind === "handle" ? " or change" : ""} files in it when that helps. Paths are relative to that folder. Do not guess what a file contains: read it.`);
  return p.join("\n\n");
}
// thinkingKwargs tells the model's chat template whether to think first. Qwen-style
// templates take enable_thinking; others (Inkling 2) take a reasoning effort, where "none"
// keeps the thinking as short as the model allows.
// A budget is the only reliable stop for models that think whatever they are told (Inkling 2):
// none when thinking is off, a few thousand tokens when on, then the engine asks for the answer.
function thinkBudget(on = !!S.settings.thinking) {
  return on ? { reasoning_budget_tokens: 4096, reasoning_budget_message: "Time to stop thinking and give the answer." } : { reasoning_budget_tokens: 1 }; // not 0: some model files loop on their markers with 0
}
function thinkingKwargs(on = !!S.settings.thinking) {
  return { enable_thinking: on, reasoning_effort: on ? "high" : "none" };
}
function apiMessages() {
  const out = [{ role: "system", content: systemPrompt() }];
  for (const m of S.cur.messages) {
    if (m.role === "user") {
      const files = (m.files || []).filter(f => !f.skipped && !f.image).map(f => `<file name="${f.name}">\n${f.text}\n</file>`).join("\n\n");
      const text = files ? files + "\n\n" + (m.content || "See the attached file.") : (m.content || "See the picture.");
      const pics = (m.files || []).filter(f => !f.skipped && f.image);
      out.push({ role: "user", content: pics.length ? [{ type: "text", text }, ...pics.map(f => ({ type: "image_url", image_url: { url: f.image } }))] : text });
    } else if (m.role === "assistant") {
      const calls = m.calls && m.calls.length ? m.calls : null;
      if (!m.content && !calls) continue;
      const a = { role: "assistant", content: m.content || "" };
      if (calls) a.tool_calls = calls.map(c => ({ id: c.id, type: "function", function: { name: c.name, arguments: c.args || "{}" } }));
      out.push(a);
    } else if (m.role === "tool") out.push({ role: "tool", tool_call_id: m.callId, content: m.content });
  }
  return out;
}

// The direct route, for Qwen-style models with thinking off.
//
// These models begin every answer with an empty thinking block, but their chat template
// leaves that block out when the same answer is shown back as history. The engine then sees
// a different conversation each turn and reads all of it again (measured: every token, every
// turn). Here the engine writes the conversation out, the block is put back where it was,
// and the text is sent as is. Measured: a follow-up then reads only the new message.
const THINK_EMPTY = "<think>\n\n</think>\n\n", ASSISTANT_OPEN = "<|im_start|>assistant\n";
function directRoute(withTools) {
  const t = (S.props && S.props.chat_template) || "";
  if (S.settings.thinking || !t.includes("enable_thinking is false") || !t.includes("'<think>\\n\\n</think>\\n\\n'") || !t.includes("<|im_start|>")) return false;
  return !withTools || (t.includes("<function=") && t.includes("<parameter="));
}
function parseToolCalls(raw) {
  const calls = [], re = /<tool_call>\s*<function=([^>\n]+)>([\s\S]*?)<\/function>\s*<\/tool_call>/g;
  let m;
  while ((m = re.exec(raw))) {
    const args = {}, pr = /<parameter=([^>\n]+)>\n?([\s\S]*?)\n?<\/parameter>/g; let q;
    while ((q = pr.exec(m[2]))) args[q[1].trim()] = q[2];
    calls.push({ id: uid(), name: m[1].trim(), args: JSON.stringify(args) });
  }
  return calls;
}
async function streamDirect(msg, signal) {
  const s = S.settings, tools = toolSpecs(), body = { messages: apiMessages(), chat_template_kwargs: thinkingKwargs(false), ...thinkBudget(false) };
  if (tools) body.tools = tools;
  const fail = async r => { let t = await r.text(); try { const j = JSON.parse(t); t = (j.error && (j.error.message || j.error)) || t; } catch { } return String(t); };
  const tr = await fetch("/apply-template", { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify(body), signal });
  if (!tr.ok) throw new Error((await fail(tr)).slice(0, 400));
  const prompt = (await tr.json()).prompt.split(ASSISTANT_OPEN).map((part, i) => i && !part.startsWith("<think>") ? THINK_EMPTY + part : part).join(ASSISTANT_OPEN);
  const req = { prompt, stream: true, cache_prompt: true };
  if (s.temperature !== "" && !isNaN(+s.temperature)) req.temperature = +s.temperature;
  if (s.maxTokens !== "" && +s.maxTokens > 0) req.n_predict = +s.maxTokens;
  const r = await fetch("/completion", { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify(req), signal });
  if (!r.ok) {
    const t = await fail(r);
    if (/context|exceed|too (long|large)/i.test(t)) throw new Error("This conversation no longer fits in the model's memory. Use /compact to continue in a fresh chat with a summary, or start a new chat.");
    throw new Error(t.slice(0, 400) || "The model did not answer.");
  }
  const reader = r.body.getReader(), dec = new TextDecoder(), OPEN = "<tool_call>"; let buf = "", raw = "";
  const show = () => {
    const at = raw.indexOf(OPEN); let vis = at >= 0 ? raw.slice(0, at) : raw;
    if (at < 0) for (let k = Math.min(OPEN.length - 1, raw.length); k > 0; k--) if (raw.endsWith(OPEN.slice(0, k))) { vis = raw.slice(0, -k); break; }
    if (at >= 0 && S.gen) S.gen.note = "Getting ready to use your folder…";
    msg.content = at >= 0 ? vis.trimEnd() : vis;
  };
  for (; ;) {
    const { value, done } = await reader.read(); if (done) break;
    buf += dec.decode(value, { stream: true });
    let nl;
    while ((nl = buf.indexOf("\n")) >= 0) {
      const line = buf.slice(0, nl).trim(); buf = buf.slice(nl + 1);
      if (!line.startsWith("data:")) continue;
      let j; try { j = JSON.parse(line.slice(5)); } catch { continue; }
      if (j.error) throw new Error(j.error.message || "The model stopped with an error.");
      if (j.content) { raw += j.content; show(); }
      if (j.stop) {
        const t = j.timings || {};
        msg.stats = { in: j.tokens_evaluated || (t.prompt_n || 0) + (t.cache_n || 0), out: j.tokens_predicted || t.predicted_n || 0, tps: t.predicted_per_second || 0, ptps: t.prompt_per_second || 0, ms: (t.prompt_ms || 0) + (t.predicted_ms || 0), fresh: t.prompt_n || 0 };
      }
      drawLive();
    }
  }
  msg.calls = tools ? parseToolCalls(raw) : [];
  show();
  return msg.calls.length ? "tool_calls" : "stop";
}
async function streamOnce(msg, signal) {
  const s = S.settings, tools = toolSpecs();
  const body = { messages: apiMessages(), stream: true, stream_options: { include_usage: true }, cache_prompt: true, chat_template_kwargs: thinkingKwargs(), ...thinkBudget(), ...extModel() };
  if (tools) body.tools = tools;
  if (s.temperature !== "" && !isNaN(+s.temperature)) body.temperature = +s.temperature;
  if (s.maxTokens !== "" && +s.maxTokens > 0) body.max_tokens = +s.maxTokens;
  const r = await fetch("/v1/chat/completions", { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify(body), signal });
  if (!r.ok) {
    let t = await r.text(); try { const j = JSON.parse(t); t = (j.error && (j.error.message || j.error)) || t; } catch { }
    if (/context|exceed|too (long|large)/i.test(t)) throw new Error("This conversation no longer fits in the model's memory. Use /compact to continue in a fresh chat with a summary, or start a new chat.");
    throw new Error(String(t).slice(0, 400) || "The model did not answer.");
  }
  const reader = r.body.getReader(), dec = new TextDecoder(); let buf = "", finish = "";
  const calls = {};
  for (; ;) {
    const { value, done } = await reader.read(); if (done) break;
    buf += dec.decode(value, { stream: true });
    let nl;
    while ((nl = buf.indexOf("\n")) >= 0) {
      const line = buf.slice(0, nl).trim(); buf = buf.slice(nl + 1);
      if (!line.startsWith("data:")) continue;
      const data = line.slice(5).trim(); if (data === "[DONE]") continue;
      let j; try { j = JSON.parse(data); } catch { continue; }
      if (j.error) throw new Error(j.error.message || "The model stopped with an error.");
      for (const ch of j.choices || []) {
        const d = ch.delta || {};
        if (d.reasoning_content) msg.reasoning = (msg.reasoning || "") + d.reasoning_content;
        if (d.content) msg.content = (msg.content || "") + d.content;
        for (const tc of d.tool_calls || []) {
          const e = calls[tc.index || 0] = calls[tc.index || 0] || { id: "", name: "", args: "" };
          if (tc.id) e.id = tc.id; const f = tc.function || {}; if (f.name) e.name += f.name; if (f.arguments) e.args += f.arguments;
        }
        if (ch.finish_reason) finish = ch.finish_reason;
      }
      if (j.usage || j.timings) {
        const u = j.usage || {}, t = j.timings || {};
        msg.stats = { in: u.prompt_tokens || t.prompt_n || 0, out: u.completion_tokens || t.predicted_n || 0, tps: t.predicted_per_second || 0, ptps: t.prompt_per_second || 0, ms: (t.prompt_ms || 0) + (t.predicted_ms || 0), fresh: t.prompt_n || 0 };
      }
      drawLive();
    }
  }
  msg.calls = Object.values(calls).filter(c => c.name).map(c => ({ ...c, id: c.id || uid() }));
  return finish;
}
function addUsage(st) {
  if (!st) return;
  const c = S.cur, d = today();
  c.used = st.in + st.out;
  for (const t of [c.totals, S.usage.total, S.usage.days[d] = S.usage.days[d] || { in: 0, out: 0, ms: 0, replies: 0 }]) { t.in += st.fresh || 0; t.out += st.out; t.ms += st.ms; t.replies++; }
  Store.set("usage", S.usage);
}
async function generate() {
  const c = S.cur;
  if (!running()) { toast("No model is ready yet. Your message is kept; press Send again once one is running."); return; }
  if (S.folder && S.folder.needsAccess && !(await reconnectFolder())) return toast("Click the folder chip to reconnect your folder first.");
  const abort = new AbortController();
  S.gen = { abort, chatId: c.id, msg: null, note: "" };
  drawTools();
  try {
    const rounds = S.cur.project ? 200 : agentOn() ? 30 : 12; // a coding task takes many steps
    for (let round = 0; round < rounds; round++) {
      const msg = { id: uid(), role: "assistant", content: "", reasoning: "", t: Date.now() };
      c.messages.push(msg); S.gen.msg = msg; S.gen.note = round ? "Reading what it found…" : (c.messages.length > 2 || !toolSpecs() ? "Reading your message…" : "Reading your message and how to use your folder…");
      drawThread(); scrollDown(true);
      let finish;
      try { finish = await (directRoute(!!toolSpecs()) ? streamDirect : streamOnce)(msg, abort.signal); }
      catch (e) {
        if (abort.signal.aborted) { msg.stats = { ...(msg.stats || {}), stopped: true }; if (!msg.content && !msg.reasoning) c.messages.pop(); break; }
        msg.error = e.message || String(e); break;
      }
      addUsage(msg.stats);
      if (!msg.calls.length || finish !== "tool_calls") break;
      S.gen.msg = null; drawThread(); scrollDown(true);
      for (const call of msg.calls) {
        const res = abort.signal.aborted ? { state: "denied", content: "Stopped by the user." } : await runTool(call);
        c.messages.push({ id: uid(), role: "tool", callId: call.id, name: call.name, content: String(res.content).slice(0, 14000), state: res.state });
        drawThread(); scrollDown(true);
      }
      if (abort.signal.aborted) break;
      if (round === rounds - 1) c.messages.push({ id: uid(), role: "assistant", content: "", error: "Stopped after many steps in a row. Say \"continue\" if it should carry on." });
    }
  } finally {
    const last = c.messages[c.messages.length - 1];
    S.gen = null;
    await saveChat(c);
    if (S.cur.id === c.id) { drawThread(); drawTools(); scrollDown(); }
    if (S.settings.speak && last && last.role === "assistant" && last.content) speakText(last.content);
  }
}
function stop() { if (!S.gen) return; S.gen.cancelAsk && S.gen.cancelAsk(); S.gen.abort.abort(); }
async function send(text) {
  if (S.gen || S.compacting) return;
  const t = (text ?? $("#input").value).trim(), files = S.pending;
  if (!t && !files.some(f => !f.skipped)) return;
  if (t.startsWith("/") && !files.length && runSlash(t)) { $("#input").value = ""; autosize(); hideSlash(); drawTools(); return; }
  stopDictation();
  if (isTask()) { S.pending = []; drawChips(); return taskSend(t); }
  const c = S.cur;
  c.messages.push({ id: uid(), role: "user", content: t, files: files.length ? files : undefined, t: Date.now() });
  if (!c.title) c.title = titleFrom(t || files[0].name);
  S.pending = []; $("#input").value = ""; autosize(); hideSlash();
  drawHeader(); drawChips(); drawThread(); scrollDown(true);
  await saveChat(c);
  if (conns().some(k => connOn(k.name))) await loadMCP();
  await generate();
}
// Versions. Trying again, or editing a message, starts a new version of that turn; the
// earlier ones are kept on the user message and can be brought back with ‹ ›. Only the
// version on screen is sent to the model.
function branchFrom(i, content) {
  const c = S.cur, m = c.messages[i];
  if (!m.branches) { m.branches = [{ content: m.content, files: m.files }]; m.bi = 0; }
  m.branches[m.bi].tail = c.messages.slice(i + 1);
  m.branches.push({ content, files: m.files });
  m.bi = m.branches.length - 1;
  m.content = content;
  c.messages.length = i + 1;
}
async function switchBranch(i, step) {
  const c = S.cur, m = c.messages[i];
  if (S.gen || !m || !m.branches) return;
  const to = m.bi + step; if (to < 0 || to >= m.branches.length) return;
  m.branches[m.bi].tail = c.messages.slice(i + 1);
  m.bi = to;
  const b = m.branches[to];
  m.content = b.content; m.files = b.files;
  c.messages = c.messages.slice(0, i + 1).concat(b.tail || []);
  b.tail = null;
  await saveChat(c); drawThread(); drawTools();
}
async function retry() {
  if (S.gen) return; const c = S.cur, i = c.messages.map(m => m.role).lastIndexOf("user"); if (i < 0) return;
  branchFrom(i, c.messages[i].content); drawThread(); await generate();
}
function editMessage(i) {
  if (S.gen) return; const el = $(`.msg[data-i="${i}"]`), m = S.cur.messages[i]; if (!el || !m) return;
  el.innerHTML = `<div class="edit-box"><textarea>${esc(m.content)}</textarea><div class="row"><button class="btn small ghost" data-a="edit-cancel">Cancel</button><button class="btn small" data-a="edit-save" data-i="${i}">Send again</button></div></div>`;
  const ta = $("textarea", el); ta.focus(); ta.setSelectionRange(ta.value.length, ta.value.length);
}
async function compact() {
  const c = S.cur; if (S.gen || !c.messages.length) return toast("There is nothing to summarize yet.");
  if (!running()) return toast("No model is running.");
  S.compacting = true; drawStatus();
  const body = { messages: [...apiMessages(), { role: "user", content: "Summarize our conversation so far for your own later use. Keep every fact, decision, name, number and open question that matters, in compact notes. Do not add anything new." }], stream: false, chat_template_kwargs: thinkingKwargs(false), ...thinkBudget(false), ...extModel() };
  try {
    const r = await fetch("/v1/chat/completions", { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify(body) });
    const j = await r.json(); if (!r.ok) throw new Error((j.error && j.error.message) || "The model did not answer.");
    const n = newChat(); n.summary = j.choices[0].message.content; n.title = (c.title || "Chat") + " (continued)"; n.titled = true;
    S.cur = n; S.folder = null; S.pending = []; await saveChat(n); drawAll(); toast("Continuing in a fresh chat with the summary.");
  } catch (e) { toast("Could not summarize: " + e.message); }
  S.compacting = false; drawStatus();
}

// ---------- voice ----------
const SR = window.SpeechRecognition || window.webkitSpeechRecognition;
async function toggleDictation() {
  if (S.rec) return stopDictation();
  if (!SR) return toast("This browser cannot take dictation. Chrome, Edge and Safari can.");
  if (!window.isSecureContext) return toast("Dictation only works on the computer running Potluck, or over a secure address.");
  const rec = new SR(), input = $("#input"), base = input.value ? input.value.replace(/\s*$/, " ") : "";
  rec.continuous = true; rec.interimResults = true; rec.lang = navigator.language || "en-US";
  S.recLocal = false;
  try { if (SR.available && "processLocally" in rec && (await SR.available({ langs: [rec.lang], processLocally: true })) === "available") { rec.processLocally = true; S.recLocal = true; } } catch { }
  let finalText = "", quiet;
  rec.onresult = e => {
    let interim = ""; finalText = "";
    for (const r of e.results) (r.isFinal ? (finalText += r[0].transcript) : (interim += r[0].transcript));
    const spoken = (finalText + interim).replace(/^\s+/, "");
    const cmd = /\b(send (the )?message|send it)[.!]?\s*$/i.exec(finalText);
    if (cmd && !interim) { input.value = base + finalText.slice(0, cmd.index).trim(); stopDictation(); autosize(); send(); return; }
    input.value = base + spoken; autosize(); drawTools();
    clearTimeout(quiet);
    if (S.settings.autoSend && finalText.trim() && !interim) quiet = setTimeout(() => { if (S.rec === rec) { stopDictation(); send(); } }, 1800);
  };
  rec.onerror = e => { if (e.error === "not-allowed" || e.error === "service-not-allowed") toast("The browser did not allow the microphone."); else if (e.error !== "no-speech" && e.error !== "aborted") toast("Dictation stopped: " + e.error); };
  rec.onend = () => { if (S.rec === rec) { S.rec = null; drawTools(); drawFine(); } };
  try { rec.start(); S.rec = rec; drawTools(); drawFine(); } catch { toast("Could not start dictation."); }
}
function stopDictation() { if (S.rec) { const r = S.rec; S.rec = null; try { r.stop(); } catch { } drawTools(); drawFine(); } }
function speakText(md) {
  if (!window.speechSynthesis) return toast("This browser cannot read aloud.");
  speechSynthesis.cancel();
  const text = md.replace(/```[\s\S]*?```/g, " (code) ").replace(/[*_#`>|]/g, "").replace(/\[([^\]]+)\]\([^)]+\)/g, "$1");
  speechSynthesis.speak(new SpeechSynthesisUtterance(text));
}

// ---------- menus ----------
let popFor = null;
function hidePop() { $("#pop").hidden = true; popFor = null; }
function showPop(anchor, html, onPick) {
  const p = $("#pop");
  if (popFor === anchor) return hidePop();
  p.innerHTML = html; p.hidden = false; popFor = anchor;
  const r = anchor.getBoundingClientRect(), w = p.offsetWidth, h = p.offsetHeight;
  p.style.left = Math.max(8, Math.min(window.innerWidth - w - 8, r.left)) + "px";
  p.style.top = (r.top - h - 8 > 8 ? r.top - h - 8 : Math.min(window.innerHeight - h - 8, r.bottom + 8)) + "px";
  p.onclick = e => { const b = e.target.closest("[data-v]"); if (!b || b.disabled) return; hidePop(); onPick(b.dataset.v, b); };
}
const mi = (v, label, opt = {}) => `<button class="mi ${opt.on ? "on" : ""} ${opt.danger ? "danger" : ""}" data-v="${esc(v)}" ${opt.disabled ? "disabled" : ""}>${opt.check !== undefined ? `<span class="check">${icon("check")}</span>` : opt.icon ? ic(opt.icon) : ""}<span>${label}${opt.d ? `<span class="d">${opt.d}</span>` : ""}</span>${opt.k ? `<span class="k">${opt.k}</span>` : ""}</button>`;

function attachMenu(a) {
  showPop(a, mi("files", "Attach files", { icon: "file", d: "Their text is included with your message" }) +
    mi("folder", S.folder ? "Change the working folder" : "Attach a folder", { icon: "folder", d: window.showDirectoryPicker ? "The model can look through it, and change it if you allow" : "The model can look through a read-only copy" }) +
    (S.folder ? mi("detach", "Remove the folder", { icon: "x" }) : ""),
    v => v === "files" ? $("#file-input").click() : v === "folder" ? pickFolder() : setFolder(null));
}
function permMenu(a) {
  showPop(a, `<h5>What the model may do with ${esc(S.folder ? S.folder.name : "your folder")}</h5>` + Object.entries(PERMS).map(([k, p]) => mi(k, p.name, { on: S.settings.perm === k, check: 1, d: p.d, disabled: k === "edit" && S.folder && S.folder.kind !== "handle" })).join("") +
    (S.folder && S.folder.kind !== "handle" ? `<div class="note">This folder is a read-only copy, so the model cannot change it.</div>` : ""),
    async v => {
      S.settings.perm = v; saveSettings();
      if (v === "edit" && S.folder && S.folder.kind === "handle") { try { await S.folder.handle.requestPermission({ mode: "readwrite" }); } catch { } }
      drawTools(); drawFine(); toast(PERMS[v].name + ": " + PERMS[v].d);
    });
}
function agentMenu(a) {
  showPop(a, `<h5>Agent mode: may the model run things on this computer?</h5>` + Object.entries(AGENT).map(([k, p]) => mi(k, p.name, { on: S.settings.agent === k, check: 1, d: p.d })).join("") +
    (isTask() ? `<hr>` + mi("all", S.cur.approveAll ? "Stop approving everything" : "Approve everything in this task…", { icon: "shield", d: "Nothing asks: commands, file changes, connectors, apps. Experimental; for tasks you leave running unattended.", danger: !S.cur.approveAll }) : "") +
    `<div class="note">Commands start in ${esc((S.server && S.server.agentDir) || "your home folder")}. Change that in Potluck's control panel, under Models, "On this computer". Only works on the computer running Potluck.</div>`,
    v => {
      if (v === "all") return S.cur.approveAll ? (S.cur.approveAll = false, saveChat(S.cur), taskPolicySync(), drawTools(), toast("Approve everything is off.")) : approveAllSheet();
      S.settings.agent = v; saveSettings(); taskPolicySync(); drawTools(); toast(AGENT[v].name + ": " + AGENT[v].d);
    });
}
async function modelMenu(a) {
  try { S.models = await api("/potluck/models"); } catch (e) { S.models = { models: [], canSwitch: false, error: e.message }; }
  const m = S.models, cur = S.server && S.server.model && S.server.model.file;
  const ext = S.settings.extModel, idx = x => m.models.indexOf(x);
  const local = m.models.filter(x => !(x.node || "").startsWith("ext:")), others = m.models.filter(x => (x.node || "").startsWith("ext:"));
  let h = `<h5>Models on your computers</h5>` + (local.length ? local.map(x => mi("m" + idx(x), esc(x.title || x.file), { on: x.running && !ext, check: 1, d: `${gb(x.size)} · on ${esc(x.nodeName)}${x.running ? " · running now" : ""}`, disabled: !m.canSwitch && !x.running })).join("") : `<div class="note">No models are downloaded yet.</div>`);
  if (others.length) h += `<h5>Your other AI setups</h5>` + others.map(x => mi("m" + idx(x), esc(x.title), { on: ext === x.file, check: 1, d: "from " + esc(x.nodeName) })).join("");
  h += `<hr>` + (running() ? mi("ctx", "Conversation memory", { icon: "chip", d: `${num(ctxSize())} tokens. Change it by reloading the model.`, disabled: !m.canSwitch }) : "");
  h += m.canSwitch ? mi("panel", "Find and download more models", { icon: "plus", d: "Opens the control panel" }) : `<div class="note">Only the computer running Potluck can switch models.</div>`;
  showPop(a, h, v => {
    if (v === "panel") return window.open(S.server.panel + "/#models", "_blank", "noopener");
    if (v === "ctx") return switchSheet(m.models.find(x => x.running) || { file: cur, title: modelTitle(), running: true });
    const x = m.models[+v.slice(1)]; if (!x) return;
    if ((x.node || "").startsWith("ext:")) { S.settings.extModel = x.file; S.settings.extTitle = x.nodeName + ": " + x.title; saveSettings(); drawStatus && drawStatus(); return toast("Chatting with " + x.title + " from " + x.nodeName + "."); }
    if (S.settings.extModel) { S.settings.extModel = ""; S.settings.extTitle = ""; saveSettings(); drawStatus && drawStatus(); if (x.running) return toast("Back to " + (x.title || x.file) + "."); }
    if (!x.running) switchSheet(x);
  });
}
function switchSheet(x) {
  const cur = ctxSize();
  sheet(`<h3>${x.running ? "Reload" : "Switch to"} ${esc(x.title || x.file)}</h3>
    <p class="muted">${x.running ? "Reloading takes a minute or two." : "The model that is running now is stopped first. Loading takes a few minutes; you can watch the progress above the chat."} Chats are kept.</p>
    <label class="f" for="sw-ctx">Conversation memory</label>
    <select id="sw-ctx">${[4096, 8192, 16384, 32768].map(n => `<option value="${n}" ${n === cur ? "selected" : ""}>${num(n)} tokens${n === 4096 ? " (normal)" : n === 8192 ? " (long)" : n === 16384 ? " (very long)" : " (longest, needs more memory)"}</option>`).join("")}</select>
    <p class="muted small">A longer memory fits bigger files and longer chats, and uses more of each computer's memory.</p>
    <div class="foot"><button class="btn ghost" data-a="close">Cancel</button><button class="btn" data-a="do-switch">${x.running ? "Reload" : "Switch"}</button></div>`,
    { "do-switch": async () => { const ctx = +$("#sw-ctx").value; closeSheet(); try { await api("/potluck/switch", { file: x.file, node: x.node || "", ctx }); toast("Loading " + (x.title || x.file) + "…"); poll(); } catch (e) { toast("Could not switch: " + e.message); } } });
}
function usageSheet() {
  const c = S.cur, s = S.server || {}, ctx = ctxSize(), frac = Math.min(1, c.used / ctx), d = S.usage.days[today()] || { in: 0, out: 0, ms: 0, replies: 0 }, t = S.usage.total;
  const speed = x => x.ms > 0 && x.out ? (x.out / (x.ms / 1000)).toFixed(1) + " tokens/s overall" : "";
  const nodes = (s.nodes || []).filter(n => n.role).map(n => `<span>${esc(n.name)} ${n.role === "head" ? "(runs the model)" : ""}</span><span>${n.held ? gb(n.held) + " of the model" : ""}${n.linkMs ? " · " + Math.round(n.linkMs) + " ms away" : ""}</span>`).join("");
  sheet(`<h3>Usage</h3><p class="muted">Everything runs on your own computers. There are no limits and nothing is billed; these numbers are for your information.</p>
    <h4>This chat's memory</h4><div class="meter"><i style="width:${(frac * 100).toFixed(1)}%"></i></div>
    <div class="kv"><span>Used</span><span>${num(c.used)} of ${num(ctx)} tokens (${Math.round(frac * 100)}%)</span></div>
    ${frac > 0.75 ? `<p class="small">Getting full. <button class="link" data-a="cmd" data-c="compact">Continue in a fresh chat with a summary</button></p>` : ""}
    <h4>This chat</h4><div class="kv"><span>Replies</span><span>${num(c.totals.replies)}</span><span>Tokens read</span><span>${num(c.totals.in)}</span><span>Tokens written</span><span>${num(c.totals.out)}</span><span>Time working</span><span>${dur(c.totals.ms)} ${speed(c.totals) ? "· " + speed(c.totals) : ""}</span></div>
    <h4>Today</h4><div class="kv"><span>Replies</span><span>${num(d.replies)}</span><span>Tokens read / written</span><span>${num(d.in)} / ${num(d.out)}</span><span>Time working</span><span>${dur(d.ms)}</span></div>
    <h4>All time, in this browser</h4><div class="kv"><span>Replies</span><span>${num(t.replies)}</span><span>Tokens read / written</span><span>${num(t.in)} / ${num(t.out)}</span><span>Time working</span><span>${dur(t.ms)}</span></div>
    ${nodes ? `<h4>Computers working right now</h4><div class="kv">${nodes}</div>` : ""}
    <div class="foot"><button class="btn" data-a="close">Done</button></div>`);
}
function chatMenu(a, id) {
  const cur = !id || id === S.cur.id, meta = id ? S.chats.find(c => c.id === id) : S.cur;
  showPop(a, mi("rename", "Rename", { icon: "edit" }) + mi("star", meta && meta.starred ? "Remove star" : "Star", { icon: "star" }) + mi("export", "Export as Markdown", { icon: "file" }) +
    (cur ? mi("compact", "Summarize and continue in a new chat", { icon: "retry" }) : "") + `<hr>` + mi("delete", "Delete", { icon: "trash", danger: 1 }),
    async v => {
      const c = cur ? S.cur : await Store.getChat(id); if (!c) return;
      if (v === "rename") { if (!cur) await openChat(id); startRename(); }
      else if (v === "star") { c.starred = !c.starred; await Store.putChat(c); const m = S.chats.find(x => x.id === c.id); if (m) m.starred = c.starred; drawHeader(); drawList(); }
      else if (v === "export") exportChat(c);
      else if (v === "compact") compact();
      else if (v === "delete") sheet(`<h3>Delete this chat?</h3><p>"${esc(c.title || "New chat")}" will be removed from this browser. This cannot be undone.</p><div class="foot"><button class="btn ghost" data-a="close">Cancel</button><button class="btn danger" data-a="yes">Delete</button></div>`, { yes: () => { closeSheet(); deleteChat(c.id); } });
    });
}
function startRename() {
  const h = $("#chat-title"); h.contentEditable = "true"; h.focus(); document.getSelection().selectAllChildren(h);
  const done = async save => { h.contentEditable = "false"; h.onblur = h.onkeydown = null; if (save && h.textContent.trim()) { S.cur.title = h.textContent.trim().slice(0, 120); S.cur.titled = true; await saveChat(); } drawHeader(); };
  h.onblur = () => done(true); h.onkeydown = e => { if (e.key === "Enter") { e.preventDefault(); h.blur(); } if (e.key === "Escape") { e.preventDefault(); done(false); } };
}
function exportChat(c) {
  let md = `# ${c.title || "Chat"}\n\n`;
  for (const m of c.messages) {
    if (m.role === "user") md += `## You\n\n${(m.files || []).map(f => `*Attached: ${f.name}*\n\n`).join("")}${m.content}\n\n`;
    else if (m.role === "assistant" && m.content) md += `## Potluck\n\n${m.content}\n\n`;
    else if (m.role === "tool") md += `*${m.name}:* ${String(m.content).split("\n")[0].slice(0, 120)}\n\n`;
  }
  download((c.title || "chat").replace(/[^\w -]+/g, "").trim().slice(0, 60) + ".md", md, "text/markdown");
}

// ---------- sheets ----------
function sheet(html, handlers = {}) {
  hidePop(); const m = $("#modal"); $(".sheet", m).innerHTML = html; m.hidden = false; m.handlers = handlers;
  const f = $("input,textarea,select,button.btn:not(.ghost)", m); f && f.focus();
}
function closeSheet() { $("#modal").hidden = true; $("#input").focus(); }
function settingsSheet() {
  const s = S.settings;
  sheet(`<h3>Settings</h3>
    <label class="f" for="st-ins">Instructions the model should always follow</label>
    <textarea id="st-ins" placeholder="For example: I am a carpenter in Phoenix. Use US units. Be direct.">${esc(s.instructions)}</textarea>
    <label class="f" for="st-style">Writing style</label>
    <select id="st-style">${Object.entries(STYLES).map(([k, v]) => `<option value="${k}" ${s.style === k ? "selected" : ""}>${v.name}</option>`).join("")}</select>
    <label class="f" for="st-theme">Appearance</label>
    <select id="st-theme"><option value="auto" ${s.theme === "auto" ? "selected" : ""}>Match this computer</option><option value="light" ${s.theme === "light" ? "selected" : ""}>Light</option><option value="dark" ${s.theme === "dark" ? "selected" : ""}>Dark</option></select>
    <h4>Typing and voice</h4>
    <label class="opt"><input type="checkbox" id="st-enter" ${s.enterSends ? "checked" : ""}><span>Enter sends the message<small>Off: Enter starts a new line and ${MOD}Enter sends.</small></span></label>
    <label class="opt"><input type="checkbox" id="st-auto" ${s.autoSend ? "checked" : ""}><span>Send when I stop speaking<small>While dictating. You can also end by saying "send message".</small></span></label>
    <label class="opt"><input type="checkbox" id="st-speak" ${s.speak ? "checked" : ""}><span>Read replies aloud</span></label>
    <h4>For the curious</h4>
    <label class="f" for="st-temp">Creativity (temperature)</label><input type="text" id="st-temp" inputmode="decimal" placeholder="Model's own default" value="${esc(s.temperature)}">
    <label class="f" for="st-max">Longest reply, in tokens</label><input type="text" id="st-max" inputmode="numeric" placeholder="No limit" value="${esc(s.maxTokens)}">
    <h4>Your chats</h4>
    <p class="muted small">Chats, settings and usage are kept in this browser on this computer, not on the Potluck computers.</p>
    <div class="row"><button class="btn small ghost" data-a="export-all">Export all chats</button><button class="btn small ghost" data-a="import-all">Import</button><button class="btn small ghost" data-a="wipe">Delete all chats</button></div>
    <div class="foot"><button class="btn ghost" data-a="close">Cancel</button><button class="btn" data-a="save">Save</button></div>`,
    {
      save: () => {
        Object.assign(s, { instructions: $("#st-ins").value, style: $("#st-style").value, theme: $("#st-theme").value, enterSends: $("#st-enter").checked, autoSend: $("#st-auto").checked, speak: $("#st-speak").checked, temperature: $("#st-temp").value.trim(), maxTokens: $("#st-max").value.trim() });
        saveSettings(); applyTheme(); closeSheet(); toast("Saved");
      },
      "export-all": async () => download("potluck-chats.json", JSON.stringify({ potluckChats: 1, chats: await Store.allChats() }), "application/json"),
      "import-all": () => { const i = document.createElement("input"); i.type = "file"; i.accept = ".json,application/json"; i.onchange = async () => { try { const j = JSON.parse(await i.files[0].text()); if (!j.potluckChats) throw 0; for (const c of j.chats) await Store.putChat(c); await loadChats(); closeSheet(); toast(j.chats.length + " chats imported."); } catch { toast("That is not a Potluck chat export."); } }; i.click(); },
      wipe: () => sheet(`<h3>Delete all chats?</h3><p>Every chat in this browser will be removed. This cannot be undone.</p><div class="foot"><button class="btn ghost" data-a="close">Cancel</button><button class="btn danger" data-a="yes">Delete all</button></div>`, { yes: async () => { await Store.clearChats(); S.chats = []; S.cur = newChat(); S.folder = null; closeSheet(); drawAll(); } }),
    });
}
function shortcutsSheet() {
  const k = [["New chat", MOD + "Shift+O"], ["Search chats and commands", MOD + "K"], ["Show or hide the sidebar", MOD + "Shift+S"], ["Commands", "type /"], ["Send", S.settings.enterSends ? "Enter" : MOD + "Enter"], ["New line", S.settings.enterSends ? "Shift+Enter" : "Enter"], ["Stop the reply", "Esc"], ["Edit your last message", "↑ in an empty box"], ["Dictate", MOD + "Shift+D"], ["Copy the last reply", MOD + "Shift+C"], ["This list", MOD + "/"]];
  sheet(`<h3>Keyboard shortcuts</h3><div class="keys">${k.map(([a, b]) => `<span>${a}</span><kbd>${b}</kbd>`).join("")}</div><div class="foot"><button class="btn" data-a="close">Done</button></div>`);
}

// ---------- commands ----------
const COMMANDS = [
  ["new", "Start a new chat", () => openChat()],
  ["model", "Choose a model, or change its memory", () => modelMenu($("#model"))],
  ["attach", "Attach files", () => $("#file-input").click()],
  ["folder", "Attach a folder for the model to work in", () => pickFolder()],
  ["agent", "Let the model run commands and drive apps on this computer", () => agentMenu($("#agent"))],
  ["permissions", "Choose what the model may do with your folder", () => S.folder ? permMenu($("#perm")) : toast("Attach a folder first; permissions apply to it.")],
  ["think", "Turn thinking before answering on or off", () => { S.settings.thinking = !S.settings.thinking; saveSettings(); drawTools(); toast(S.settings.thinking ? "The model will think first. Slower, sometimes better." : "Thinking is off."); }],
  ["usage", "See memory and usage", () => usageSheet()],
  ["retry", "Have the model answer again", () => retry()],
  ["stop", "Stop the reply being written", () => stop()],
  ["copy", "Copy the last reply", () => { const m = [...S.cur.messages].reverse().find(x => x.role === "assistant" && x.content); m ? copyText(m.content) : toast("No reply yet."); }],
  ["compact", "Summarize this chat and continue in a fresh one", () => compact()],
  ["instructions", "Set instructions the model always follows", () => settingsSheet()],
  ["style", "Change the writing style", () => showPop($("#attach"), `<h5>Writing style</h5>` + Object.entries(STYLES).map(([k, v]) => mi(k, v.name, { on: S.settings.style === k, check: 1 })).join(""), v => { S.settings.style = v; saveSettings(); toast("Style: " + STYLES[v].name); })],
  ["voice", "Dictate your message", () => toggleDictation()],
  ["speak", "Read replies aloud, on or off", () => { S.settings.speak = !S.settings.speak; saveSettings(); if (!S.settings.speak && window.speechSynthesis) speechSynthesis.cancel(); toast(S.settings.speak ? "Replies will be read aloud." : "Reading aloud is off."); }],
  ["export", "Save this chat as a Markdown file", () => exportChat(S.cur)],
  ["rename", "Rename this chat", () => startRename()],
  ["star", "Star or unstar this chat", async () => { S.cur.starred = !S.cur.starred; await saveChat(); drawHeader(); }],
  ["clear", "Delete this chat", () => chatMenu($("#chat-menu")) || null],
  ["theme", "Switch between light and dark", () => { S.settings.theme = (S.settings.theme === "dark" || (S.settings.theme === "auto" && matchMedia("(prefers-color-scheme: dark)").matches)) ? "light" : "dark"; saveSettings(); applyTheme(); }],
  ["settings", "Open settings", () => settingsSheet()],
  ["shortcuts", "Show keyboard shortcuts", () => shortcutsSheet()],
  ["help", "What can I do here?", () => helpSheet()],
];
function helpSheet() {
  sheet(`<h3>What you can do here</h3>
    <p><b>Type or dictate</b> a message. The microphone button turns speech into text; say "send message" to send it.</p>
    <p><b>Attach</b> files to include their text, or a folder for the model to work in. With a folder you choose a permission level: chat only, ask every time, read freely, or edit freely.</p>
    <p><b>Commands:</b> type <kbd>/</kbd> in the message box, or press <kbd>${MOD}K</kbd> anywhere.</p>
    <p><b>Model:</b> the button at the bottom right shows which model is running and lets the main computer switch.</p>
    <p><b>Usage:</b> the ring shows how full this chat's memory is. Click it for details.</p>
    <div class="foot"><button class="btn ghost" data-a="cmd" data-c="shortcuts">Keyboard shortcuts</button><button class="btn" data-a="close">Done</button></div>`);
}
function runSlash(text) {
  const name = text.slice(1).trim().split(/\s+/)[0].toLowerCase();
  const c = COMMANDS.find(x => x[0] === name) || COMMANDS.filter(x => x[0].startsWith(name))[0];
  if (!c || !name) return false;
  setTimeout(() => c[2](), 0); return true;
}
function hideSlash() { $("#slash").hidden = true; }
function drawSlash() {
  const v = $("#input").value, box = $("#slash");
  if (!/^\/[a-z]*$/i.test(v)) return hideSlash();
  const q = v.slice(1).toLowerCase(), list = COMMANDS.filter(c => c[0].startsWith(q));
  if (!list.length) return hideSlash();
  S.slashSel = Math.min(S.slashSel, list.length - 1);
  box.innerHTML = list.map((c, i) => `<button type="button" class="mi ${i === S.slashSel ? "sel" : ""}" data-cmd="${c[0]}" role="option"><span><b>/${c[0]}</b><span class="d">${c[1]}</span></span></button>`).join("");
  box.hidden = false; const sel = $(".sel", box); sel && sel.scrollIntoView({ block: "nearest" });
}
function palette() {
  sheet(`<div class="palette"><input id="pal-q" type="text" placeholder="Search chats and commands" autocomplete="off"><div class="list" id="pal-list"></div></div>`);
  let sel = 0, items = [];
  const draw = () => {
    const q = $("#pal-q").value.trim().toLowerCase();
    items = [...COMMANDS.filter(c => !q || c[0].includes(q) || c[1].toLowerCase().includes(q)).map(c => ({ label: c[1], k: "/" + c[0], run: c[2] })),
    ...S.chats.filter(c => !q || (c.title || "").toLowerCase().includes(q) || (c.text || "").includes(q)).sort((a, b) => b.updated - a.updated).slice(0, q ? 30 : 8).map(c => ({ label: c.title || "New chat", k: "chat", run: () => openChat(c.id) }))];
    sel = Math.min(sel, Math.max(0, items.length - 1));
    $("#pal-list").innerHTML = items.map((it, i) => `<button class="mi ${i === sel ? "sel" : ""}" data-i="${i}"><span>${esc(it.label)}</span><span class="k">${esc(it.k)}</span></button>`).join("") || `<div class="note">Nothing matches.</div>`;
    const s = $("#pal-list .sel"); s && s.scrollIntoView({ block: "nearest" });
  };
  const go = i => { const it = items[i]; if (!it) return; closeSheet(); setTimeout(it.run, 0); };
  $("#pal-q").oninput = () => { sel = 0; draw(); };
  $("#pal-q").onkeydown = e => { if (e.key === "ArrowDown") { sel = Math.min(items.length - 1, sel + 1); draw(); e.preventDefault(); } else if (e.key === "ArrowUp") { sel = Math.max(0, sel - 1); draw(); e.preventDefault(); } else if (e.key === "Enter") { e.preventDefault(); go(sel); } };
  $("#pal-list").onclick = e => { const b = e.target.closest("[data-i]"); if (b) go(+b.dataset.i); };
  draw();
}

// ---------- preview ----------
function showPreview(lang, code) {
  const doc = lang === "svg" ? `<!doctype html><body style="margin:0;display:grid;place-items:center;min-height:100vh">${code}</body>` : code;
  $("#preview-frame").srcdoc = doc; $("#preview-title").textContent = lang === "svg" ? "Picture preview" : "Page preview"; $("#preview").hidden = false;
}

// ---------- wiring ----------
async function loadChats() {
  const all = await Store.allChats();
  S.chats = all.map(c => ({ id: c.id, title: c.title, updated: c.updated, starred: c.starred, text: c.messages.filter(m => m.role !== "tool").map(m => m.content || "").join(" ").slice(0, 4000).toLowerCase() }));
  drawList();
}
function wire() {
  const input = $("#input");
  $("#side-close").innerHTML = ic("side"); $("#side-open").innerHTML = ic("side"); $("#chat-menu").innerHTML = ic("more");
  $("#attach").innerHTML = ic("clip"); $("#mic").innerHTML = ic("mic"); $("#preview-close").innerHTML = ic("x");
  $$("[data-ic]").forEach(e => e.innerHTML = icon(e.dataset.ic)); $("#kbd-new").textContent = MOD + "⇧O";
  const toggleSide = () => { document.body.classList.toggle("side-closed"); S.settings.sideClosed = document.body.classList.contains("side-closed"); saveSettings(); };
  $("#side-close").onclick = $("#side-open").onclick = toggleSide;
  $("#new-chat").onclick = () => openChat();
  $("#chat-search").oninput = e => { S.filter = e.target.value; drawList(); };
  $("#project-list").onclick = e => { if (e.target.closest("[data-a=new-task]")) return newTaskChat(); const tk = e.target.closest("[data-task]"); if (tk) return openTask(tk.dataset.task); if (e.target.closest("[data-a=all-projects]")) return projectsSheet(); const it = e.target.closest("[data-proj]"); if (it) openProject(it.dataset.proj, it.dataset.name); };
  $("#chat-list").onclick = e => { const more = e.target.closest("[data-a=chat-more]"); if (more) { e.stopPropagation(); return chatMenu(more, more.dataset.id); } const it = e.target.closest(".chat-item"); if (it) openChat(it.dataset.id); };
  $("#chat-list").onkeydown = e => { if (e.key === "Enter" && e.target.classList.contains("chat-item")) openChat(e.target.dataset.id); };
  $("#cluster-chip").onclick = () => usageSheet();
  $("#open-settings").onclick = () => settingsSheet();
  $("#chat-title").onclick = () => { if ($("#chat-title").contentEditable !== "true") startRename(); };
  $("#chat-star").onclick = async () => { S.cur.starred = !S.cur.starred; await saveChat(); drawHeader(); };
  $("#chat-menu").onclick = e => chatMenu(e.currentTarget);
  $("#attach").onclick = e => attachMenu(e.currentTarget);
  $("#perm").onclick = e => permMenu(e.currentTarget);
  $("#agent").onclick = e => agentMenu(e.currentTarget);
  $("#conn").onclick = e => connMenu(e.currentTarget);
  $("#think").onclick = () => COMMANDS.find(c => c[0] === "think")[2]();
  $("#model").onclick = e => modelMenu(e.currentTarget);
  $("#usage").onclick = () => usageSheet();
  $("#mic").onclick = () => toggleDictation();
  $("#preview-close").onclick = () => { $("#preview").hidden = true; $("#preview-frame").srcdoc = ""; };
  $("#file-input").onchange = e => { addFiles([...e.target.files]); e.target.value = ""; };
  $("#dir-input").onchange = e => {
    const files = [...e.target.files]; e.target.value = ""; if (!files.length) return;
    const root = files[0].webkitRelativePath.split("/")[0], map = new Map();
    for (const f of files) { const p = f.webkitRelativePath.split("/").slice(1); if (!p.some(x => SKIP_DIRS.has(x) || x.startsWith("."))) map.set(p.join("/"), f); }
    setFolder({ name: root, kind: "snapshot", files: map });
  };
  $("#composer").onsubmit = e => { e.preventDefault(); if (isTask() && S.task && S.task.status === "working" && !S.task.q && !$("#input").value.trim()) return taskStop(); S.gen ? stop() : send(); };
  $("#thread").addEventListener("click", e => {
    const b = e.target.closest("[data-a^=q-]"); if (!b) return;
    taskAnswer({ "q-yes": "yes", "q-no": "no", "q-always": "always" }[b.dataset.a]);
  });
  input.oninput = () => { autosize(); S.slashSel = 0; drawSlash(); drawTools(); };
  input.onkeydown = e => {
    const slashOpen = !$("#slash").hidden;
    if (slashOpen && (e.key === "ArrowDown" || e.key === "ArrowUp")) { const n = $$("#slash .mi").length; S.slashSel = (S.slashSel + (e.key === "ArrowDown" ? 1 : n - 1)) % n; drawSlash(); return e.preventDefault(); }
    if (slashOpen && (e.key === "Tab" || e.key === "Enter")) { const b = $("#slash .sel"); if (b) { e.preventDefault(); input.value = ""; autosize(); hideSlash(); drawTools(); runSlash("/" + b.dataset.cmd); return; } }
    if (e.key === "Escape" && slashOpen) { hideSlash(); return e.preventDefault(); }
    // A task's question is open: Enter on an empty box says yes, Backspace or Delete says no,
    // anything typed is the answer in your own words.
    if (isTask() && S.task && S.task.q && !input.value && !e.isComposing) {
      if (e.key === "Enter") { e.preventDefault(); return taskAnswer("yes"); }
      if (e.key === "Backspace" || e.key === "Delete") { e.preventDefault(); return taskAnswer("no"); }
    }
    if (e.key === "Enter" && !e.isComposing) {
      const mod = e.metaKey || e.ctrlKey;
      if (S.settings.enterSends ? !e.shiftKey && !e.altKey : mod) { e.preventDefault(); if (!S.gen) send(); }
    }
    if (e.key === "ArrowUp" && !input.value && !S.gen) { const i = S.cur.messages.map(m => m.role).lastIndexOf("user"); if (i >= 0) { e.preventDefault(); editMessage(i); } }
  };
  input.onpaste = e => {
    const files = [...(e.clipboardData.files || [])];
    if (files.length) { e.preventDefault(); addFiles(files); return; }
    // Long pasted text becomes an attachment, so the message box stays readable.
    const t = e.clipboardData.getData("text/plain");
    if (t && (t.length > 4000 || t.split("\n").length > 60)) {
      e.preventDefault();
      const n = S.pending.filter(f => /^Pasted text/.test(f.name)).length;
      S.pending.push({ name: "Pasted text" + (n ? " " + (n + 1) : ""), size: new Blob([t]).size, text: t.length > TEXT_LIMIT ? t.slice(0, TEXT_LIMIT) + "\n[cut off here]" : t, pasted: true });
      drawChips(); drawTools(); toast("Long text attached. Click its name to put it back in the box.");
    }
  };
  $("#slash").onclick = e => { const b = e.target.closest("[data-cmd]"); if (b) { input.value = ""; autosize(); hideSlash(); drawTools(); runSlash("/" + b.dataset.cmd); input.focus(); } };
  $("#chips").onclick = e => {
    const b = e.target.closest("[data-a]");
    if (b && b.dataset.a === "unattach") { S.pending.splice(+b.dataset.i, 1); drawChips(); drawTools(); }
    else if (!b && e.target.closest("[data-pasted]")) { const i = +e.target.closest("[data-pasted]").dataset.pasted, f = S.pending[i]; S.pending.splice(i, 1); const inp = $("#input"); inp.value = (inp.value ? inp.value + "\n" : "") + f.text; autosize(); drawChips(); drawTools(); }
    else if (b && b.dataset.a === "detach") setFolder(null);
    else if (e.target.closest(".chip.folder") && S.folder && S.folder.needsAccess) reconnectFolder();
  };
  // drag and drop
  const wrap = $("#composer-wrap"); let depth = 0;
  window.addEventListener("dragenter", e => { if (e.dataTransfer && [...e.dataTransfer.types].includes("Files")) { depth++; $("#drop-hint").hidden = false; } });
  window.addEventListener("dragleave", () => { depth = Math.max(0, depth - 1); if (!depth) $("#drop-hint").hidden = true; });
  window.addEventListener("dragover", e => e.preventDefault());
  window.addEventListener("drop", async e => {
    e.preventDefault(); depth = 0; $("#drop-hint").hidden = true;
    // Take everything out of the drop before waiting for anything: the browser empties the
    // list at the first wait, which used to lose every file after the first.
    const taken = [...(e.dataTransfer.items || [])].filter(it => it.kind === "file").map(it => ({
      file: it.getAsFile(), entry: it.webkitGetAsEntry ? it.webkitGetAsEntry() : null,
      handle: it.getAsFileSystemHandle ? it.getAsFileSystemHandle().catch(() => null) : Promise.resolve(null),
    }));
    const files = [];
    for (const it of taken) {
      if (it.entry && it.entry.isDirectory) {
        const h = await it.handle;
        if (h && h.kind === "directory") await setFolder({ name: h.name, kind: "handle", handle: h });
        else toast("To attach a folder in this browser, use the paperclip button.");
        continue;
      }
      if (it.file) files.push(it.file);
    }
    if (files.length) addFiles(files);
  });
  // thread actions
  $("#thread").onclick = e => {
    const b = e.target.closest("[data-a]"); if (!b) return;
    const el = b.closest(".msg"), i = el ? +el.dataset.i : -1, m = S.cur.messages[i], a = b.dataset.a;
    if (a === "copy-code") copyText($("code", b.closest(".code")).textContent);
    else if (a === "preview") showPreview(b.closest(".code").dataset.lang, $("code", b.closest(".code")).textContent);
    else if (a === "suggest") { $("#input").value = b.textContent; autosize(); drawTools(); $("#input").focus(); }
    else if (a === "copy-msg") {
      if (m.role === "user") return copyText(m.content);
      let j = i; while (j > 0 && S.cur.messages[j - 1].role !== "user") j--;
      copyText(S.cur.messages.slice(j, i + 1).filter(x => x.role === "assistant" && x.content).map(x => x.content).join("\n\n"));
    }
    else if (a === "retry") retry();
    else if (a === "speak") speakText(m.content);
    else if (a === "edit-msg") editMessage(i);
    else if (a === "edit-cancel") drawThread();
    else if (a === "edit-save") { const j = +b.dataset.i, t = $("textarea", b.closest(".edit-box")).value.trim(); if (!t) return; if (t === S.cur.messages[j].content && S.cur.messages.length > j + 1) { drawThread(); return; } branchFrom(j, t); drawThread(); saveChat().then(generate); }
    else if (a === "branch") switchBranch(i, +b.dataset.d);
  };
  $("#banner").onclick = e => { if (e.target.dataset.a === "models") modelMenu($("#model")); };
  $("#modal").onclick = e => {
    if (e.target.id === "modal") return closeSheet();
    const b = e.target.closest("[data-a]"); if (!b) return;
    if (b.dataset.a === "close") return closeSheet();
    if (b.dataset.a === "cmd") { closeSheet(); return runSlash("/" + b.dataset.c); }
    const h = $("#modal").handlers[b.dataset.a]; h && h();
  };
  document.addEventListener("mousedown", e => { if (!$("#pop").hidden && !e.target.closest("#pop") && e.target.closest("button") !== popFor) hidePop(); });
  document.addEventListener("keydown", e => {
    const mod = e.metaKey || e.ctrlKey, k = e.key.toLowerCase();
    if (e.key === "Escape") { if (!$("#pop").hidden) return hidePop(); if (!$("#modal").hidden) return closeSheet(); if (S.rec) return stopDictation(); if (S.gen) return stop(); }
    if (mod && k === "k") { e.preventDefault(); palette(); }
    else if (mod && e.shiftKey && k === "o") { e.preventDefault(); openChat(); }
    else if (mod && e.shiftKey && k === "s") { e.preventDefault(); toggleSide(); }
    else if (mod && e.shiftKey && k === "d") { e.preventDefault(); toggleDictation(); }
    else if (mod && e.shiftKey && k === "c") { e.preventDefault(); runSlash("/copy"); }
    else if (mod && k === "/") { e.preventDefault(); shortcutsSheet(); }
  });
  window.addEventListener("resize", hidePop);
}

(async function init() {
  S.settings = { ...DEFAULTS, ...((await Store.get("settings").catch(() => null)) || {}) };
  S.usage = (await Store.get("usage").catch(() => null)) || S.usage;
  applyTheme();
  if (S.settings.sideClosed || window.innerWidth <= 820) document.body.classList.add("side-closed");
  S.cur = newChat();
  wire();
  await loadChats();
  drawAll();
  await poll();
  drawThread();
  $("#input").focus();
})();
