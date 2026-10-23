package store

import (
	"context"
	"time"
)

// SourceStat 源维度的覆盖统计。
type SourceStat struct {
	ChannelCount int64      `json:"channel_count"`
	ProgramCount int64      `json:"program_count"`
	CoverUntil   *time.Time `json:"cover_until"`
}

// SourceStats 按源统计频道数、节目数与数据覆盖截止时间。
func (s *Store) SourceStats(ctx context.Context) (map[string]SourceStat, error) {
	out := map[string]SourceStat{}

	chRows, err := s.db.QueryContext(ctx,
		`SELECT source, COUNT(*) FROM epg_channels GROUP BY source`)
	if err != nil {
		return nil, err
	}
	for chRows.Next() {
		var src string
		var n int64
		if err := chRows.Scan(&src, &n); err != nil {
			chRows.Close()
			return nil, err
		}
		st := out[src]
		st.ChannelCount = n
		out[src] = st
	}
	chRows.Close()
	if err := chRows.Err(); err != nil {
		return nil, err
	}

	pgRows, err := s.db.QueryContext(ctx,
		`SELECT c.source, COUNT(*), MAX(p.stop)
		 FROM epg_programmes p JOIN epg_channels c ON p.channel_id = c.id
		 GROUP BY c.source`)
	if err != nil {
		return nil, err
	}
	defer pgRows.Close()
	for pgRows.Next() {
		var src string
		var n int64
		var mx *time.Time
		if err := pgRows.Scan(&src, &n, &mx); err != nil {
			return nil, err
		}
		st := out[src]
		st.ProgramCount = n
		st.CoverUntil = mx
		out[src] = st
	}
	return out, pgRows.Err()
}
