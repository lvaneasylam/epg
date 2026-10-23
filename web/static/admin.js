/* EPG 管理页逻辑 */
"use strict";

const $ = (id) => document.getElementById(id);
const esc = (s) => String(s ?? "").replace(/[&<>"]/g, (c) => ({
  "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;",
}[c]));

// token：页面经 /admin?token=xxx 打开时自动携带
const TOKEN = new URLSearchParams(location.search).get("token") || "";
const API = "/api/admin";

async function api(path, opts = {}) {
  const headers = Object.assign({}, opts.headers || {});
  if (TOKEN) headers["Authorization"] = "Bearer " + TOKEN;
  if (opts.body && typeof opts.body === "object") {
    opts = { ...opts, body: JSON.stringify(opts.body) };
    headers["Content-Type"] = "application/json";
  }
  const resp = await fetch(API + path, { ...opts, headers });
  if (resp.status === 401) {
    toast("未授权：请在 URL 上加 ?token=你的令牌");
    throw new Error("unauthorized");
  }
  const data = await resp.json().catch(() => ({}));
  if (!resp.ok) throw new Error(data.error || data.message || "HTTP " + resp.status);
  return data;
}

function toast(msg, ms = 2200) {
  const el = $("toast");
  el.textContent = msg;
  el.hidden = false;
  clearTimeout(el._t);
  el._t = setTimeout(() => (el.hidden = true), ms);
}

function ago(t) {
  if (!t) return "";
  const s = (Date.now() - new Date(t).getTime()) / 1000;
  if (s < 60) return "刚刚";
  if (s < 3600) return `${(s / 60) | 0}m ago`;
  if (s < 86400) return `${(s / 3600) | 0}h ago`;
  return `${(s / 86400) | 0}d ago`;
}

function fmtTime(t) {
  if (!t) return "—";
  return new Date(t).toLocaleString("zh-CN", { hour12: false });
}

/* ---------- Tabs ---------- */
document.querySelectorAll(".tab").forEach((tab) => {
  tab.addEventListener("click", () => {
    document.querySelectorAll(".tab").forEach((t) => t.classList.toggle("active", t === tab));
    document.querySelectorAll(".tab-panel").forEach((p) => (p.hidden = p.id !== "tab-" + tab.dataset.tab));
    if (tab.dataset.tab === "bindings") loadBindings();
  });
});

/* ---------- EPG 源 ---------- */
let sources = [];

async function loadSources() {
  try {
    sources = await api("/sources");
  } catch (e) { toast("加载失败：" + e.message); return; }
  const tb = $("sourceRows");
  tb.innerHTML = "";
  $("sourceEmpty").hidden = sources.length > 0;
  for (const s of sources) {
    const tr = document.createElement("tr");
    const st = s.state || "idle";
    const stateTxt = { ok: "正常", error: "错误", running: "同步中", idle: "未同步" }[st] || st;
    tr.innerHTML = `
      <td><span class="src-name">${esc(s.name)}</span>${s.enabled ? "" : '<span class="tag off">禁用</span>'}</td>
      <td><div class="src-url ${st === "error" ? "err" : ""}" title="${esc(s.url)}">${esc(s.url)}</div></td>
      <td>${s.priority}</td>
      <td>${s.interval_min > 0 ? s.interval_min + "m" : "全局"}</td>
      <td><div class="state"><i class="dot ${st === "ok" ? "ok" : st === "error" ? "err" : st === "running" ? "run" : "idle"}"></i>
        ${stateTxt} <span class="state-time">${st === "running" ? "" : ago(s.last_done)}</span></div>
        ${st === "error" && s.message ? `<div class="state-msg" title="${esc(s.message)}">${esc(s.message)}</div>` : ""}</td>
      <td><div class="cover">${s.stat.channel_count} ch · ${Number(s.stat.program_count).toLocaleString()} prog
        ${s.stat.cover_until ? `<span class="until">覆盖至 ${fmtTime(s.stat.cover_until)}</span>` : ""}</div></td>
      <td><div class="op-cell">
        <label class="switch" title="启用/禁用">
          <input type="checkbox" ${s.enabled ? "checked" : ""} data-toggle="${s.id}">
          <i></i>
        </label>
        <button class="btn ghost mini" data-sync="${s.id}" ${st === "running" ? "disabled" : ""}>刷新</button>
        <button class="btn ghost mini" data-edit="${s.id}">编辑</button>
        <button class="btn danger mini" data-del="${s.id}">删除</button>
      </div></td>`;
    tb.appendChild(tr);
  }
}

$("sourceRows").addEventListener("click", async (e) => {
  const btn = e.target.closest("button");
  if (!btn) return;
  if (btn.dataset.toggle !== undefined) return;
  const id = btn.dataset.sync || btn.dataset.edit || btn.dataset.del;
  if (!id) return;
  const src = sources.find((s) => s.id === +id);
  if (btn.dataset.sync !== undefined) {
    try { toast((await api(`/sources/${id}/sync`, { method: "POST" })).message); setTimeout(loadSources, 1500); }
    catch (err) { toast(err.message); }
  } else if (btn.dataset.edit !== undefined) {
    openSourceModal(src);
  } else if (btn.dataset.del !== undefined) {
    if (!confirm(`确认删除源「${src.name}」？其已入库的频道与节目将一并清除。`)) return;
    try { await api(`/sources/${id}`, { method: "DELETE" }); toast("已删除"); loadSources(); }
    catch (err) { toast(err.message); }
  }
});

$("sourceRows").addEventListener("change", async (e) => {
  const id = e.target.dataset.toggle;
  if (id === undefined) return;
  try {
    await api(`/sources/${id}/toggle`, { method: "POST", body: { enabled: e.target.checked } });
    toast(e.target.checked ? "已启用" : "已禁用（数据保留，不再同步）");
    loadSources();
  } catch (err) { toast(err.message); loadSources(); }
});

$("addSourceBtn").addEventListener("click", () => openSourceModal(null));
$("syncAllBtn").addEventListener("click", async () => {
  try { toast("已触发全量同步…"); await fetch("/api/sync", { method: "POST" }); setTimeout(loadSources, 2000); }
  catch { toast("触发失败"); }
});
$("refreshIconsBtn").addEventListener("click", async () => {
  try { toast((await api("/icons/refresh", { method: "POST" })).message); }
  catch (err) { toast(err.message); }
});

let editingId = null;
function openSourceModal(src) {
  editingId = src ? src.id : null;
  $("sourceModalTitle").textContent = src ? "编辑源：" + src.name : "添加源";
  $("fName").value = src?.name || "";
  $("fUrl").value = src?.url || "";
  $("fPriority").value = src?.priority ?? 10;
  $("fInterval").value = src?.interval_min ?? 0;
  $("fEnabled").checked = src ? !!src.enabled : true;
  $("sourceModal").hidden = false;
}

$("saveSourceBtn").addEventListener("click", async () => {
  const body = {
    name: $("fName").value.trim(), url: $("fUrl").value.trim(),
    priority: +$("fPriority").value || 0, interval_min: +$("fInterval").value || 0,
    enabled: $("fEnabled").checked,
  };
  if (!body.name || !body.url) { toast("名称与地址必填"); return; }
  try {
    if (editingId) await api(`/sources/${editingId}`, { method: "PUT", body });
    else await api("/sources", { method: "POST", body });
    $("sourceModal").hidden = true;
    toast("已保存"); loadSources();
  } catch (err) { toast(err.message); }
});

/* ---------- 频道绑定 ---------- */
let bindings = [];
let bindFilterMode = "all";

async function loadBindings() {
  try { bindings = await api("/bindings"); }
  catch (e) { toast("加载失败：" + e.message); return; }
  renderBindStats();
  renderBindRows();
}

function renderBindStats() {
  const manual = bindings.filter((b) => b.mode === "manual").length;
  const auto = bindings.length - manual;
  $("bindStats").innerHTML = `
    <span>频道 <b>${bindings.length}</b></span>
    <span>自动 <b>${auto}</b></span>
    <span class="lock">🔒 手动锁定 <b>${manual}</b></span>`;
}

function renderBindRows() {
  const kw = $("bindSearch").value.trim().toLowerCase();
  const tb = $("bindRows");
  tb.innerHTML = "";
  const list = bindings.filter((b) =>
    (bindFilterMode === "all" || b.mode === bindFilterMode) &&
    (!kw || b.channel_name.toLowerCase().includes(kw)));
  $("bindEmpty").hidden = list.length > 0;
  for (const b of list) {
    const tr = document.createElement("tr");
    tr.innerHTML = `
      <td>${esc(b.channel_name)}</td>
      <td><span class="bind-mode ${b.mode}">${b.mode === "manual" ? "🔒 手动" : "🔄 自动"}</span></td>
      <td>${esc(b.target_name || "—")}</td>
      <td>${esc(b.target_source || "—")}</td>
      <td>${b.candidates > 0 ? "+" + b.candidates + " 个备用源" : "—"}</td>
      <td title="${fmtTime(b.channel_updated_at)}">${ago(b.channel_updated_at)}</td>
      <td><input type="checkbox" class="vis-check" data-hide="${esc(b.channel_name)}"
        ${b.hidden ? "" : "checked"} title="勾选=首页显示"></td>
      <td><div class="op-cell">
        <button class="btn ghost mini" data-pick="${esc(b.channel_name)}">选择</button>
        ${b.mode === "manual" ? `<button class="btn danger mini" data-unbind="${esc(b.channel_name)}">解绑</button>` : ""}
      </div></td>`;
    tb.appendChild(tr);
  }
}

$("bindSearch").addEventListener("input", renderBindRows);
$("bindFilter").addEventListener("click", (e) => {
  const b = e.target.closest("button");
  if (!b) return;
  bindFilterMode = b.dataset.f;
  $("bindFilter").querySelectorAll("button").forEach((x) => x.classList.toggle("active", x === b));
  renderBindRows();
});

// 首页显示开关：勾选=显示，取消=隐藏（仅影响首页时间轴，对外接口不变）
$("bindRows").addEventListener("change", async (e) => {
  const cb = e.target.closest("[data-hide]");
  if (!cb) return;
  const name = cb.dataset.hide;
  const hidden = !cb.checked;
  cb.disabled = true;
  try {
    await api("/hidden", { method: "PUT", body: { channel_name: name, hidden } });
    const b = bindings.find((x) => x.channel_name === name);
    if (b) b.hidden = hidden;
    toast(hidden ? "已隐藏，首页不再显示" : "已恢复首页显示");
  } catch (err) {
    cb.checked = hidden; // 失败回滚
    toast(err.message);
  }
  cb.disabled = false;
});

let bindingName = null;
let bindingSel = null;
let bindingCands = []; // 候选全量数据（含全日节目），供对比浮窗渲染

$("bindRows").addEventListener("click", async (e) => {
  const pick = e.target.closest("[data-pick]")?.dataset.pick;
  const unbind = e.target.closest("[data-unbind]")?.dataset.unbind;
  if (unbind) {
    try {
      await api("/bindings/" + encodeURIComponent(unbind), { method: "DELETE" });
      toast("已解除，恢复自动选取"); loadBindings();
    } catch (err) { toast(err.message); }
    return;
  }
  if (!pick) return;
  bindingName = pick;
  bindingSel = null;
  bindingCands = [];
  $("bindTargetName").textContent = pick;
  $("bindCandidates").innerHTML = "加载中…";
  $("comparePop").hidden = true;
  $("bindModal").hidden = false;
  try {
    const cands = await api("/bindings/candidates?name=" + encodeURIComponent(pick));
    bindingCands = cands;
    $("bindCandidates").innerHTML = cands.length
      ? cands.map((c, i) => `
        <div class="cand ${i === 0 ? "sel" : ""}" data-id="${c.id}">
          <div class="cn">${esc(c.name)}</div>
          <div class="cs">来源 ${esc(c.source)} · 优先级 ${c.priority} · 今日节目 ${c.today_programs ?? "?"} 条</div>
        </div>`).join("")
      : '<div class="hint">无候选（该名字无跨源频道）</div>';
    if (cands.length) {
      // 默认高亮"今日有节目"且优先级最优的候选
      const best = cands.find((c) => c.today_programs > 0) || cands[0];
      bindingSel = best.id;
      $("bindCandidates").querySelectorAll(".cand").forEach((x) =>
        x.classList.toggle("sel", +x.dataset.id === best.id));
    }
  } catch (err) { $("bindCandidates").innerHTML = "加载失败：" + esc(err.message); }
});

$("bindCandidates").addEventListener("click", (e) => {
  const c = e.target.closest(".cand");
  if (!c) return;
  bindingSel = +c.dataset.id;
  $("bindCandidates").querySelectorAll(".cand").forEach((x) => x.classList.toggle("sel", x === c));
  if (!$("comparePop").hidden) renderCompare(); // 对比窗开着时左列（当前选择）跟着换
});

/* 悬停候选 → 浮窗两列对比：左=当前选择，右=悬停候选 */
function renderCompare(hoverId) {
  const sel = bindingCands.find((c) => c.id === bindingSel);
  const hov = bindingCands.find((c) => c.id === hoverId) || sel; // 未指定时右列与左列一致
  if (!sel) { $("comparePop").hidden = true; return; }
  const item = (p) => `<div><span>${esc(p.start)}</span>${esc(p.title)}</div>`;
  const list = (c) => (c?.programs?.length
    ? c.programs.map(item).join("") : "<i>当日无节目数据</i>");
  $("cpLeftTitle").textContent = `当前选择 · ${sel.source}（${sel.today_programs} 条）`;
  $("cpRightTitle").textContent = hov === sel
    ? `悬停候选 · ${hov.source}（与当前一致）`
    : `悬停候选 · ${hov.source}（${hov.today_programs} 条）`;
  $("cpLeft").innerHTML = list(sel);
  $("cpRight").innerHTML = list(hov);
  $("comparePop").hidden = false;
}

$("bindCandidates").addEventListener("mouseover", (e) => {
  const c = e.target.closest(".cand");
  if (c) renderCompare(+c.dataset.id);
});
$("cpClose").addEventListener("click", () => ($("comparePop").hidden = true));

$("saveBindBtn").addEventListener("click", async () => {
  if (!bindingSel) { toast("请先选择来源频道"); return; }
  try {
    await api("/bindings", { method: "PUT", body: { channel_name: bindingName, target_channel_id: bindingSel } });
    $("bindModal").hidden = true;
    $("comparePop").hidden = true;
    toast("已锁定绑定"); loadBindings();
  } catch (err) { toast(err.message); }
});

/* ---------- 弹窗通用关闭 ---------- */
document.querySelectorAll("[data-close]").forEach((b) =>
  b.addEventListener("click", () => {
    $("sourceModal").hidden = true;
    $("bindModal").hidden = true;
    $("comparePop").hidden = true;
  }));
document.querySelectorAll(".modal-mask").forEach((m) =>
  m.addEventListener("click", (e) => { if (e.target === m) { m.hidden = true; $("comparePop").hidden = true; } }));

/* ---------- 启动 ---------- */
loadSources();
setInterval(() => { if (!$("tab-sources").hidden) loadSources(); }, 10000);
