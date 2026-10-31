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
    if (tab.dataset.tab === "icons") loadIcons();
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
$("warmEpgBtn").addEventListener("click", async () => {
  try { toast((await api("/warm", { method: "POST" })).message); }
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
    $("iconBindModal").hidden = true;
    $("comparePop").hidden = true;
  }));
document.querySelectorAll(".modal-mask").forEach((m) =>
  m.addEventListener("click", (e) => { if (e.target === m) { m.hidden = true; $("comparePop").hidden = true; } }));

/* ---------- 图标库 ---------- */
let iconDirs = [];      // 目录树：[{name, count, files:[{name,path,mod}]}]
let iconBinds = [];     // 频道图标绑定：[{channel_name, icon_path}]
let iconSelDir = "";    // 当前选中目录
let iconFlashPath = ""; // 上传/导入成功后高亮的图标路径
let iconChannelsLoaded = false;

async function loadIcons() {
  let dirs, binds;
  try {
    [dirs, binds] = await Promise.all([api("/icons/tree"), api("/icons/binds")]);
  } catch (e) { toast("图标库加载失败：" + e.message); return; }
  iconDirs = dirs || [];
  iconBinds = binds || [];
  if (!iconDirs.some((d) => d.name === iconSelDir)) iconSelDir = iconDirs[0]?.name || "";
  renderIconTree();
  renderIconStats();
  renderIconGrid();
  if (!iconChannelsLoaded) { iconChannelsLoaded = true; loadChannelNames(); }
}

// 频道名自动补全（绑定弹窗 datalist；失败不阻塞手动输入）
async function loadChannelNames() {
  try {
    const list = await api("/bindings");
    $("channelNames").innerHTML = list.map((b) => `<option value="${esc(b.channel_name)}">`).join("");
  } catch { /* ignore */ }
}

function renderIconStats() {
  const total = iconDirs.reduce((n, d) => n + d.count, 0);
  $("iconStats").innerHTML = `
    <span>目录 <b>${iconDirs.length}</b></span>
    <span>图标 <b>${total}</b></span>
    <span class="lock">🔖 图标绑定 <b>${iconBinds.length}</b></span>`;
}

function renderIconTree() {
  $("iconTree").innerHTML = iconDirs.map((d) => `
    <div class="it-item ${d.name === iconSelDir ? "active" : ""}" data-dir="${esc(d.name)}">
      <span>${esc(d.name)}</span><span class="cnt">${d.count}</span>
    </div>`).join("") || '<div class="empty">无目录</div>';
}

$("iconTree").addEventListener("click", (e) => {
  const item = e.target.closest("[data-dir]");
  if (!item || item.dataset.dir === iconSelDir) return;
  iconSelDir = item.dataset.dir;
  renderIconTree();
  renderIconGrid();
});

// 缩略图地址：token 随查询串携带（img 标签无法带请求头），v= mtime 绕缓存
function iconRawURL(f) {
  return `/api/admin/icons/raw?path=${encodeURIComponent(f.path)}` +
    (TOKEN ? `&token=${encodeURIComponent(TOKEN)}` : "") + `&v=${f.mod}`;
}

function renderIconGrid() {
  const dir = iconDirs.find((d) => d.name === iconSelDir);
  $("iconGridTitle").textContent = dir ? `当前目录：${dir.name}（${dir.count} 个图标）` : "";
  const grid = $("iconGrid");
  if (!dir || !(dir.files || []).length) {
    grid.innerHTML = `<div class="empty" style="grid-column:1/-1">${dir ? "该目录暂无图标，拖拽上传第一个" : "请先选择目录"}</div>`;
    return;
  }
  const kw = $("iconSearch").value.trim().toLowerCase();
  grid.innerHTML = (dir.files || [])
    .filter((f) => !kw || f.name.toLowerCase().includes(kw))
    .map((f) => {
      const binds = iconBinds.filter((b) => b.icon_path === f.path);
      return `
      <div class="icon-card ${f.path === iconFlashPath ? "flash" : ""}" data-path="${esc(f.path)}">
        <img loading="lazy" src="${iconRawURL(f)}" alt="">
        <div class="ic-name" title="${esc(f.name)}">${esc(f.name)}</div>
        ${binds.length ? `<div class="ic-binds">${binds.map((b) =>
          `<span class="ic-chip" data-unbind="${esc(b.channel_name)}" title="点击解除「${esc(b.channel_name)}」的绑定">${esc(b.channel_name)}</span>`).join("")}</div>` : ""}
        <div class="ic-ops">
          <button class="btn ghost mini" data-copy="${esc(f.path)}" title="复制对外图标直链（/icon/名字）">复制</button>
          <button class="btn ghost mini" data-replace="${esc(f.path)}" title="用新图片覆盖此图标">替换</button>
          <button class="btn ghost mini" data-bind="${esc(f.path)}">绑定</button>
          <button class="btn danger mini" data-del="${esc(f.path)}">删除</button>
        </div>
      </div>`;
    }).join("");
  iconFlashPath = "";
}

$("iconSearch").addEventListener("input", renderIconGrid);

// multipart 上传（不能走 api()：body 会被 JSON 化）
async function postForm(path, fd) {
  const headers = {};
  if (TOKEN) headers["Authorization"] = "Bearer " + TOKEN;
  const resp = await fetch(API + path, { method: "POST", headers, body: fd });
  const data = await resp.json().catch(() => ({}));
  if (!resp.ok) throw new Error(data.error || "HTTP " + resp.status);
  return data;
}

async function uploadOne(file, dir, name, oldPath) {
  const fd = new FormData();
  fd.append("dir", dir);
  fd.append("name", name);
  fd.append("file", file);
  if (oldPath) fd.append("old_path", oldPath);
  return postForm("/icons/upload", fd);
}

// 上传预检：浏览器实际解码一次，拦下 magic bytes 合法但内容损坏的图片
function checkImage(file) {
  return new Promise((resolve) => {
    const url = URL.createObjectURL(file);
    const img = new Image();
    img.onload = () => { URL.revokeObjectURL(url); resolve(true); };
    img.onerror = () => { URL.revokeObjectURL(url); resolve(false); };
    img.src = url;
  });
}

// 替换模式：锁定目标图标（目录 + 名字），拖入/选中的新图直接覆盖它
let iconReplaceTarget = null; // {dir, base, path}

function enterReplace(path) {
  const slash = path.lastIndexOf("/");
  iconReplaceTarget = {
    dir: path.slice(0, slash),
    base: path.slice(slash + 1).replace(/\.[^.]+$/, ""),
    path,
  };
  $("dzReplaceName").textContent = path;
  $("dzReplace").hidden = false;
  $("iconDrop").classList.add("replacing");
  if (iconSelDir !== iconReplaceTarget.dir) { // 切到目标目录，让被替换图标可见
    iconSelDir = iconReplaceTarget.dir;
    renderIconTree();
    renderIconGrid();
  }
}

function exitReplace() {
  iconReplaceTarget = null;
  $("dzReplace").hidden = true;
  $("iconDrop").classList.remove("replacing");
}

$("dzReplaceCancel").addEventListener("click", (e) => { e.stopPropagation(); exitReplace(); });

// 上传规则：替换模式强制覆盖目标；普通模式单文件用输入框名字（空则取文件名），
// 多文件忽略输入框、文件名即图标名
async function handleFiles(files) {
  if (!files.length) return;
  if (iconReplaceTarget && files.length > 1) {
    toast("替换模式一次只处理一张图，已退出（可直接批量上传）");
    exitReplace();
  }
  if (!iconSelDir && !iconReplaceTarget) { toast("请先在左侧选择目标目录"); return; }
  try {
    if (iconReplaceTarget) {
      const t = iconReplaceTarget;
      if (!await checkImage(files[0])) { toast("该文件不是有效的图片（无法解码），已跳过"); return; }
      toast("替换中…");
      const res = await uploadOne(files[0], t.dir, t.base, t.path);
      toast(res.message);
      iconFlashPath = res.path;
      exitReplace();
    } else if (files.length === 1) {
      if (!await checkImage(files[0])) { toast("该文件不是有效的图片（无法解码），已跳过"); return; }
      toast("上传中…");
      const name = $("iconName").value.trim() || files[0].name.replace(/\.[^.]+$/, "");
      const res = await uploadOne(files[0], iconSelDir, name);
      toast(res.message);
      iconFlashPath = res.path;
    } else {
      let ok = 0, fail = 0, bad = 0;
      for (let i = 0; i < files.length; i++) {
        const f = files[i];
        toast(`上传中 ${i + 1}/${files.length}：${f.name}`);
        if (!await checkImage(f)) { bad++; continue; }
        try { iconFlashPath = (await uploadOne(f, iconSelDir, f.name.replace(/\.[^.]+$/, ""))).path; ok++; }
        catch { fail++; }
      }
      toast(`批量上传完成：成功 ${ok}${fail ? `，失败 ${fail}` : ""}${bad ? `，损坏跳过 ${bad}` : ""}`);
    }
    await loadIcons();
  } catch (err) { toast(err.message); }
}

const iconDrop = $("iconDrop");
iconDrop.addEventListener("click", () => $("iconFile").click());
$("iconFile").addEventListener("change", (e) => {
  handleFiles([...e.target.files]);
  e.target.value = ""; // 允许重复选择同一文件
});
["dragenter", "dragover"].forEach((ev) =>
  iconDrop.addEventListener(ev, (e) => { e.preventDefault(); iconDrop.classList.add("over"); }));
["dragleave", "drop"].forEach((ev) =>
  iconDrop.addEventListener(ev, (e) => { e.preventDefault(); iconDrop.classList.remove("over"); }));
iconDrop.addEventListener("drop", (e) => handleFiles([...e.dataTransfer.files]));

// URL 导入：替换模式覆盖目标图标；普通模式名字空则取 URL 最后一段（去 query）
$("iconFetchBtn").addEventListener("click", async () => {
  const url = $("iconUrl").value.trim();
  if (!url) { toast("请输入图片地址"); return; }
  const t = iconReplaceTarget;
  if (!t && !iconSelDir) { toast("请先在左侧选择目标目录"); return; }
  const fallback = decodeURIComponent(url.split("/").pop().split("?")[0]);
  const body = t
    ? { dir: t.dir, name: t.base, url, old_path: t.path }
    : { dir: iconSelDir, name: $("iconName").value.trim() || fallback || "icon", url };
  try {
    const res = await api("/icons/fetch", { method: "POST", body });
    toast(res.message);
    iconFlashPath = res.path;
    $("iconUrl").value = "";
    if (t) exitReplace();
    await loadIcons();
  } catch (err) { toast(err.message); }
});

$("iconMkdirBtn").addEventListener("click", async () => {
  const name = prompt("新目录名（省份或分类）：");
  if (!name) return;
  try {
    toast((await api("/icons/mkdir", { method: "POST", body: { name } })).message);
    iconSelDir = name.trim();
    await loadIcons();
  } catch (err) { toast(err.message); }
});

let iconBindTarget = ""; // 绑定弹窗当前操作的图标路径

$("iconGrid").addEventListener("click", async (e) => {
  // 复制对外直链 /icon/{文件名去后缀}（不带 token，可安全外发）
  const copy = e.target.closest("[data-copy]");
  if (copy) {
    const name = copy.dataset.copy.split("/").pop().replace(/\.[^.]+$/, "");
    const url = `${location.origin}/icon/${encodeURIComponent(name)}`;
    try {
      await navigator.clipboard.writeText(url);
      toast("已复制：" + url);
    } catch {
      prompt("浏览器拒绝了剪贴板权限，请手动复制：", url);
    }
    return;
  }
  const chip = e.target.closest("[data-unbind]");
  if (chip) {
    const ch = chip.dataset.unbind;
    if (!confirm(`解除频道「${ch}」的图标绑定？解除后恢复自动匹配。`)) return;
    try {
      toast((await api("/icons/binds/" + encodeURIComponent(ch), { method: "DELETE" })).message);
      await loadIcons();
    } catch (err) { toast(err.message); }
    return;
  }
  const del = e.target.closest("[data-del]");
  if (del) {
    if (!confirm("确认删除该图标文件？已绑定该图标的频道将一并解绑。")) return;
    try {
      toast((await api("/icons/file?path=" + encodeURIComponent(del.dataset.del), { method: "DELETE" })).message);
      await loadIcons();
    } catch (err) { toast(err.message); }
    return;
  }
  const replace = e.target.closest("[data-replace]");
  if (replace) {
    enterReplace(replace.dataset.replace);
    return;
  }
  const bind = e.target.closest("[data-bind]");
  if (bind) {
    iconBindTarget = bind.dataset.bind;
    $("iconBindInfo").textContent = "图标：" + iconBindTarget;
    $("iconBindChannel").value = "";
    $("iconBindModal").hidden = false;
    $("iconBindChannel").focus();
  }
});

$("iconBindSaveBtn").addEventListener("click", async () => {
  const ch = $("iconBindChannel").value.trim();
  if (!ch) { toast("请输入频道名"); return; }
  try {
    toast((await api("/icons/binds", { method: "PUT", body: { channel_name: ch, icon_path: iconBindTarget } })).message);
    $("iconBindModal").hidden = true;
    await loadIcons();
  } catch (err) { toast(err.message); }
});

/* ---------- 启动 ---------- */
loadSources();
setInterval(() => { if (!$("tab-sources").hidden) loadSources(); }, 10000);
