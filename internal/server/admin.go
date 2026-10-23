package server

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"goepg/internal/epgtime"
	"goepg/internal/store"
	"goepg/internal/syncer"
	"goepg/web"
)

// timelineChannel GET /api/timeline 单频道结构（页面网格渲染用，精简字段省流量）。
type timelineChannel struct {
	Name       string              `json:"name"`
	Icon       string              `json:"icon,omitempty"`
	Programmes []timelineProgramme `json:"programmes"`
}

type timelineProgramme struct {
	Start time.Time `json:"start"`
	Stop  time.Time `json:"stop"`
	Title string    `json:"title"`
}

// handleTimeline GET /api/timeline?date=YYYY-MM-DD
// 页面数据源：全部去重频道 + 当日节目。
func (s *Server) handleTimeline(w http.ResponseWriter, r *http.Request) {
	dateStr := strings.TrimSpace(r.URL.Query().Get("date"))
	if dateStr == "" {
		dateStr = epgtime.FormatDate(time.Now().In(s.tz()))
	}
	day, ok := epgtime.ParseDate(dateStr, s.tz())
	if !ok {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "date 参数格式应为 YYYY-MM-DD"})
		return
	}

	cacheKey := fmt.Sprintf("v%s:epg:timeline:%s", s.dataVersion(), dateStr)
	if val, hit := s.cache.Get(r.Context(), cacheKey); hit {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.Header().Set("X-Cache", "hit")
		_, _ = w.Write(val)
		return
	}

	all, err := s.store.AllChannels(r.Context())
	if err != nil {
		http.Error(w, "数据库查询失败", http.StatusInternalServerError)
		return
	}
	bindings, err := s.store.BindingChannelMap(r.Context())
	if err != nil {
		http.Error(w, "数据库查询失败", http.StatusInternalServerError)
		return
	}
	hidden, err := s.store.HiddenSet(r.Context())
	if err != nil {
		http.Error(w, "数据库查询失败", http.StatusInternalServerError)
		return
	}
	channels := store.FilterHidden(store.ApplyBindings(all, bindings), hidden)
	ids := make([]int64, 0, len(channels))
	byID := make(map[int64]int, len(channels)) // channel_id → out 下标
	for i, c := range channels {
		ids = append(ids, c.ID)
		byID[c.ID] = i
	}

	progs, err := s.store.ProgrammesBetween(r.Context(), ids, day, day.Add(24*time.Hour), false)
	if err != nil {
		http.Error(w, "数据库查询失败", http.StatusInternalServerError)
		return
	}
	out := make([]timelineChannel, len(channels))
	for i, c := range channels {
		// 图标统一走本服务 /icon 地址（与 /api/epg 一致，iconlib 模糊匹配，未命中 404 由前端隐藏）
		out[i] = timelineChannel{Name: c.Name, Icon: s.iconURL(r, c.Name), Programmes: []timelineProgramme{}}
	}
	for _, p := range progs {
		if i, ok := byID[p.ChannelID]; ok {
			out[i].Programmes = append(out[i].Programmes, timelineProgramme{
				Start: p.Start, Stop: p.Stop, Title: p.Title,
			})
		}
	}
	// 当日无节目的频道整行不下发（前端时间轴不渲染空行，同时减小响应体积）
	kept := make([]timelineChannel, 0, len(out))
	for _, c := range out {
		if len(c.Programmes) > 0 {
			kept = append(kept, c)
		}
	}
	out = kept

	resp := map[string]any{
		"date":         dateStr,
		"day_start":    day.Format(time.RFC3339), // 前端网格定位基准，避免浏览器时区推算
		"generated_at": time.Now().In(s.tz()).Format(time.RFC3339),
		"count":        len(out),
		"channels":     out,
	}
	var buf bytes.Buffer
	_ = json.NewEncoder(&buf).Encode(resp)
	body := buf.Bytes()

	s.cache.Set(r.Context(), cacheKey, body, s.cfg.Redis.CacheTTL)
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("X-Cache", "miss")
	_, _ = w.Write(body)
}

// statusResponse GET /api/status 响应。
type statusResponse struct {
	Stats        *store.Stats          `json:"stats"`
	KeepDays     int                   `json:"keep_days"`
	SyncRunning  bool                  `json:"sync_running"`
	SyncInterval string                `json:"sync_interval"`
	Sources      []syncer.SourceStatus `json:"sources"`
}

// handleStatus GET /api/status 运行状态与库存统计（页面顶栏与诊断用）。
func (s *Server) handleStatus(w http.ResponseWriter, r *http.Request) {
	stats, err := s.store.Stats(r.Context())
	if err != nil {
		http.Error(w, "数据库查询失败", http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, statusResponse{
		Stats:        stats,
		KeepDays:     s.cfg.EPG.KeepDays,
		SyncRunning:  s.syncer.IsRunning(),
		SyncInterval: s.cfg.EPG.SyncInterval.String(),
		Sources:      s.syncer.Statuses(),
	})
}

// handleSyncNow POST /api/sync 手动触发一次全量同步。
func (s *Server) handleSyncNow(w http.ResponseWriter, r *http.Request) {
	if !s.syncer.TriggerSync() {
		writeJSON(w, http.StatusConflict, map[string]string{"message": "同步任务正在进行中，请稍后"})
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]string{"message": "已触发同步，可通过 /api/status 观察进度"})
}

// ---------- 管理端 API ----------

// sourceStat 源覆盖统计（复用 store.SourceStat）。

// sourceView 管理页源视图：DB 定义 + 运行状态 + 覆盖统计。
type sourceView struct {
	store.SourceRow
	State       string           `json:"state"`
	Message     string           `json:"message,omitempty"`
	LastStart   *time.Time       `json:"last_start,omitempty"`
	LastDone    *time.Time       `json:"last_done,omitempty"`
	NextSyncAt  *time.Time       `json:"next_sync_at,omitempty"`
	DurationSec float64          `json:"duration_sec,omitempty"`
	Stat        store.SourceStat `json:"stat"`
}

// adminListSources GET /api/admin/sources
func (s *Server) adminListSources(w http.ResponseWriter, r *http.Request) {
	rows, err := s.store.ListSources(r.Context())
	if err != nil {
		http.Error(w, "数据库查询失败", http.StatusInternalServerError)
		return
	}
	stats, err := s.store.SourceStats(r.Context())
	if err != nil {
		http.Error(w, "数据库查询失败", http.StatusInternalServerError)
		return
	}
	statuses := s.syncer.Statuses()
	stMap := make(map[string]syncer.SourceStatus, len(statuses))
	for _, st := range statuses {
		stMap[st.Name] = st
	}
	out := make([]sourceView, 0, len(rows))
	for _, row := range rows {
		v := sourceView{SourceRow: row}
		if st, ok := stMap[row.Name]; ok {
			v.State = st.State
			v.Message = st.Message
			v.LastStart = st.LastStart
			v.LastDone = st.LastDone
			v.NextSyncAt = st.NextSyncAt
			v.DurationSec = st.DurationSec
		} else {
			v.State = syncer.StateIdle
		}
		v.Stat = stats[row.Name]
		out = append(out, v)
	}
	writeJSON(w, http.StatusOK, out)
}

// sourcePayload 源新建/编辑请求体。
type sourcePayload struct {
	Name        string `json:"name"`
	URL         string `json:"url"`
	Priority    int    `json:"priority"`
	IntervalMin int    `json:"interval_min"`
	Enabled     bool   `json:"enabled"`
}

func (p *sourcePayload) validate() string {
	if p.Name == "" {
		return "名称不能为空"
	}
	if p.URL == "" {
		return "地址不能为空"
	}
	return ""
}

// adminCreateSource POST /api/admin/sources
func (s *Server) adminCreateSource(w http.ResponseWriter, r *http.Request) {
	var p sourcePayload
	if err := json.NewDecoder(r.Body).Decode(&p); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "请求体格式错误"})
		return
	}
	if msg := p.validate(); msg != "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": msg})
		return
	}
	row := store.SourceRow{Name: p.Name, URL: p.URL, Priority: p.Priority,
		IntervalMin: p.IntervalMin, Enabled: p.Enabled}
	if err := s.store.CreateSource(r.Context(), &row); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, row)
}

// adminUpdateSource PUT /api/admin/sources/{id}
func (s *Server) adminUpdateSource(w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
	var p sourcePayload
	if err := json.NewDecoder(r.Body).Decode(&p); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "请求体格式错误"})
		return
	}
	if msg := p.validate(); msg != "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": msg})
		return
	}
	row := store.SourceRow{Name: p.Name, URL: p.URL, Priority: p.Priority,
		IntervalMin: p.IntervalMin, Enabled: p.Enabled}
	if err := s.store.UpdateSource(r.Context(), id, &row); err != nil {
		if err == store.ErrNotFound {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "源不存在"})
			return
		}
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, row)
}

// adminDeleteSource DELETE /api/admin/sources/{id}（源数据一并清理）
func (s *Server) adminDeleteSource(w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err := s.store.DeleteSource(r.Context(), id); err != nil {
		if err == store.ErrNotFound {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "源不存在"})
			return
		}
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"message": "已删除"})
}

// adminToggleSource POST /api/admin/sources/{id}/toggle  body: {"enabled":bool}
func (s *Server) adminToggleSource(w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
	var body struct {
		Enabled bool `json:"enabled"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "请求体格式错误"})
		return
	}
	if err := s.store.ToggleSource(r.Context(), id, body.Enabled); err != nil {
		if err == store.ErrNotFound {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "源不存在"})
			return
		}
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"message": "ok"})
}

// adminSyncSource POST /api/admin/sources/{id}/sync 单源手动刷新
func (s *Server) adminSyncSource(w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
	row, err := s.store.GetSource(r.Context(), id)
	if err != nil {
		if err == store.ErrNotFound {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "源不存在"})
			return
		}
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	if st := s.syncer.StatusOf(row.Name); st != nil && st.State == syncer.StateRunning {
		writeJSON(w, http.StatusConflict, map[string]string{"message": "该源正在同步中"})
		return
	}
	_ = s.syncer.TriggerSource(row.Name)
	writeJSON(w, http.StatusAccepted, map[string]string{"message": "已触发刷新: " + row.Name})
}

// ---------- 绑定 API ----------

// bindingView 绑定列表视图：对外频道 + 生效来源 + 候选数 + 首页显示状态。
type bindingView struct {
	ChannelName      string     `json:"channel_name"`      // 对外频道名
	Mode             string     `json:"mode"`              // auto / manual
	TargetName       string     `json:"target_name"`       // 生效频道显示名
	TargetSource     string     `json:"target_source"`     // 生效来源
	Candidates       int        `json:"candidates"`        // 跨源同名频道数（备用源数）
	ChannelUpdatedAt *time.Time `json:"channel_updated_at"` // 生效频道行的更新时间（所属源最近同步）
	Hidden           bool       `json:"hidden"`             // 是否隐藏（不在首页时间轴显示）
	UpdatedAt        *time.Time `json:"updated_at,omitempty"`
}

// adminListBindings GET /api/admin/bindings
// 返回全部对外频道（去重后）的绑定视图：手动绑定标记 manual，其余 auto。
func (s *Server) adminListBindings(w http.ResponseWriter, r *http.Request) {
	all, err := s.store.AllChannels(r.Context())
	if err != nil {
		http.Error(w, "数据库查询失败", http.StatusInternalServerError)
		return
	}
	bindings, err := s.store.ListBindings(r.Context())
	if err != nil {
		http.Error(w, "数据库查询失败", http.StatusInternalServerError)
		return
	}
	hidden, err := s.store.HiddenSet(r.Context())
	if err != nil {
		http.Error(w, "数据库查询失败", http.StatusInternalServerError)
		return
	}
	byName := map[string][]store.Channel{}
	for _, c := range all {
		byName[c.Name] = append(byName[c.Name], c)
	}
	manual := map[string]store.BindingRow{}
	for _, b := range bindings {
		manual[b.ChannelName] = b
	}

	channels := store.ApplyBindings(all, nil2Map(bindings))
	out := make([]bindingView, 0, len(channels))
	for _, c := range channels {
		t := c.UpdatedAt
		v := bindingView{
			ChannelName:      c.Name,
			Mode:             "auto",
			TargetName:       c.Name,
			TargetSource:     c.Source,
			Candidates:       len(byName[c.Name]) - 1,
			ChannelUpdatedAt: &t,
			Hidden:           hidden[c.Name],
		}
		if b, ok := manual[c.Name]; ok {
			v.Mode = "manual"
			if b.TargetName != "" {
				v.TargetName = b.TargetName
				v.TargetSource = b.TargetSource
			}
			t := b.UpdatedAt
			v.UpdatedAt = &t
		}
		out = append(out, v)
	}
	writeJSON(w, http.StatusOK, out)
}

func nil2Map(bindings []store.BindingRow) store.BindingMap {
	m := store.BindingMap{}
	for _, b := range bindings {
		m[b.ChannelName] = b.TargetChannel
	}
	return m
}

// adminBindingCandidates GET /api/admin/bindings/candidates?name=xx[&date=YYYY-MM-DD]
// 返回该名字（精确名下的全部跨源频道，供手动绑定选择）；
// 带 date 时附带各候选当日节目数，帮助选择"当天有数据"的源。
func (s *Server) adminBindingCandidates(w http.ResponseWriter, r *http.Request) {
	name := strings.TrimSpace(r.URL.Query().Get("name"))
	if name == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "缺少 name 参数"})
		return
	}
	all, err := s.store.AllChannels(r.Context())
	if err != nil {
		http.Error(w, "数据库查询失败", http.StatusInternalServerError)
		return
	}
	type candProg struct {
		Start string `json:"start"` // HH:mm
		Title string `json:"title"`
	}
	type cand struct {
		ID            int64      `json:"id"`
		Name          string     `json:"name"`
		Source        string     `json:"source"`
		Priority      int        `json:"priority"`
		TodayPrograms int        `json:"today_programs"`
		Programs      []candProg `json:"programs"` // 当日全部节目，供绑定弹窗浮动对比
	}
	var ids []int64
	var out []cand
	for _, c := range all {
		if c.Name == name || c.XmltvID == name {
			ids = append(ids, c.ID)
			out = append(out, cand{ID: c.ID, Name: c.Name, Source: c.Source, Priority: c.Priority})
		}
	}
	if len(ids) > 0 {
		day := time.Now().In(s.tz())
		if d := r.URL.Query().Get("date"); d != "" {
			if t, ok := epgtime.ParseDate(d, s.tz()); ok {
				day = t
			}
		}
		counts, err := s.store.ProgramCountByChannel(r.Context(), ids, day, day.Add(24*time.Hour))
		if err == nil {
			for i := range out {
				out[i].TodayPrograms = counts[out[i].ID]
			}
		}
		if progs, err := s.store.ProgrammesBetween(r.Context(), ids, day, day.Add(24*time.Hour), false); err == nil {
			sort.Slice(progs, func(i, j int) bool { // 按开始时间排序后每源取前几条
				if progs[i].Start.Equal(progs[j].Start) {
					return progs[i].ChannelID < progs[j].ChannelID
				}
				return progs[i].Start.Before(progs[j].Start)
			})
			byID := map[int64][]candProg{}
			for _, p := range progs {
				if len(byID[p.ChannelID]) < 500 { // 防异常源超大节目单撑爆响应
					byID[p.ChannelID] = append(byID[p.ChannelID], candProg{
						Start: p.Start.In(s.tz()).Format("15:04"), Title: p.Title,
					})
				}
			}
			for i := range out {
				out[i].Programs = byID[out[i].ID]
			}
		}
	}
	writeJSON(w, http.StatusOK, out)
}

// adminSetBinding PUT /api/admin/bindings  body: {"channel_name","target_channel_id"}
func (s *Server) adminSetBinding(w http.ResponseWriter, r *http.Request) {
	var body struct {
		ChannelName     string `json:"channel_name"`
		TargetChannelID int64  `json:"target_channel_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil ||
		body.ChannelName == "" || body.TargetChannelID <= 0 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "参数不完整"})
		return
	}
	if err := s.store.SetBinding(r.Context(), body.ChannelName, body.TargetChannelID); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	s.bindVer.Add(1) // 绑定变更，缓存立即失效
	writeJSON(w, http.StatusOK, map[string]string{"message": "已绑定"})
}

// adminDeleteBinding DELETE /api/admin/bindings/{name}
func (s *Server) adminDeleteBinding(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if err := s.store.DeleteBinding(r.Context(), name); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	s.bindVer.Add(1) // 绑定变更，缓存立即失效
	writeJSON(w, http.StatusOK, map[string]string{"message": "已解除绑定"})
}

// adminSetHidden PUT /api/admin/hidden  body: {"channel_name","hidden"}
// 设置/取消频道隐藏（仅影响首页时间轴，对外 XML / DIYP 不受影响）。
func (s *Server) adminSetHidden(w http.ResponseWriter, r *http.Request) {
	var body struct {
		ChannelName string `json:"channel_name"`
		Hidden      bool   `json:"hidden"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.ChannelName == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "参数不完整"})
		return
	}
	if err := s.store.SetHidden(r.Context(), body.ChannelName, body.Hidden); err != nil {
		http.Error(w, "数据库操作失败", http.StatusInternalServerError)
		return
	}
	s.bindVer.Add(1) // 隐藏变更，时间轴缓存立即失效
	msg := "已隐藏，首页不再显示"
	if !body.Hidden {
		msg = "已恢复首页显示"
	}
	writeJSON(w, http.StatusOK, map[string]string{"message": msg})
}

// adminRefreshIcons POST /api/admin/icons/refresh 手动重建图标索引（拷贝文件后立即可用）
func (s *Server) adminRefreshIcons(w http.ResponseWriter, r *http.Request) {
	s.icons.Refresh()
	s.publishIconIndex() // 索引重建后同步刷新 Redis 共享副本
	writeJSON(w, http.StatusOK, map[string]string{"message": "图标索引已刷新"})
}

// handleAdminPage GET /admin 管理页面。
func (s *Server) handleAdminPage(w http.ResponseWriter, r *http.Request) {
	data, err := web.AdminHTML()
	if err != nil {
		http.Error(w, "页面资源缺失", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	_, _ = w.Write(data)
}
