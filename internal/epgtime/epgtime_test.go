package epgtime

import (
	"testing"
	"time"
)

func TestDayStart(t *testing.T) {
	sh, _ := time.LoadLocation("Asia/Shanghai")
	// 回归：time.Truncate(24h) 在非 UTC 时区会得到错误的"当天零点"
	got := DayStart(time.Date(2026, 10, 23, 9, 30, 0, 0, sh), sh)
	want := time.Date(2026, 10, 23, 0, 0, 0, 0, sh)
	if !got.Equal(want) {
		t.Errorf("DayStart = %v, want %v", got, want)
	}
	// UTC 时间输入也应归一化到 loc 当天
	got2 := DayStart(time.Date(2026, 10, 22, 20, 0, 0, 0, time.UTC), sh)
	want2 := time.Date(2026, 10, 23, 0, 0, 0, 0, sh) // UTC 22日20点 = 上海23日04点
	if !got2.Equal(want2) {
		t.Errorf("DayStart(UTC 输入) = %v, want %v", got2, want2)
	}
}

func TestParseDate(t *testing.T) {
	sh, _ := time.LoadLocation("Asia/Shanghai")
	got, ok := ParseDate("2026-10-23", sh)
	if !ok || got.Format("2006-01-02 15:00") != "2026-10-23 00:00" {
		t.Errorf("ParseDate = %v, ok=%v", got, ok)
	}
	if _, ok := ParseDate("2026/10/23", sh); ok {
		t.Error("非标准格式应失败")
	}
	if _, ok := ParseDate("2026-13-40", sh); ok {
		t.Error("非法日期应失败")
	}
}
