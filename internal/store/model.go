package store

import "time"

// Channel 频道记录。
type Channel struct {
	ID       int64  `json:"id"`
	Source   string `json:"source"`
	Priority int    `json:"priority"`
	XmltvID  string `json:"xmltv_id"`
	Name     string `json:"name"`
	Icon     string `json:"icon"`
}

// Programme 节目记录。
type Programme struct {
	ChannelID int64     `json:"-"`
	Start     time.Time `json:"start"`
	Stop      time.Time `json:"stop"`
	Title     string    `json:"title"`
	SubTitle  string    `json:"sub_title"`
	Descr     string    `json:"desc"`
	Category  string    `json:"category"`
}

// Stats 库存统计。
type Stats struct {
	ChannelCount int64      `json:"channel_count"`
	ProgramCount int64      `json:"program_count"`
	Earliest     *time.Time `json:"earliest"`
	Latest       *time.Time `json:"latest"`
}
