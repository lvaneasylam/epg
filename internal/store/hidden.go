package store

import (
	"context"
	"time"
)

// 隐藏频道（黑名单式，存在即隐藏）：勾选后不在首页时间轴显示，
// 对外 XML / DIYP 接口不受影响。与 epg_bindings 同按对外频道名键控，
// 属持久化配置，存 MySQL 而非 cache（Redis 为可选组件且语义为可丢失缓存）。
// 频道从源中消失时记录保留，频道回归后隐藏依然生效。

// HiddenSet 读取全部隐藏频道名。
func (s *Store) HiddenSet(ctx context.Context) (map[string]bool, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT channel_name FROM epg_hidden_channels`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make(map[string]bool)
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, err
		}
		out[name] = true
	}
	return out, rows.Err()
}

// SetHidden 设置/取消隐藏（true=INSERT，false=DELETE，均幂等）。
func (s *Store) SetHidden(ctx context.Context, channelName string, hidden bool) error {
	if hidden {
		_, err := s.db.ExecContext(ctx,
			`INSERT IGNORE INTO epg_hidden_channels (channel_name, created_at) VALUES (?, ?)`,
			channelName, time.Now())
		return err
	}
	_, err := s.db.ExecContext(ctx,
		`DELETE FROM epg_hidden_channels WHERE channel_name = ?`, channelName)
	return err
}
