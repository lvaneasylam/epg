package server

import (
	"bytes"
	"strings"
	"testing"
	"time"

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
		{day.Add(23*time.Hour + 50*time.Minute), "23:50"},                        // 窗口内原样
		{day.Add(9 * time.Hour), "09:00"},                                       // 窗口内原样
		{day.Add(-10 * time.Minute), "00:00"},                                   // 前一天开始 → 对齐 00:00
		{day.Add(-2 * time.Hour), "00:00"},                                      // 前一天更早开始 → 对齐 00:00
		{dayEnd.Add(30 * time.Minute), "23:59"},                                 // 延续到次日 → 对齐 23:59
		{dayEnd, "23:59"},                                                       // 恰好到次日 0 点 → 23:59
	}
	for _, c := range cases {
		if got := diypHHMM(c.t, day, dayEnd, sh); got != c.want {
			t.Errorf("diypHHMM(%v) = %s, want %s", c.t, got, c.want)
		}
	}
}
