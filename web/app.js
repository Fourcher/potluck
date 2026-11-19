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
  const lend = !n.online ? `<span class="muted">Not connected</span>` : !n.active ? `<span class="muted">${L.enabled ? "Resting (outside its hours)" : "Not lending"}</span>` : `Lends <b>${gb(n.offer)}</b> of memory, ${Math.max(1, Math.round((i.perfThreads || i.cores || 1) * (L.pedal || 0) / 100))} of ${i.perfThreads || i.cores || "?"} ${i.perfThreads ? "fast-core threads" : "cores"}`;
  const slow = !n.isMain && n.online && n.linkMs > 30 ? `<div class="note small">Slow connection to this computer: about ${Math.round(n.linkMs)} ms per round trip. A network cable, or a spot nearer the router, would speed it up.</div>` : "";
  return `<div class="card ${n.online ? "" : "off"}" data-node="${esc(i.id)}">
    <div class="row spread"><div class="row"><span class="dot ${n.online ? "on" : ""}" title="${n.online ? "Connected" : "Not connected"}"></span><h3>${esc(i.name)}</h3></div>
      <div class="row">${n.isMain ? `<span class="badge main">Main computer</span>` : ""}${badge}</div></div>
    ${specs(i)}
    <div class="lend">${lend}</div>${slow}
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
    ${pf ? "" : diskView()}
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
      return `<div class="file"><span class="q">${esc(f.file.replace(/\.gguf$/, ""))}</span><span>${gb(f.size)}</span>
        <span class="n muted small">${f.complete ? (later ? "Part of a model that comes in several files" : "On " + esc(n.info.name)) : "Unfinished download"}${sum ? `<br>SHA-256: <span style="user-select:all;overflow-wrap:anywhere">${esc(sum)}</span>` : ""}</span>
        <span class="row">${f.complete && !later ? `<button class="btn sm" data-a="planlocal" data-node="${esc(id)}" data-file="${esc(f.file)}">Set up</button>` : ""}
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

function viewSheet() {
  if (!sheet) return "";
  if (sheet.loading) return `<div class="sheet"><div class="card"><p>Reading the model's blueprint and working out a layout…</p></div></div>`;
  const p = sheet.plan, solo = sheet.solo, stream = sheet.stream, m = sheet.model, info = sheet.info;
  const isLocal = m.source === "local";
  const go = isLocal ? "Run it" : "Download and run";
  const ctx = `<label class="small">Conversation memory <select id="ctx"><option value="4096">Normal (4,096 tokens)</option><option value="8192">Long (8,192)</option><option value="16384">Very long (16,384)</option></select></label>`;
  let body;
  if (p.fits) {
    const verdict = p.needsPool ? `<div class="banner" style="border-color:var(--ok);background:var(--ok-soft)"><b>Too big for the memory of any one of your computers. Runs across ${p.machines} of them together.</b></div>`
      : `<div class="note">Fits in ${esc(p.shares[0].name)}'s memory, so it runs there${p.machines > 1 ? " with help from the others" : ""}.</div>`;
    body = `${verdict}${planBar(p)}
      <div class="row" style="margin-top:16px">${ctx}<button class="btn" data-a="start">${go}</button></div>
      <p class="muted small">${isLocal ? "The file is already on " + esc(p.shares[0].name) + ". Helpers receive their share from it over your network, which is slower the first time than a download."
        : "Each computer downloads its own share. The first start takes as long as the download; later starts take a few minutes."}</p>`;
    if (p.needsPool && solo && solo.fits) {
      body += `<h2 style="font-size:16px">Or: ${esc(solo.shares[0].name)} alone, with help from its disk</h2>
        <p class="muted small">The model is about ${gb(solo.overflow)} bigger than ${esc(solo.shares[0].name)}'s free memory. It can keep most of it in memory and read the rest from disk as needed.
        No network in the way: with an SSD and this kind of model that can match or beat pooling; with a hard disk it is slow. Try both and compare with "Measure speed".</p>
        <button class="btn ghost" data-a="start" data-solo="${esc(solo.head)}">Run on ${esc(solo.shares[0].name)} alone</button>`;
    }
  } else if (solo && solo.fits) {
    body = `<div class="note"><b>Does not fit in your computers' pooled memory.</b> ${esc(p.reason)}</div>
      <h2 style="font-size:16px">It can still run on ${esc(solo.shares[0].name)}, with help from its disk</h2>
      <p class="muted small">About ${gb(solo.overflow)} of it will not fit in memory and is read from disk as needed. Fine with an SSD, slow with a hard disk.</p>${planBar(solo)}
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
    <p class="muted small">${gb(m.size)} · ${info.layers} layers · ${esc(isLocal ? "already on one of your computers" : m.repo || "direct link")}${m.parts && m.parts.length > 1 ? ` · comes as ${m.parts.length} files` : ""}</p>
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

function viewRun() {
  const m = S.main, c = m.cluster;
  if (c.phase === "idle") {
    if (m.pauseWhy) return `<h1>Waiting for your computers</h1><div class="note">${esc(m.pauseWhy)}</div>
      <p style="margin-top:14px"><button class="btn ghost" data-a="stop">Do not restart it</button></p>`;
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
        <label class="sw" style="margin-top:10px"><input type="checkbox" data-share ${m.shareChat ? "checked" : ""}> Let phones and other devices on my network use it too</label>
        ${m.shareChat ? `<p class="muted small" style="margin-top:6px">Open this on any device on your network. There is no password, so only switch this on at home.</p><p class="addr" style="font-size:17px">${esc(m.sharedURL)}</p>` : ""}</div>
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
    const m = sheet.model, ctx = +($("#ctx") ? $("#ctx").value : 4096), solo = t.dataset.solo || "";
    sheet = null; tab = "run"; location.hash = tab;
    act(() => api("/api/start", { ...m, ctx, solo }));
  },
  again() {
    const c = S.main.cluster, m = c.model || S.main.lastModel;
    if (m) { logText = ""; act(() => api("/api/start", { ...m, ctx: 4096, solo: c.plan && c.plan.stream ? "stream:" + c.plan.head : c.plan && c.plan.solo ? c.plan.head : "" })); }
  },
  stop() { logText = ""; act(() => api("/api/stop", {})); },
  test() { act(() => api("/api/test", {})); },
  planlocal(t) { openPlan({ source: "local", node: t.dataset.node, file: t.dataset.file }); },
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
  if (e.target.matches("[data-share]")) { const on = e.target.checked; holdUntil = 0; return act(() => api("/api/share", { on })); }
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
