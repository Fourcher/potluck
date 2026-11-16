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

async function api(path, body) {
  const opt = body === undefined ? {} : { method: "POST", headers: { "Content-Type": "application/json", "X-Potluck": "1" }, body: JSON.stringify(body) };
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
    <div class="card"><h3>This is my main computer</h3>
      <p class="muted">You sit at this one. It finds your other computers, lets you pick models, and shows the chat. It only lends its own muscle if you turn that on.</p>
      <p style="margin-top:16px"><button class="btn" data-a="role" data-role="main">Make this my main computer</button></p></div>
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

function nodeCard(n) {
  const i = n.info, L = i.limits || {};
  const badge = n.role === "head" ? `<span class="badge ok">Runs the model</span>` : n.role === "worker" ? `<span class="badge ok">Holds part of the model</span>` : "";
  const lend = !n.online ? `<span class="muted">Not connected</span>` : !n.active ? `<span class="muted">${L.enabled ? "Resting (outside its hours)" : "Not lending"}</span>` : `Lends <b>${gb(n.offer)}</b> of memory, ${Math.max(1, Math.round((i.cores || 1) * (L.pedal || 0) / 100))} of ${i.cores || "?"} cores`;
  return `<div class="card ${n.online ? "" : "off"}" data-node="${esc(i.id)}">
    <div class="row spread"><div class="row"><span class="dot ${n.online ? "on" : ""}" title="${n.online ? "Connected" : "Not connected"}"></span><h3>${esc(i.name)}</h3></div>
      <div class="row">${n.isMain ? `<span class="badge main">Main computer</span>` : ""}${badge}</div></div>
    ${specs(i)}
    <div class="lend">${lend}</div>
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
    </div>`;
}

// ---------- main computer: models ----------

function verdictBadge(f) {
  const map = { one: ["ok", "Runs on one computer"], pool: ["main", "Runs pooled"], no: ["bad", "Too big for now"], split: ["warn", "Not supported yet"] };
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
  const runs = f => f.verdict === "one" || f.verdict === "pool";
  // The best pick is the biggest size that runs here, leaving out the uncompressed originals.
  const best = files ? files.filter(f => runs(f) && !/^(BF16|F16|F32)$/.test(f.quant)).slice(-1)[0] : null;
  const rows = files && open ? `<div class="files">${files.map(f => `<div class="file">
      <span class="q">${esc(f.quant)}${best && f.file === best.file ? ` <span class="badge main">Best fit</span>` : ""}</span><span>${gb(f.size)}</span><span class="n">${verdictBadge(f)} <span class="muted small">${esc(f.note)}</span></span>
      <button class="btn sm ${runs(f) ? "" : "quiet"}" data-a="plan" data-repo="${esc(mod.repo)}" data-file="${esc(f.file)}" ${f.verdict === "split" ? "disabled" : ""}>${f.verdict === "no" ? "Why not?" : "Set up"}</button>
    </div>`).join("")}</div>` : "";
  const fit = files ? (() => { const n = files.filter(runs).length; return n ? `<span class="badge ${best && best.verdict === "pool" ? "main" : "ok"}">${best && best.verdict === "pool" ? "Runs pooled across your computers" : "Runs here"}</span>` : `<span class="badge bad">Too big for your setup</span>`; })() : "";
  return `<div class="model"><div class="row spread"><div><h3>${esc(mod.repo)}</h3>${mod.blurb ? `<p class="muted small">${esc(mod.blurb)}</p>` : ""}
      ${mod.downloads ? `<p class="muted small">${mod.downloads.toLocaleString()} downloads</p>` : ""}</div>
      <div class="row">${fit}${best ? `<button class="btn sm" data-a="plan" data-repo="${esc(mod.repo)}" data-file="${esc(best.file)}">Set up best fit (${gb(best.size)})</button>` : ""}
      <button class="btn sm ghost" data-a="files" data-repo="${esc(mod.repo)}">${open ? "Hide sizes" : files ? `All ${files.length} sizes` : "Show sizes"}</button></div></div>${rows}</div>`;
}

function viewModels() {
  if (store.results === null && !store.loading) searchStore();
  const pf = store.source === "pirateface";
  return `<h1>Models</h1>
    <p class="muted">Every size is checked against the computers you have connected right now.</p>
    <div class="tools">
      <div class="seg"><button class="${pf ? "" : "on"}" data-a="source" data-source="hf">Hugging Face</button><button class="${pf ? "on" : ""}" data-a="source" data-source="pirateface">PirateFace</button></div>
      <input type="search" id="q" placeholder="Search models, e.g. qwen 27b" value="${esc(store.q)}" aria-label="Search models">
      <button class="btn" data-a="search">Search</button>
    </div>
    ${pf ? `<div class="note">PirateFace lists community torrents of Hugging Face models, for when Hugging Face is unreachable. Potluck only reads the list. Download with your own torrent app, then compare the file's SHA-256 with the one Hugging Face publishes before you trust it. Downloading torrents inside Potluck is planned.</div>` : ""}
    ${store.error ? `<div class="err">${esc(store.error)}</div>` : ""}
    ${store.loading ? `<p class="muted">Searching…</p>` : (store.results || []).map(modelCard).join("") || `<p class="muted">Nothing found.</p>`}
    ${pf ? "" : `<h2>Have a direct link?</h2><div class="tools"><input type="text" id="url" placeholder="https://…/model.gguf" style="flex:1;min-width:260px" aria-label="Direct link to a model file"><button class="btn quiet" data-a="planurl">Check it</button></div>`}`;
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
  const seg = (n, color, striped) => n > 0 ? `<div style="flex:${n};background:${color};${striped ? "background-image:repeating-linear-gradient(45deg,rgba(255,255,255,.35) 0 6px,transparent 6px 12px)" : ""}"></div>` : "";
  segs.push(seg(head.cpuLayers, COLORS[0]));
  plan.shares.slice(1).forEach((s, k) => {
    segs.push(seg(s.cpuLayers, COLORS[(k + 1) % COLORS.length]));
    legend.push(`<div><i style="background:${COLORS[(k + 1) % COLORS.length]}"></i><b>${esc(s.name)}</b>: ${s.cpuLayers} of ${total} layers, about ${gb(s.cpuLayers * layerGB)}</div>`);
  });
  segs.push(seg(head.gpuLayers, COLORS[0], true));
  const headBits = [head.cpuLayers ? `${head.cpuLayers} layers in memory` : "", head.gpuLayers ? `${head.gpuLayers} on its graphics card` : ""].filter(Boolean).join(" + ") || "coordinates only";
  legend.unshift(`<div><i style="background:${COLORS[0]}"></i><b>${esc(head.name)}</b> (runs the model): ${headBits}, about ${gb((head.cpuLayers + head.gpuLayers) * layerGB)}</div>`);
  return `<div class="plan" role="img" aria-label="How the model's layers are spread over the computers">${segs.join("")}</div><div class="legend">${legend.join("")}</div>`;
}

function viewSheet() {
  if (!sheet) return "";
  if (sheet.loading) return `<div class="sheet"><div class="card"><p>Reading the model's blueprint and working out a layout…</p></div></div>`;
  const p = sheet.plan, m = sheet.model, info = sheet.info;
  const verdict = !p.fits ? `<div class="err"><b>This one does not fit yet.</b> ${esc(p.reason)} Add a computer, press a pedal further down, or pick a smaller size.</div>`
    : p.needsPool ? `<div class="banner" style="border-color:var(--ok);background:var(--ok-soft)"><b>Too big for any one of your computers. Runs across ${p.machines} of them together.</b></div>`
    : `<div class="note">Fits on ${esc(p.shares[0].name)} by itself, so it will run there${p.machines > 1 ? " with help from the others" : ""}.</div>`;
  return `<div class="sheet" data-a="closesheet"><div class="card" data-stop>
    <div class="row spread"><h1 style="font-size:21px">${esc(m.title || m.file)}</h1><button class="link" data-a="closesheet">Close</button></div>
    <p class="muted small">${gb(m.size)} · ${info.layers} layers · ${esc(m.repo || "direct link")}</p>
    ${verdict}${p.fits ? planBar(p) : ""}
    ${p.fits ? `<div class="row" style="margin-top:16px"><label class="small">Conversation memory <select id="ctx"><option value="4096">Normal (4,096 tokens)</option><option value="8192">Long (8,192)</option><option value="16384">Very long (16,384)</option></select></label>
      <button class="btn" data-a="start">Download and run</button></div>
      <p class="muted small">Each computer downloads its own share. The first start takes as long as the download; later starts take a few minutes.</p>` : ""}
  </div></div>`;
}

async function openPlan(model) {
  sheet = { loading: true }; draw();
  try { const r = await api("/api/plan", model); sheet = r; }
  catch (e) { sheet = null; toast(e.message); }
  draw();
}

// ---------- main computer: running ----------

function viewRun() {
  const m = S.main, c = m.cluster;
  if (c.phase === "idle") {
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
      <p class="muted">${p.needsPool ? `Too big for any one of your computers. Pooled across ${p.machines}.` : `Running on ${p.machines || 1} ${p.machines > 1 ? "computers" : "computer"}.`}</p></div>
      <div class="row"><a class="btn" href="${esc(m.chatURL)}" target="_blank" rel="noopener">Open chat</a><button class="btn ghost" data-a="stop">Stop</button></div></div>
    ${planBar(p)}
    <div class="grid" style="margin-top:18px">
      <div class="card speed"><h3>Speed</h3>
        ${c.tps ? `<p><b>${c.tps.toFixed(1)}</b> tokens per second</p><p class="muted small">Measured on a short question. A token is about three quarters of a word; people read roughly 5 per second.</p>` : `<p class="muted">Not measured yet.</p>`}
        <p><button class="btn quiet sm" data-a="test" ${c.testing ? "disabled" : ""}>${c.testing ? "Measuring…" : "Measure speed"}</button></p>
        ${c.reply ? `<p class="small muted" style="margin-top:8px">"${esc(c.reply.slice(0, 360))}"</p>` : ""}</div>
      <div class="card"><h3>Use it from other apps</h3>
        <p class="muted small">Anything that speaks the OpenAI API can use your pooled model. On this computer the address is:</p>
        <p class="addr" style="font-size:17px">${esc(m.chatURL)}/v1</p></div>
    </div>
    <div class="grid" style="margin-top:16px">${nodes}</div>`;
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
  tab(t) { tab = t.dataset.tab; location.hash = tab; logText = ""; draw(true); },
  role(t) { act(() => api("/api/role", { role: t.dataset.role })); },
  pending(t) { act(() => api("/api/pending", { id: t.dataset.id, approve: !!t.dataset.ok })); },
  rename(t) {
    const name = prompt("Name for this computer:", t.dataset.name);
    if (!name || !name.trim()) return;
    const n = S.main ? S.main.nodes.find(x => x.info.id === t.dataset.id) : null;
    const limits = n ? n.info.limits : S.helper.info.limits;
    act(() => api("/api/node", { id: t.dataset.id, name: name.trim(), limits }));
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
  closesheet(t, e) { if (e.target.closest("[data-stop]") && !e.target.closest("button")) return; sheet = null; draw(true); },
  start() {
    const m = sheet.model, ctx = +$("#ctx").value;
    sheet = null; tab = "run"; location.hash = tab;
    act(() => api("/api/start", { ...m, ctx }));
  },
  again() { const m = (S.main.cluster.model) || S.main.lastModel; if (m) { logText = ""; act(() => api("/api/start", { ...m, ctx: 4096 })); } },
  stop() { logText = ""; act(() => api("/api/stop", {})); },
  test() { act(() => api("/api/test", {})); },
  magnet(t) { navigator.clipboard.writeText(t.dataset.magnet).then(() => toast("Torrent link copied. Paste it into your torrent app."), () => toast("Could not copy. Your browser blocked it.")); },
  tohf(t) { store.source = "hf"; store.q = t.dataset.repo.split("/").pop(); store.results = null; draw(true); },
};

document.addEventListener("click", e => {
  const t = e.target.closest("[data-a]");
  if (t && actions[t.dataset.a]) { e.preventDefault(); actions[t.dataset.a](t, e); }
});
$("#tabs").addEventListener("click", e => { const b = e.target.closest("button[data-tab]"); if (b) actions.tab(b); });
document.addEventListener("input", e => {
  holdUntil = Date.now() + 2500;
  if (e.target.matches("[data-c=pedal]")) { const b = $("[data-pedal]", e.target.closest(".limits")); b.textContent = `${e.target.value}% · ${pedalWord(+e.target.value)}`; }
});
document.addEventListener("change", e => {
  if (e.target.matches("[data-head]")) { const id = e.target.value; holdUntil = 0; return act(() => api("/api/head", { id })); }
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
