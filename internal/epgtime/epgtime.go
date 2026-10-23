// Package epgtime 提供节目单业务的时间工具。
// 注意：time.Truncate 按UTC纪元对齐，非UTC时区直接使用会得到错误的"当天零点"，
// 必须用日历分量重建。
package epgtime

import "time"

// DayStart 返回 t 所在时区 loc 下"当天 00:00:00"。
func DayStart(t time.Time, loc *time.Location) time.Time {
	y, m, d := t.In(loc).Date()
	return time.Date(y, m, d, 0, 0, 0, 0, loc)
}

// ParseDate 解析 "2006-01-02" 形式的日期参数到 loc 时区当天零点。
func ParseDate(s string, loc *time.Location) (time.Time, bool) {
	t, err := time.ParseInLocation("2006-01-02", s, loc)
	if err != nil {
		return time.Time{}, false
	}
	return t, true
}

// FormatDate 以 "2006-01-02" 输出。
func FormatDate(t time.Time) string {
	return t.Format("2006-01-02")
}
