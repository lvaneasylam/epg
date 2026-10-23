// Package xmltv 实现 XMLTV (DTD) 格式的流式解析，处理常见 EPG 源的时间格式差异。
package xmltv

import (
	"encoding/xml"
	"errors"
	"io"
	"strings"
	"time"
)

// timeLayouts XMLTV 规范时间为 "YYYYMMDDHHmmss ZZZ"，兼容无秒/无时区的变体。
var timeLayouts = []string{
	"20060102150405 -0700",
	"20060102150405",
	"200601021504 -0700",
	"200601021504",
}

// ParseTime 解析 XMLTV 时间属性，统一换算到 loc 时区。
func ParseTime(s string, loc *time.Location) (time.Time, bool) {
	s = strings.TrimSpace(s)
	if len(s) < 14 {
		return time.Time{}, false
	}
	for _, layout := range timeLayouts {
		if t, err := time.ParseInLocation(layout, s, loc); err == nil {
			return t.In(loc), true
		}
	}
	return time.Time{}, false
}

// channel XMLTV <channel> 节点。
type channel struct {
	ID           string   `xml:"id,attr"`
	DisplayNames []string `xml:"display-name"`
	Icons        []struct {
		Src string `xml:"src,attr"`
	} `xml:"icon"`
}

// programme XMLTV <programme> 节点。
type programme struct {
	Channel    string   `xml:"channel,attr"`
	Start      string   `xml:"start,attr"`
	Stop       string   `xml:"stop,attr"`
	Titles     []string `xml:"title"`
	SubTitles  []string `xml:"sub-title"`
	Descs      []string `xml:"desc"`
	Categories []string `xml:"category"`
}

// ChannelInput 解析产物：频道。
type ChannelInput struct {
	XmltvID string
	Name    string
	Icon    string
}

// ProgrammeInput 解析产物：节目（Start/Stop 已归一化到 loc 时区）。
type ProgrammeInput struct {
	XmltvID  string
	Start    time.Time
	Stop     time.Time
	Title    string
	SubTitle string
	Descr    string
	Category string
}

// ParseChannels 流式解析文档中全部 <channel> 节点。
// 频道量级小（数千），一次返回；节目则通过 ParseProgrammes 回调流式处理。
func ParseChannels(r io.Reader) ([]ChannelInput, error) {
	dec := xml.NewDecoder(r)
	var out []ChannelInput
	for {
		tok, err := dec.Token()
		if err == io.EOF {
			return out, nil
		}
		if err != nil {
			return nil, err
		}
		se, ok := tok.(xml.StartElement)
		if !ok || se.Name.Local != "channel" {
			continue
		}
		var c channel
		if err := dec.DecodeElement(&c, &se); err != nil {
			continue // 单个节点损坏跳过，不中断整体
		}
		name := firstNonEmpty(c.DisplayNames)
		if c.ID == "" || name == "" {
			continue
		}
		ci := ChannelInput{XmltvID: c.ID, Name: name}
		for _, ic := range c.Icons {
			if ic.Src != "" {
				ci.Icon = ic.Src
				break
			}
		}
		out = append(out, ci)
	}
}

// ParseProgrammes 流式解析全部 <programme> 节点，逐条回调 fn。
// 返回已回调的条数；fn 返回错误时中止解析。
func ParseProgrammes(r io.Reader, loc *time.Location, fn func(ProgrammeInput) error) (int, error) {
	dec := xml.NewDecoder(r)
	count := 0
	for {
		tok, err := dec.Token()
		if err == io.EOF {
			return count, nil
		}
		if err != nil {
			return count, err
		}
		se, ok := tok.(xml.StartElement)
		if !ok || se.Name.Local != "programme" {
			continue
		}
		var p programme
		if err := dec.DecodeElement(&p, &se); err != nil {
			continue
		}
		pi, ok := normalizeProgramme(p, loc)
		if !ok {
			continue
		}
		if err := fn(pi); err != nil {
			return count, err
		}
		count++
	}
}

// normalizeProgramme 字段归一化：时间解析、默认时长、脏数据丢弃。
func normalizeProgramme(p programme, loc *time.Location) (ProgrammeInput, bool) {
	start, ok := ParseTime(p.Start, loc)
	if !ok {
		return ProgrammeInput{}, false
	}
	stop, ok := ParseTime(p.Stop, loc)
	if !ok || !stop.After(start) {
		// 无 stop 或 stop 异常时由调用方按默认时长补齐（此处仅标记为零值）
		stop = time.Time{}
	}
	if stop.IsZero() {
		return ProgrammeInput{
			XmltvID: p.Channel, Start: start, Stop: start,
			Title: firstNonEmpty(p.Titles), SubTitle: firstNonEmpty(p.SubTitles),
			Descr: firstNonEmpty(p.Descs), Category: firstNonEmpty(p.Categories),
		}, true
	}
	return ProgrammeInput{
		XmltvID: p.Channel, Start: start, Stop: stop,
		Title: firstNonEmpty(p.Titles), SubTitle: firstNonEmpty(p.SubTitles),
		Descr: firstNonEmpty(p.Descs), Category: firstNonEmpty(p.Categories),
	}, true
}

// ErrStop 手动停止解析（内部使用）。
var ErrStop = errors.New("xmltv: stop")

func firstNonEmpty(ss []string) string {
	for _, s := range ss {
		if s != "" {
			return strings.TrimSpace(s)
		}
	}
	return ""
}
