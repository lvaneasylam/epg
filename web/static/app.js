/* EPG 节目单时间轴前端逻辑（无依赖原生实现） */
"use strict";

let HOUR_W = 120;            // 与 CSS --hour-w 同步（layoutHours 动态计算）
const CH_W = 190;            // 与 CSS --ch-w 保持一致
const VIEW_HOURS = 5;        // 可视时间轴宽度：约 5 小时，当前时间居中
const DAY_MS = 24 * 3600 * 1000;

const $ = (id) => document.getElementById(id);
const viewport = $("viewport");

const state = {
  date: null,        // YYYY-MM-DD
  dayStart: null,    // 当天 00:00 的时间戳（后端时区，来自 day_start 字段）
  channels: [],
};

/* ---------- 工具 ---------- */

function localTodayStr() {
  const d = new Date();
  const p = (n) => String(n).padStart(2, "0");
  return `${d.getFullYear()}-${p(d.getMonth() + 1)}-${p(d.getDate())}`;
}

function fmtHM(ts) {
  const d = new Date(ts);
  const p = (n) => String(n).padStart(2, "0");
  return `${p(d.getHours())}:${p(d.getMinutes())}`;
}

function esc(s) {
  return String(s ?? "").replace(/[&<>"]/g, (c) => ({
    "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;",
  }[c]));
}

function toast(msg, ms = 2400) {
  const el = $("toast");
  el.textContent = msg;
  el.hidden = false;
  clearTimeout(el._t);
  el._t = setTimeout(() => (el.hidden = true), ms);
}

async function api(path, opts) {
  const resp = await fetch(path, opts);
  if (!resp.ok) {
    let msg = `HTTP ${resp.status}`;
    try { msg = (await resp.json()).error || msg; } catch (_) {}
    throw new Error(msg);
  }
  return resp.json();
}

/* ---------- 渲染 ---------- */

function renderTicks() {
  const html = [];
  for (let h = 0; h < 24; h++) {
    html.push(`<div class="tick">${String(h).padStart(2, "0")}:00</div>`);
  }
  $("ticks").innerHTML = html.join("");
}

function renderRows() {
  const rows = [];
  for (const ch of state.channels) {
    if (!ch.programmes.length) continue; // 当日无节目的频道不渲染
    const cards = ch.programmes.map((p) => {
      const s = new Date(p.start).getTime();
      const e = new Date(p.stop).getTime();
      const left = ((s - state.dayStart) / 3600000) * HOUR_W;
      let width = ((e - s) / 3600000) * HOUR_W - 4;
      if (width < 8) width = 8; // 极短节目保底可点击
      return `<div class="prog" style="left:${left.toFixed(1)}px;width:${width.toFixed(1)}px"
        data-name="${esc(ch.name)}" data-start="${s}" data-stop="${e}">
        <div class="t">${esc(p.title)}</div>
        <div class="h">${fmtHM(s)}</div>
      </div>`;
    }).join("");
    const logo = ch.icon
      ? `<img class="logo" src="${esc(ch.icon)}" loading="lazy"
          onerror="this.style.display='none'">` : "";
    rows.push(`<div class="row">
      <div class="ch">${logo}<span class="name" title="${esc(ch.name)}">${esc(ch.name)}</span></div>
      <div class="lane">${cards}</div>
    </div>`);
  }
  $("rows").innerHTML = rows.join("");
}

function isToday() {
  const now = Date.now();
  return state.dayStart && now >= state.dayStart && now < state.dayStart + DAY_MS;
}

function updateNow() {
  const line = $("nowLine");
  if (!isToday()) {
    line.style.display = "none";
    markLive(null);
    return;
  }
  const x = ((Date.now() - state.dayStart) / 3600000) * HOUR_W;
  if (x < 0 || x >= 24 * HOUR_W) { line.style.display = "none"; return; }
  line.style.display = "block";
  line.style.left = `calc(var(--ch-w) + ${x.toFixed(1)}px)`;
  markLive(Date.now());
}

/* 播出中卡片高亮 */
function markLive(now) {
  document.querySelectorAll(".prog").forEach((el) => {
    const s = +el.dataset.start, e = +el.dataset.stop;
    el.classList.toggle("live", now !== null && now >= s && now < e);
  });
}

function scrollToNow() {
  if (!isToday()) return;
  const x = (Date.now() - state.dayStart) / 3600000 * HOUR_W;
  const laneW = viewport.clientWidth - CH_W; // 可见时间轴区域宽（频道列 sticky 占 CH_W）
  viewport.scrollLeft = Math.max(0, Math.round(x - laneW / 2)); // 当前时间居中
}

/* 按视口宽度动态设定每小时格宽：可视区约 VIEW_HOURS 小时 */
function layoutHours() {
  const laneW = viewport.clientWidth - CH_W;
  HOUR_W = Math.max(120, Math.floor(laneW / VIEW_HOURS));
  document.documentElement.style.setProperty("--hour-w", HOUR_W + "px");
}

/* ---------- 显示开关（body class 控制，状态存 localStorage） ---------- */

function bindToggles() {
  const tpt = $("toggleProgText"), tic = $("toggleIcons");
  tpt.checked = localStorage.getItem("epgShowProgText") !== "0";
  tic.checked = localStorage.getItem("epgShowIcons") !== "0";
  const apply = () => {
    document.body.classList.toggle("hide-prog-text", !tpt.checked);
    document.body.classList.toggle("hide-icons", !tic.checked);
    localStorage.setItem("epgShowProgText", tpt.checked ? "1" : "0");
    localStorage.setItem("epgShowIcons", tic.checked ? "1" : "0");
  };
  tpt.addEventListener("change", apply);
  tic.addEventListener("change", apply);
  apply();
}

/* ---------- 频道搜索定位 ---------- */

function highlightRow(row) {
  document.querySelectorAll(".row.hl").forEach((r) => r.classList.remove("hl"));
  if (!row) return;
  row.classList.add("hl");
  row.scrollIntoView({ block: "center", behavior: "smooth" });
  clearTimeout(highlightRow._t);
  highlightRow._t = setTimeout(() => row.classList.remove("hl"), 2000);
}

function bindSearch() {
  const input = $("chSearch");
  input.addEventListener("input", () => {
    const q = input.value.trim().toLowerCase();
    if (!q) { highlightRow(null); return; }
    const row = [...document.querySelectorAll("#rows .row")]
      .find((r) => r.querySelector(".name").textContent.toLowerCase().includes(q));
    highlightRow(row || null);
  });
  input.addEventListener("keydown", (e) => {
    if (e.key === "Escape") { input.value = ""; highlightRow(null); }
  });
}

/* ---------- 数据加载 ---------- */

async function loadTimeline(date) {
  state.date = date;
  $("dateInput").value = date;
  $("rows").innerHTML = "";
  $("emptyState").hidden = true;
  try {
    const data = await api(`/api/timeline?date=${encodeURIComponent(date)}`);
    state.dayStart = new Date(data.day_start).getTime();
    state.channels = (data.channels || []).map((c) => ({
      ...c,
      programmes: c.programmes || [], // 后端 nil slice 序列化为 null，此处容错
    }));
    const withProg = state.channels.filter((c) => c.programmes.length > 0);
    if (state.channels.length === 0) {
      $("emptyHint").textContent = "请确认 EPG 源已配置并完成一次同步";
      $("emptyState").hidden = false;
    } else if (withProg.length === 0) {
      $("emptyHint").textContent = `${date} 无节目数据（可能超出保留天数范围）`;
      $("emptyState").hidden = false;
    }
    renderRows();
    updateNow();
    scrollToNow();
  } catch (err) {
    toast(`加载失败：${err.message}`);
  }
}

async function loadStats() {
  try {
    const st = await api("/api/status");
    const parts = [];
    if (st.stats) {
      parts.push(`频道 ${st.stats.channel_count}`, `节目 ${st.stats.program_count}`);
      if (st.stats.latest) parts.push(`最新数据至 ${new Date(st.stats.latest).toLocaleString()}`);
    }
    if (st.sync_running) parts.push("同步中…");
    $("stats").textContent = parts.join(" · ") || "暂无数据";
    return st;
  } catch (_) { /* 状态加载失败不打扰主界面 */ }
}

/* ---------- 详情浮层 ---------- */

async function openPopup(card) {
  const name = card.dataset.name;
  const startMs = +card.dataset.start;
  const pop = $("popup");
  $("popTitle").textContent = card.querySelector(".t").textContent;
  $("popTime").textContent = "";
  $("popCat").hidden = true;
  $("popDesc").textContent = "加载中…";
  positionPopup(card);
  pop.hidden = false;

  try {
    const data = await api(`/api/epg?ch=${encodeURIComponent(name)}&date=${state.date}`);
    const s0 = new Date(startMs);
    const p = data.programmes.find((x) => new Date(x.start).getTime() === s0)
      || data.programmes.find((x) => Math.abs(new Date(x.start).getTime() - startMs) < 60000);
    $("popTime").textContent = `${new Date(p.start).toLocaleString()} ~ ${new Date(p.stop).toLocaleTimeString()}`;
    $("popCat").hidden = !p.category;
    $("popCat").textContent = p.category || "";
    $("popDesc").textContent = p.desc || p.sub_title || "暂无节目简介";
  } catch (err) {
    $("popDesc").textContent = `详情获取失败：${err.message}`;
  }
}

function positionPopup(card) {
  const pop = $("popup");
  const rect = card.getBoundingClientRect();
  pop.hidden = true;       // 先隐藏以获取自然宽度
  pop.style.left = "0px";
  pop.style.top = "0px";
  const pw = pop.offsetWidth || 300, ph = pop.offsetHeight || 160;
  let x = rect.left + rect.width / 2 - pw / 2;
  let y = rect.bottom + 8;
  if (y + ph > innerHeight - 10) y = rect.top - ph - 8;
  if (x < 10) x = 10;
  if (x + pw > innerWidth - 10) x = innerWidth - pw - 10;
  pop.style.left = `${x}px`;
  pop.style.top = `${Math.max(10, y)}px`;
}

function closePopup() { $("popup").hidden = true; }

/* ---------- 事件绑定 ---------- */

function shiftDate(days) {
  const d = new Date(`${state.date}T00:00:00`);
  d.setDate(d.getDate() + days);
  const p = (n) => String(n).padStart(2, "0");
  loadTimeline(`${d.getFullYear()}-${p(d.getMonth() + 1)}-${p(d.getDate())}`);
}

function bind() {
  $("prevDay").addEventListener("click", () => shiftDate(-1));
  $("nextDay").addEventListener("click", () => shiftDate(1));
  $("todayBtn").addEventListener("click", () => loadTimeline(localTodayStr()));
  $("dateInput").addEventListener("change", (e) => {
    if (e.target.value) loadTimeline(e.target.value);
  });

  $("rows").addEventListener("click", (e) => {
    const card = e.target.closest(".prog");
    if (card) openPopup(card);
  });
  $("popClose").addEventListener("click", closePopup);
  document.addEventListener("click", (e) => {
    if (!$("popup").hidden && !e.target.closest("#popup") && !e.target.closest(".prog")) {
      closePopup();
    }
  });
  document.addEventListener("keydown", (e) => {
    if (e.key === "Escape") closePopup();
    if (e.target.tagName === "INPUT") return;
    if (e.key === "ArrowLeft") shiftDate(-1);
    if (e.key === "ArrowRight") shiftDate(1);
  });
}

/* ---------- 启动 ---------- */

layoutHours();
renderTicks();
bind();
bindToggles();
bindSearch();
setInterval(updateNow, 30000);   // 当前时间线与播出高亮每 30s 刷新
// 视口尺寸变化：重算小时格宽并重排卡片（inline 定位依赖 HOUR_W）
window.addEventListener("resize", () => {
  layoutHours();
  if (state.channels.length) { renderRows(); updateNow(); scrollToNow(); }
});
loadStats();
setInterval(loadStats, 30000);
loadTimeline(localTodayStr());
