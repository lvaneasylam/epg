package xmltv

import (
	"io"
	"strings"
	"testing"
	"time"
)

var shanghai = mustLoc()

func mustLoc() *time.Location {
	loc, err := time.LoadLocation("Asia/Shanghai")
	if err != nil {
		panic(err)
	}
	return loc
}

func TestParseTime(t *testing.T) {
	cases := []struct {
		in   string
		want string // RFC3339 期望
	}{
		{"20261023080000 +0800", "2026-10-23T08:00:00+08:00"},
		{"20261023080000", "2026-10-23T08:00:00+08:00"},       // 无时区，按 loc
		{"202610230800 +0800", "2026-10-23T08:00:00+08:00"},   // 无秒
		{"20261023090000 +0000", "2026-10-23T17:00:00+08:00"}, // 跨时区换算
	}
	for _, c := range cases {
		got, ok := ParseTime(c.in, shanghai)
		if !ok {
			t.Fatalf("ParseTime(%q) 解析失败", c.in)
		}
		if got.Format(time.RFC3339) != c.want {
			t.Errorf("ParseTime(%q) = %s, want %s", c.in, got.Format(time.RFC3339), c.want)
		}
	}
	if _, ok := ParseTime("garbage", shanghai); ok {
		t.Error("非法时间应解析失败")
	}
	if _, ok := ParseTime("", shanghai); ok {
		t.Error("空时间应解析失败")
	}
}

const sampleXML = `<?xml version="1.0" encoding="UTF-8"?>
<tv>
  <channel id="cctv1">
    <display-name>CCTV-1 综合</display-name>
    <display-name>CCTV1</display-name>
    <icon src="https://example.com/cctv1.png"/>
  </channel>
  <channel id="no-name"><display-name></display-name></channel>
  <programme start="20261023080000 +0800" stop="20261023090000 +0800" channel="cctv1">
    <title lang="zh">朝闻天下</title>
    <sub-title>第一段</sub-title>
    <desc lang="zh">新闻节目 &amp; 直播</desc>
    <category lang="zh">新闻</category>
  </programme>
  <programme start="20261023090000 +0800" channel="cctv1">
    <title>新闻直播间</title>
  </programme>
  <programme start="bad-time" stop="20261023100000 +0800" channel="cctv1">
    <title>应被丢弃</title>
  </programme>
</tv>`

func TestParseChannels(t *testing.T) {
	chs, err := ParseChannels(strings.NewReader(sampleXML))
	if err != nil {
		t.Fatal(err)
	}
	if len(chs) != 1 {
		t.Fatalf("有效频道应为 1（缺 display-name 的丢弃），实际 %d", len(chs))
	}
	c := chs[0]
	if c.XmltvID != "cctv1" || c.Name != "CCTV-1 综合" || c.Icon == "" {
		t.Errorf("频道字段解析错误: %+v", c)
	}
}

func TestParseProgrammes(t *testing.T) {
	var got []ProgrammeInput
	n, err := ParseProgrammes(strings.NewReader(sampleXML), shanghai, func(p ProgrammeInput) error {
		got = append(got, p)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if n != 2 || len(got) != 2 {
		t.Fatalf("有效节目应为 2（坏时间丢弃），实际 %d", n)
	}
	first := got[0]
	if first.Title != "朝闻天下" || first.SubTitle != "第一段" {
		t.Errorf("节目字段错误: %+v", first)
	}
	if first.Descr != "新闻节目 & 直播" {
		t.Errorf("desc 实体解码错误: %q", first.Descr)
	}
	// 缺 stop 的节目以 Stop==Start 标记，由同步层按默认时长补齐
	if !got[1].Stop.Equal(got[1].Start) {
		t.Errorf("缺 stop 应标记 Stop==Start: %+v", got[1])
	}
}

func TestParseProgrammesStopEarly(t *testing.T) {
	// fn 返回错误应中止解析
	_, err := ParseProgrammes(strings.NewReader(sampleXML), shanghai, func(ProgrammeInput) error {
		return io.ErrClosedPipe
	})
	if err != io.ErrClosedPipe {
		t.Errorf("应透传回调错误，实际 %v", err)
	}
}
