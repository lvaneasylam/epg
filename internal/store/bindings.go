package store

import (
	"context"
	"database/sql"
	"time"
)

// BindingRow 频道手动绑定：把对外频道名锁定到指定源的频道，
// 优先于自动的跨源同名去重（priority）。
type BindingRow struct {
	ID            int64     `json:"id"`
	ChannelName   string    `json:"channel_name"`
	TargetChannel int64     `json:"target_channel_id"`
	TargetName    string    `json:"target_name"`   // join 出的目标频道显示名
	TargetSource  string    `json:"target_source"` // 目标频道所属源
	UpdatedAt     time.Time `json:"updated_at"`
}

// SetBinding 写入绑定（同名覆盖）。
func (s *Store) SetBinding(ctx context.Context, channelName string, targetChannelID int64) error {
	now := time.Now()
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO epg_bindings (channel_name, target_channel_id, created_at, updated_at)
		 VALUES (?, ?, ?, ?)
		 ON DUPLICATE KEY UPDATE target_channel_id=?, updated_at=?`,
		channelName, targetChannelID, now, now, targetChannelID, now)
	return err
}

// DeleteBinding 解除绑定。
func (s *Store) DeleteBinding(ctx context.Context, channelName string) error {
	_, err := s.db.ExecContext(ctx,
		`DELETE FROM epg_bindings WHERE channel_name=?`, channelName)
	return err
}

// ListBindings 全部绑定（带目标频道信息）。
func (s *Store) ListBindings(ctx context.Context) ([]BindingRow, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT b.id, b.channel_name, b.target_channel_id, IFNULL(c.name,''), IFNULL(c.source,''), b.updated_at
		 FROM epg_bindings b
		 LEFT JOIN epg_channels c ON c.id = b.target_channel_id
		 ORDER BY b.channel_name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []BindingRow
	for rows.Next() {
		var r BindingRow
		if err := rows.Scan(&r.ID, &r.ChannelName, &r.TargetChannel, &r.TargetName, &r.TargetSource, &r.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// bindingChannel 查绑定目标的频道行，未绑定返回 nil。
func (s *Store) bindingChannel(ctx context.Context, name string) (*Channel, error) {
	var c Channel
	err := s.db.QueryRowContext(ctx,
		`SELECT c.id, c.source, c.priority, c.xmltv_id, c.name, c.icon
		 FROM epg_bindings b
		 JOIN epg_channels c ON c.id = b.target_channel_id
		 WHERE b.channel_name = ?`, name).
		Scan(&c.ID, &c.Source, &c.Priority, &c.XmltvID, &c.Name, &c.Icon)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &c, nil
}
