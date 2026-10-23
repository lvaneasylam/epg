package store

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// DeleteSourceData 删除某源的全部频道与节目（源被移除时调用；常规同步
// 已改为 UPSERT + 差量清理，不再全删，历史节目得以保留）。
func (s *Store) DeleteSourceData(ctx context.Context, source string) error {
	if _, err := s.db.ExecContext(ctx,
		`DELETE p FROM epg_programmes p
		 JOIN epg_channels c ON p.channel_id = c.id
		 WHERE c.source = ?`, source); err != nil {
		return err
	}
	_, err := s.db.ExecContext(ctx, `DELETE FROM epg_channels WHERE source = ?`, source)
	return err
}

// ChannelInput 待写入的频道（同步阶段产物）。
type ChannelInput struct {
	XmltvID string
	Name    string
	Icon    string
}

// ReplaceSourceChannels 同步某源频道：UPSERT（id 稳定，历史节目才挂得住）+
// 差量清理源里已消失的频道（连同其节目）。
// 返回 xmltv_id -> 频道 id 映射，供节目写入阶段关联。
// 源文件内同一 xmltv_id 重复出现时仅保留第一个（真实源数据存在此脏数据，撞 uk 需去重）。
func (s *Store) ReplaceSourceChannels(ctx context.Context, source string, priority int,
	channels []ChannelInput) (map[string]int64, error) {
	deduped := make([]ChannelInput, 0, len(channels))
	seen := make(map[string]bool, len(channels))
	for _, c := range channels {
		// MySQL utf8mb4 默认排序规则不区分大小写：tvN 与 Tvn 会撞唯一键，
		// 去重键必须同样按小写归一化
		key := strings.ToLower(c.XmltvID)
		if seen[key] {
			continue
		}
		seen[key] = true
		deduped = append(deduped, c)
	}
	channels = deduped

	// 已有频道行 UPSERT：更新 name/icon/priority/updated_at，id 不变
	now := time.Now()
	const batch = 500
	var sb strings.Builder
	for start := 0; start < len(channels); start += batch {
		end := min(start+batch, len(channels))
		sb.Reset()
		sb.WriteString(`INSERT INTO epg_channels (source, priority, xmltv_id, name, icon, updated_at) VALUES `)
		args := make([]any, 0, (end-start)*6)
		for i, c := range channels[start:end] {
			if i > 0 {
				sb.WriteByte(',')
			}
			sb.WriteString("(?,?,?,?,?,?)")
			args = append(args, source, priority, c.XmltvID, c.Name, c.Icon, now)
		}
		sb.WriteString(` ON DUPLICATE KEY UPDATE
			priority=VALUES(priority), name=VALUES(name), icon=VALUES(icon), updated_at=VALUES(updated_at)`)
		if _, err := s.db.ExecContext(ctx, sb.String(), args...); err != nil {
			return nil, fmt.Errorf("写入源 %s 频道失败: %w", source, err)
		}
	}

	// 读回该源全部频道行，建映射并找出源里已消失的（连同节目删除）
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, xmltv_id FROM epg_channels WHERE source = ?`, source)
	if err != nil {
		return nil, err
	}
	type existCh struct {
		id  int64
		xid string
	}
	var existing []existCh
	for rows.Next() {
		var e existCh
		if err := rows.Scan(&e.id, &e.xid); err != nil {
			rows.Close()
			return nil, err
		}
		existing = append(existing, e)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}

	m := make(map[string]int64, len(existing))
	var vanishedIDs []int64
	for _, e := range existing {
		key := strings.ToLower(e.xid) // 键小写归一化，与去重口径一致
		if seen[key] {
			m[key] = e.id
		} else {
			vanishedIDs = append(vanishedIDs, e.id)
		}
	}
	if err := s.deleteChannelsByID(ctx, vanishedIDs); err != nil {
		return nil, fmt.Errorf("清理源 %s 消失频道失败: %w", source, err)
	}
	return m, nil
}

// deleteChannelsByID 删除频道行及其全部节目（id 分批）。
func (s *Store) deleteChannelsByID(ctx context.Context, ids []int64) error {
	const batch = 500
	for start := 0; start < len(ids); start += batch {
		end := min(start+batch, len(ids))
		part := ids[start:end]
		args := make([]any, 0, len(part))
		ph := ""
		for i, id := range part {
			if i > 0 {
				ph += ","
			}
			ph += "?"
			args = append(args, id)
		}
		if _, err := s.db.ExecContext(ctx,
			`DELETE FROM epg_programmes WHERE channel_id IN (`+ph+`)`, args...); err != nil {
			return err
		}
		if _, err := s.db.ExecContext(ctx,
			`DELETE FROM epg_channels WHERE id IN (`+ph+`)`, args...); err != nil {
			return err
		}
	}
	return nil
}

// InsertProgrammes 批量写入节目：uk_channel_start 冲突时更新内容
// （历史数据保留策略下，同一节目多轮同步以源数据最新内容为准）。
func (s *Store) InsertProgrammes(ctx context.Context, progs []Programme) error {
	const batch = 500
	var sb strings.Builder
	for start := 0; start < len(progs); start += batch {
		end := min(start+batch, len(progs))
		sb.Reset()
		sb.WriteString(`INSERT INTO epg_programmes
			(channel_id, start, stop, title, sub_title, descr, category) VALUES `)
		args := make([]any, 0, (end-start)*7)
		for i, p := range progs[start:end] {
			if i > 0 {
				sb.WriteByte(',')
			}
			sb.WriteString("(?,?,?,?,?,?,?)")
			args = append(args, p.ChannelID, p.Start, p.Stop, p.Title, p.SubTitle, p.Descr, p.Category)
		}
		sb.WriteString(` ON DUPLICATE KEY UPDATE
			stop=VALUES(stop), title=VALUES(title), sub_title=VALUES(sub_title),
			descr=VALUES(descr), category=VALUES(category)`)
		if _, err := s.db.ExecContext(ctx, sb.String(), args...); err != nil {
			return fmt.Errorf("批量写入节目失败: %w", err)
		}
	}
	return nil
}

// CleanupProgrammes 删除 start 早于 before 的过期节目，
// 分批 DELETE ... LIMIT（MySQL 5.6 支持），避免超长事务锁表。
func (s *Store) CleanupProgrammes(ctx context.Context, before time.Time) (int64, error) {
	var total int64
	for {
		res, err := s.db.ExecContext(ctx,
			`DELETE FROM epg_programmes WHERE start < ? LIMIT 5000`, before)
		if err != nil {
			return total, err
		}
		n, _ := res.RowsAffected()
		total += n
		if n < 5000 {
			return total, nil
		}
	}
}

// SweepOrphanSources 清理已从配置移除的源遗留数据，返回被清理的源名列表。
func (s *Store) SweepOrphanSources(ctx context.Context, keep []string) ([]string, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT DISTINCT source FROM epg_channels`)
	if err != nil {
		return nil, err
	}
	var all []string
	for rows.Next() {
		var src string
		if err := rows.Scan(&src); err != nil {
			rows.Close()
			return nil, err
		}
		all = append(all, src)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	keepSet := make(map[string]bool, len(keep))
	for _, k := range keep {
		keepSet[k] = true
	}
	var removed []string
	for _, src := range all {
		if !keepSet[src] {
			if err := s.DeleteSourceData(ctx, src); err != nil {
				return removed, err
			}
			removed = append(removed, src)
		}
	}
	return removed, nil
}

// Ping 探活（健康检查用）。
func (s *Store) Ping(ctx context.Context) error {
	return s.db.PingContext(ctx)
}
