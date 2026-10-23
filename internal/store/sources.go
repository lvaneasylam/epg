package store

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

// ErrNotFound 通用未找到错误。
var ErrNotFound = errors.New("记录不存在")

// SourceRow epg_sources 记录（管理页可增删改，运行时权威）。
type SourceRow struct {
	ID          int64     `json:"id"`
	Name        string    `json:"name"`
	URL         string    `json:"url"`
	Priority    int       `json:"priority"`
	IntervalMin int       `json:"interval_min"`
	Enabled     bool      `json:"enabled"`
	UpdatedAt   time.Time `json:"updated_at"`
}

// ListSources 返回全部源（含禁用），按 name 排序。
func (s *Store) ListSources(ctx context.Context) ([]SourceRow, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, name, url, priority, interval_min, enabled, updated_at
		 FROM epg_sources ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []SourceRow
	for rows.Next() {
		var r SourceRow
		if err := rows.Scan(&r.ID, &r.Name, &r.URL, &r.Priority, &r.IntervalMin, &r.Enabled, &r.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// SeedSources 配置文件源导入：仅插入 DB 中不存在的 name，已有记录以 DB 为准
// （管理页修改不会被重启覆盖）。
func (s *Store) SeedSources(ctx context.Context, seeds []struct {
	Name, URL string
	Priority  int
	Enabled   bool
}) error {
	now := time.Now()
	for _, it := range seeds {
		_, err := s.db.ExecContext(ctx,
			`INSERT IGNORE INTO epg_sources (name, url, priority, enabled, created_at, updated_at)
			 VALUES (?, ?, ?, ?, ?, ?)`,
			it.Name, it.URL, it.Priority, it.Enabled, now, now)
		if err != nil {
			return err
		}
	}
	return nil
}

// CreateSource 新增源。
func (s *Store) CreateSource(ctx context.Context, r *SourceRow) error {
	now := time.Now()
	res, err := s.db.ExecContext(ctx,
		`INSERT INTO epg_sources (name, url, priority, interval_min, enabled, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?)`,
		r.Name, r.URL, r.Priority, r.IntervalMin, r.Enabled, now, now)
	if err != nil {
		return err
	}
	r.ID, _ = res.LastInsertId()
	r.UpdatedAt = now
	return nil
}

// UpdateSource 更新源字段。
func (s *Store) UpdateSource(ctx context.Context, id int64, r *SourceRow) error {
	res, err := s.db.ExecContext(ctx,
		`UPDATE epg_sources SET name=?, url=?, priority=?, interval_min=?, enabled=?, updated_at=?
		 WHERE id=?`,
		r.Name, r.URL, r.Priority, r.IntervalMin, r.Enabled, time.Now(), id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// DeleteSource 删除源定义并清理其入库数据。
func (s *Store) DeleteSource(ctx context.Context, id int64) error {
	var name string
	err := s.db.QueryRowContext(ctx, `SELECT name FROM epg_sources WHERE id=?`, id).Scan(&name)
	if err == sql.ErrNoRows {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	if _, err := s.db.ExecContext(ctx, `DELETE FROM epg_sources WHERE id=?`, id); err != nil {
		return err
	}
	return s.DeleteSourceData(ctx, name)
}

// ToggleSource 启用/禁用；禁用不删数据（重新启用后可直接同步）。
func (s *Store) ToggleSource(ctx context.Context, id int64, enabled bool) error {
	res, err := s.db.ExecContext(ctx,
		`UPDATE epg_sources SET enabled=?, updated_at=? WHERE id=?`, enabled, time.Now(), id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// GetSource 按 id 查源。
func (s *Store) GetSource(ctx context.Context, id int64) (*SourceRow, error) {
	var r SourceRow
	err := s.db.QueryRowContext(ctx,
		`SELECT id, name, url, priority, interval_min, enabled, updated_at
		 FROM epg_sources WHERE id=?`, id).
		Scan(&r.ID, &r.Name, &r.URL, &r.Priority, &r.IntervalMin, &r.Enabled, &r.UpdatedAt)
	if err == sql.ErrNoRows {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &r, nil
}
