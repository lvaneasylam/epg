package server

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"goepg/internal/config"
	"goepg/internal/epgtime"
	"goepg/internal/store"
)

func TestWriteXMLTV(t *testing.T) {
	sh, _ := time.LoadLocation("Asia/Shanghai")
	channels := []store.Channel{
		{ID: 1, XmltvID: "cctv1", Name: "CCTV-1 综合", Icon: "http://x/1.png"},
		{ID: 2, XmltvID: "cctv2", Name: "CCTV-2"},
	}
	idToXmltv := map[int64]string{1: "cctv1", 2: "cctv2"}
	progs := []store.Programme{
		{ChannelID: 1,
			Start: time.Date(2026, 10, 23, 8, 0, 0, 0, sh),
			Stop:  time.Date(2026, 10, 23, 9, 0, 0, 0, sh),
			Title: "朝闻天下 & 直播 <早间>", Category: "新闻"},
		{ChannelID: 99, // 非代表频道应被过滤
			Start: time.Date(2026, 10, 23, 8, 0, 0, 0, sh),
			Stop:  time.Date(2026, 10, 23, 9, 0, 0, 0, sh),
			Title: "不应输出"},
	}
	stream := func(_, _ time.Time, fn func(store.Programme) error) error {
		for _, p := range progs {
			if err := fn(p); err != nil {
				return err
			}
		}
		return nil
	}

	var buf bytes.Buffer
	if err := writeXMLTV(&buf, channels, idToXmltv, time.Time{}, time.Time{}, stream); err != nil {
		t.Fatal(err)
	}
	out := buf.String()

	// 结构检查
	for _, want := range []string{
		`<?xml version="1.0" encoding="UTF-8"?>`,
		`<tv source-info-name="goepg"`,
		`<channel id="cctv1">`,
		`<display-name>CCTV-1 综合</display-name>`,
		`<icon src="http://x/1.png"/>`,
		`start="20261023080000 +0800" stop="20261023090000 +0800" channel="cctv1"`,
		`<title>朝闻天下 &amp; 直播 &lt;早间&gt;</title>`,
		`<category>新闻</category>`,
		`</tv>`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("输出缺少: %q\n输出:\n%s", want, out)
		}
	}
	if strings.Contains(out, "不应输出") {
		t.Error("非代表频道的节目不应出现在输出中")
	}
}

func TestDiypHHMM(t *testing.T) {
	sh, _ := time.LoadLocation("Asia/Shanghai")
	day := time.Date(2026, 10, 23, 0, 0, 0, 0, sh)
	dayEnd := day.Add(24 * time.Hour)

	cases := []struct {
		t    time.Time
		want string
	}{
		{day.Add(23*time.Hour + 50*time.Minute), "23:50"}, // 窗口内原样
		{day.Add(9 * time.Hour), "09:00"},                 // 窗口内原样
		{day.Add(-10 * time.Minute), "00:00"},             // 前一天开始 → 对齐 00:00
		{day.Add(-2 * time.Hour), "00:00"},                // 前一天更早开始 → 对齐 00:00
		{dayEnd.Add(30 * time.Minute), "23:59"},           // 延续到次日 → 对齐 23:59
		{dayEnd, "23:59"},                                 // 恰好到次日 0 点 → 23:59
	}
	for _, c := range cases {
		if got := diypHHMM(c.t, day, dayEnd, sh); got != c.want {
			t.Errorf("diypHHMM(%v) = %s, want %s", c.t, got, c.want)
		}
	}
}

func TestBuildEPGBody(t *testing.T) {
	sh, _ := time.LoadLocation("Asia/Shanghai")
	day := time.Date(2026, 10, 24, 0, 0, 0, 0, sh)
	progs := []store.Programme{
		{ // 跨天节目：前一天 23:50 开始 → epg_data 对齐 00:00，programmes 保留绝对时间
			Start: time.Date(2026, 10, 23, 23, 50, 0, 0, sh),
			Stop:  time.Date(2026, 10, 24, 1, 30, 0, 0, sh),
			Title: "跨天节目", Descr: "简介",
		},
		{Start: day.Add(8 * time.Hour), Stop: day.Add(9 * time.Hour), Title: "窗口内", Category: "新闻"},
	}

	body := buildEPGBody("湖南卫视", "2026-10-24", "https://x.cn/icon/a.png", progs, day, sh)
	var m map[string]any
	if err := json.Unmarshal(body, &m); err != nil {
		t.Fatalf("输出非合法 JSON: %v", err)
	}
	// DIYP 主体字段（用户约束：格式不可变）
	for _, k := range []string{"channel_name", "date", "url", "icon", "epg_data", "programmes"} {
		if _, ok := m[k]; !ok {
			t.Errorf("缺少字段 %q", k)
		}
	}
	ed := m["epg_data"].([]any)
	if len(ed) != 2 {
		t.Fatalf("epg_data 条数 = %d, want 2", len(ed))
	}
	first := ed[0].(map[string]any)
	if first["start"] != "00:00" || first["end"] != "01:30" || first["title"] != "跨天节目" || first["desc"] != "简介" {
		t.Errorf("跨天条目对齐异常: %v", first)
	}
	pm := m["programmes"].([]any)
	if pm[0].(map[string]any)["start"] != "2026-10-23T23:50:00+08:00" {
		t.Errorf("programmes 应保留绝对时间: %v", pm[0])
	}

	// 无节目：epg_data 为空串（DIYP 兼容），programmes 为空数组
	empty := buildEPGBody("X", "2026-10-24", "", nil, day, sh)
	var em map[string]any
	_ = json.Unmarshal(empty, &em)
	if em["epg_data"] != "" {
		t.Errorf("无节目 epg_data 应为空串: %v", em["epg_data"])
	}
}

func TestEPGCacheKeyAndTTL(t *testing.T) {
	if got := epgCacheKey("CCTV-1 综合", "2026-10-24"); got != "epg:ch:cctv-1 综合:2026-10-24" {
		t.Errorf("epgCacheKey = %q", got)
	}
	sh, _ := time.LoadLocation("Asia/Shanghai")
	s := &Server{cfg: &config.Config{}, loc: sh}
	s.cfg.Redis.CacheTTL = 5 * time.Minute
	today := epgtime.FormatDate(epgtime.DayStart(time.Now(), sh))
	cases := map[string]time.Duration{
		today: epgWarmTTL,
		epgtime.FormatDate(time.Now().AddDate(0, 0, 1)):  epgWarmTTL,
		epgtime.FormatDate(time.Now().AddDate(0, 0, -1)): epgWarmTTL,
		epgtime.FormatDate(time.Now().AddDate(0, 0, -3)): 5 * time.Minute,
	}
	for date, want := range cases {
		if got := s.epgCacheTTL(date); got != want {
			t.Errorf("epgCacheTTL(%s) = %v, want %v", date, got, want)
		}
	}
}

func TestIconJoin(t *testing.T) {
	if got := iconJoin("https://x.cn", "CCTV1"); got != "https://x.cn/icon/CCTV1.png" {
		t.Errorf("iconJoin = %q", got)
	}
	if got := iconJoin("https://x.cn/", "湖南卫视"); got != "https://x.cn/icon/%E6%B9%96%E5%8D%97%E5%8D%AB%E8%A7%86.png" {
		t.Errorf("iconJoin 带尾斜杠 = %q", got)
	}
}
