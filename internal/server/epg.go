package server

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"io"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"

	"goepg/internal/epgtime"
	"goepg/internal/store"
)

// epgResponse GET /api/epg 的响应结构。
// 主体字段（channel_name/date/url/icon/epg_data）兼容 DIYP/百川 播放器接口；
// programmes 为附加详情（站内页面弹层使用），多余字段不影响消费端解析。
type epgResponse struct {
	ChannelName string         `json:"channel_name"` // 回显请求的频道名
	Date        string         `json:"date"`
	URL         string         `json:"url"`
	Icon        string         `json:"icon"`     // 本服务 /icon 地址（自有域名）
	EpgData     any            `json:"epg_data"` // 有数据为数组，无数据为空串（DIYP 兼容）
	Programmes  []epgProgramme `json:"programmes"`
}

type epgProgramme struct {
	Start    time.Time `json:"start"`
	Stop     time.Time `json:"stop"`
	Title    string    `json:"title"`
	SubTitle string    `json:"sub_title,omitempty"`
	Desc     string    `json:"desc,omitempty"`
	Category string    `json:"category,omitempty"`
}

// diypProgramme DIYP/百川 格式的节目条目。
type diypProgramme struct {
	Start string `json:"start"` // HH:MM
	End   string `json:"end"`
	Title string `json:"title"`
	Desc  string `json:"desc,omitempty"`
}

// diypHHMM DIYP 时间显示：跨天节目对齐到当天边界——前一天开始的显示 00:00，
// 延续到次日之后的显示 23:59，保证节目单首尾覆盖整天且时间均为当天。
func diypHHMM(t, dayStart, dayEnd time.Time, tz *time.Location) string {
	if t.Before(dayStart) {
		t = dayStart
	} else if t.After(dayEnd.Add(-time.Minute)) {
		t = dayEnd.Add(-time.Minute)
	}
	return t.In(tz).Format("15:04")
}

// handleEPG 接口②：GET /api/epg?ch={频道名}&date=YYYY-MM-DD
// date 缺省为今天；频道查找顺序：手动绑定 → 名字精确（不区分大小写）→ xmltv_id 匹配。
func (s *Server) handleEPG(w http.ResponseWriter, r *http.Request) {
	ch := strings.TrimSpace(r.URL.Query().Get("ch"))
	if ch == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "缺少 ch 参数"})
		return
	}
	dateStr := strings.TrimSpace(r.URL.Query().Get("date"))
	if dateStr == "" {
		dateStr = epgtime.FormatDate(time.Now().In(s.tz()))
	}
	day, ok := epgtime.ParseDate(dateStr, s.tz())
	if !ok {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "date 参数格式应为 YYYY-MM-DD"})
		return
	}

	// 命中缓存直接返回（key 含数据版本：同步完成/绑定变更自动失效）
	cacheKey := fmt.Sprintf("v%s:epg:ch:%s:%s", s.dataVersion(), strings.ToLower(ch), dateStr)
	if val, hit := s.cache.Get(r.Context(), cacheKey); hit {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.Header().Set("X-Cache", "hit")
		_, _ = w.Write(val)
		return
	}

	channel, err := s.store.FindChannel(r.Context(), ch)
	if err != nil {
		http.Error(w, "数据库查询失败", http.StatusInternalServerError)
		return
	}
	if channel == nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "频道不存在: " + ch})
		return
	}
	progs, err := s.store.ProgrammesBetween(r.Context(),
		[]int64{channel.ID}, day, day.Add(24*time.Hour), true)
	if err != nil {
		http.Error(w, "数据库查询失败", http.StatusInternalServerError)
		return
	}

	resp := epgResponse{
		ChannelName: ch, // 回显请求名（绑定场景下与库内显示名可能不同）
		Date:        dateStr,
		URL:         "goepg",
		Icon:        s.iconURL(r, ch),
		EpgData:     "", // 无数据时为空串（DIYP 兼容）
		Programmes:  make([]epgProgramme, 0, len(progs)),
	}
	if len(progs) > 0 {
		dayEnd := day.Add(24 * time.Hour)
		diyp := make([]diypProgramme, 0, len(progs))
		for _, p := range progs {
			d := diypProgramme{
				Start: diypHHMM(p.Start, day, dayEnd, s.tz()),
				End:   diypHHMM(p.Stop, day, dayEnd, s.tz()),
				Title: p.Title,
				Desc:  p.Descr,
			}
			diyp = append(diyp, d)
			resp.Programmes = append(resp.Programmes, epgProgramme{
				Start: p.Start, Stop: p.Stop, Title: p.Title,
				SubTitle: p.SubTitle, Desc: p.Descr, Category: p.Category,
			})
		}
		resp.EpgData = diyp
	}

	// 序列化一次，同时用于缓存与响应
	var buf bytes.Buffer
	_ = json.NewEncoder(&buf).Encode(resp)
	body := buf.Bytes()

	s.cache.Set(r.Context(), cacheKey, body, s.cfg.Redis.CacheTTL)
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("X-Cache", "miss")
	_, _ = w.Write(body)
}

// xmltvTime XMLTV 标准时间格式输出，如 20261023080000 +0800。
func xmltvTime(t time.Time) string {
	return t.Format("20060102150405 -0700")
}

// handleXML 接口①：GET /api/xml[?days=N]
// 汇总全部频道（跨源同名去重）重新封装为 XMLTV 输出，支持 ETag/304 与 gzip。
func (s *Server) handleXML(w http.ResponseWriter, r *http.Request) {
	days := s.cfg.EPG.KeepDays
	if v := r.URL.Query().Get("days"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 && n <= s.cfg.EPG.KeepDays {
			days = n
		}
	}

	// ETag 基于数据版本（同步+绑定）与输出天数：数据未变则 304
	etag := fmt.Sprintf(`"xml-%s-%d"`, s.dataVersion(), days)
	w.Header().Set("ETag", etag)
	w.Header().Set("Cache-Control", "no-cache")
	if match := r.Header.Get("If-None-Match"); match != "" && match == etag {
		w.WriteHeader(http.StatusNotModified)
		return
	}

	// 串行化生成：等待中的请求拿到锁后复查 ETag（可能别人刚生成完，数据未变）
	s.xmlMu.Lock()
	defer s.xmlMu.Unlock()
	if match := r.Header.Get("If-None-Match"); match != "" && match == etag {
		w.WriteHeader(http.StatusNotModified)
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
	channels := store.ApplyBindings(all, bindings)
	ids := make([]int64, 0, len(channels))
	idToXmltv := make(map[int64]string, len(channels))
	for _, c := range channels {
		ids = append(ids, c.ID)
		idToXmltv[c.ID] = c.XmltvID
	}

	from := epgtime.DayStart(time.Now(), s.tz())
	to := from.AddDate(0, 0, days)

	w.Header().Set("Content-Type", "application/xml; charset=utf-8")
	w.Header().Set("Content-Disposition", `inline; filename="epg.xml"`)
	var out io.Writer = w
	if strings.Contains(r.Header.Get("Accept-Encoding"), "gzip") {
		w.Header().Set("Content-Encoding", "gzip")
		gz := gzip.NewWriter(w)
		defer gz.Close()
		out = gz
	}

	stream := func(from, to time.Time, fn func(store.Programme) error) error {
		return s.store.StreamProgrammesBetween(r.Context(), ids, from, to, true, fn)
	}
	if err := writeXMLTV(out, channels, idToXmltv, from, to, stream); err != nil {
		// 响应头已写出无法改状态码，记录日志即可（多为客户端提前断开）
		log.Printf("[server] XML 输出中断: %v", err)
	}
}

// streamFn 节目流式查询抽象（便于单测替身）。
type streamFn func(from, to time.Time, fn func(store.Programme) error) error

// writeXMLTV 流式生成 XMLTV 文档：先输出全部 <channel>，再按时间流式输出 <programme>。
func writeXMLTV(w io.Writer, channels []store.Channel, idToXmltv map[int64]string,
	from, to time.Time, stream streamFn) error {

	bw := bufio.NewWriterSize(w, 32*1024)
	write := func(s string) { _, _ = bw.WriteString(s) }

	write(`<?xml version="1.0" encoding="UTF-8"?>` + "\n")
	write(`<!DOCTYPE tv SYSTEM "xmltv.dtd">` + "\n")
	write(`<tv source-info-name="goepg" generator-info-name="goepg">` + "\n")
	for _, c := range channels {
		write(`<channel id="` + escAttr(c.XmltvID) + `">`)
		write(`<display-name>` + escText(c.Name) + `</display-name>`)
		if c.Icon != "" {
			write(`<icon src="` + escAttr(c.Icon) + `"/>`)
		}
		write(`</channel>` + "\n")
	}

	streamErr := stream(from, to, func(p store.Programme) error {
		xid, ok := idToXmltv[p.ChannelID]
		if !ok {
			return nil // 非代表频道的节目（该频道名已被更优源覆盖）
		}
		write(`<programme start="` + xmltvTime(p.Start) +
			`" stop="` + xmltvTime(p.Stop) +
			`" channel="` + escAttr(xid) + `">`)
		if p.Title != "" {
			write(`<title>` + escText(p.Title) + `</title>`)
		}
		if p.SubTitle != "" {
			write(`<sub-title>` + escText(p.SubTitle) + `</sub-title>`)
		}
		if p.Descr != "" {
			write(`<desc>` + escText(p.Descr) + `</desc>`)
		}
		if p.Category != "" {
			write(`<category>` + escText(p.Category) + `</category>`)
		}
		write(`</programme>` + "\n")
		return nil
	})
	if streamErr == nil {
		write(`</tv>`)
	}
	if err := bw.Flush(); err != nil && streamErr == nil {
		streamErr = err
	}
	return streamErr
}

// escText XML 文本节点转义（EscapeText 覆盖 & < > " ' 及控制字符）。
func escText(s string) string {
	var b strings.Builder
	_ = xml.EscapeText(&b, []byte(s))
	return b.String()
}

// escAttr XML 属性值转义。
func escAttr(s string) string { return escText(s) }
