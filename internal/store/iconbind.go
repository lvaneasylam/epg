package store

import (
	"context"
	"time"
)

// IconBindRow 频道图标手动绑定：把对外频道名锁定到指定图标文件，
// 优先于 iconlib 的自动模糊匹配。
type IconBindRow struct {
	ChannelName string    `json:"channel_name"`
	IconPath    string    `json:"icon_path"`
	UpdatedAt   time.Time `json:"updated_at"`
}

// SetIconBind 写入频道图标绑定（同频道覆盖）。
func (s *Store) SetIconBind(ctx context.Context, channelName, iconPath string) error {
	now := time.Now()
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO epg_icon_bindings (channel_name, icon_path, created_at, updated_at)
		 VALUES (?, ?, ?, ?)
		 ON DUPLICATE KEY UPDATE icon_path=?, updated_at=?`,
		channelName, iconPath, now, now, iconPath, now)
	return err
}

// DeleteIconBind 解除频道图标绑定。
func (s *Store) DeleteIconBind(ctx context.Context, channelName string) error {
	_, err := s.db.ExecContext(ctx,
		`DELETE FROM epg_icon_bindings WHERE channel_name=?`, channelName)
	return err
}

// DeleteIconBindsByPath 清理指向某图标文件的全部绑定（图标删除时连带清理）。
func (s *Store) DeleteIconBindsByPath(ctx context.Context, iconPath string) error {
	_, err := s.db.ExecContext(ctx,
		`DELETE FROM epg_icon_bindings WHERE icon_path=?`, iconPath)
	return err
}

// UpdateIconBindPath 图标文件替换后（扩展名变化导致落盘路径变化），
// 把旧路径上的绑定迁移到新路径，保持「频道 → 图标」关系不断。
func (s *Store) UpdateIconBindPath(ctx context.Context, oldPath, newPath string) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE epg_icon_bindings SET icon_path=?, updated_at=? WHERE icon_path=?`,
		newPath, time.Now(), oldPath)
	return err
}

// ListIconBinds 全部图标绑定（按频道名排序）。
func (s *Store) ListIconBinds(ctx context.Context) ([]IconBindRow, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT channel_name, icon_path, updated_at
		 FROM epg_icon_bindings ORDER BY channel_name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]IconBindRow, 0) // 空表也要序列化为 [] 而非 null（前端直接 .filter）
	for rows.Next() {
		var r IconBindRow
		if err := rows.Scan(&r.ChannelName, &r.IconPath, &r.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}
