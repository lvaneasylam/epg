package store

import (
	"context"
	"database/sql"
	"time"
)

// AllChannels 返回全部频道。频道量级通常仅数千，直接全量读入内存，
// 跨源同名频道去重（按 priority，小者优先，再按 id 小者优先）在内存完成，
// 避免依赖 ONLY_FULL_GROUP_BY 等不确定的 sql_mode 行为。
func (s *Store) AllChannels(ctx context.Context) ([]Channel, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, source, priority, xmltv_id, name, icon, updated_at FROM epg_channels`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Channel
	for rows.Next() {
		var c Channel
		if err := rows.Scan(&c.ID, &c.Source, &c.Priority, &c.XmltvID, &c.Name, &c.Icon, &c.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// DedupChannels 跨源同名去重：每个频道名只保留最优源的一条记录。
func DedupChannels(all []Channel) []Channel {
	best := make(map[string]Channel, len(all))
	for _, c := range all {
		cur, ok := best[c.Name]
		if !ok || c.Priority < cur.Priority || (c.Priority == cur.Priority && c.ID < cur.ID) {
			best[c.Name] = c
		}
	}
	out := make([]Channel, 0, len(best))
	for _, c := range best {
		out = append(out, c)
	}
	sortChannels(out)
	return out
}

// BindingMap 绑定名 -> 目标频道 id。
type BindingMap map[string]int64

// ApplyBindings 自动去重后应用手动绑定：同名频道的代表强制替换为绑定目标。
func ApplyBindings(all []Channel, bindings BindingMap) []Channel {
	out := DedupChannels(all)
	if len(bindings) == 0 {
		return out
	}
	byID := make(map[int64]Channel, len(all))
	for _, c := range all {
		byID[c.ID] = c
	}
	for i := range out {
		if id, ok := bindings[out[i].Name]; ok {
			if c, ok2 := byID[id]; ok2 {
				out[i] = c
			}
		}
	}
	return out
}

// FilterHidden 过滤隐藏频道（首页时间轴用）：hidden 为名字黑名单。
// 名单按对外频道名键控，频道从源中临时消失时保留设置，频道回归后依然生效。
func FilterHidden(channels []Channel, hidden map[string]bool) []Channel {
	if len(hidden) == 0 {
		return channels
	}
	out := make([]Channel, 0, len(channels))
	for _, c := range channels {
		if !hidden[c.Name] {
			out = append(out, c)
		}
	}
	return out
}

// BindingChannelMap 全部绑定转为 map。
func (s *Store) BindingChannelMap(ctx context.Context) (BindingMap, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT channel_name, target_channel_id FROM epg_bindings`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	m := BindingMap{}
	for rows.Next() {
		var name string
		var id int64
		if err := rows.Scan(&name, &id); err != nil {
			return nil, err
		}
		m[name] = id
	}
	return m, rows.Err()
}

// sortChannels 按名称稳定排序，保证输出顺序确定。
func sortChannels(cs []Channel) {
	for i := 1; i < len(cs); i++ {
		for j := i; j > 0 && cs[j].Name < cs[j-1].Name; j-- {
			cs[j], cs[j-1] = cs[j-1], cs[j]
		}
	}
}

// FindChannel 按频道名精确查找：手动绑定优先 → 名字精确（utf8mb4 排序规则
// 不区分大小写，多同名取最优源）→ 回退 xmltv_id 匹配。
func (s *Store) FindChannel(ctx context.Context, name string) (*Channel, error) {
	// 手动绑定优先
	if bc, err := s.bindingChannel(ctx, name); err != nil {
		return nil, err
	} else if bc != nil {
		return bc, nil
	}
	all, err := s.AllChannels(ctx)
	if err != nil {
		return nil, err
	}
	var fallback *Channel
	for i := range all {
		c := &all[i]
		if c.Name == name {
			if fallback == nil || c.Priority < fallback.Priority ||
				(c.Priority == fallback.Priority && c.ID < fallback.ID) {
				fallback = c
			}
		}
	}
	if fallback != nil {
		return fallback, nil
	}
	for i := range all {
		if all[i].XmltvID == name {
			c := all[i]
			return &c, nil
		}
	}
	return nil, nil
}

// batchQueryProgrammes 分批以 IN 查询节目（每批 500 个 channel_id，
// 控制参数数量与 SQL 长度），通过 fn 流式回调，避免大结果集驻留内存。
// 结果为"与 [from, to) 相交"的全部节目：窗口内节目 + 从前一天跨入窗口的节目
// （如 23:50 开始、次日凌晨结束），跨入部分先回调（start 最小），整体保持 start 升序。
// withDesc=false 时不取 descr TEXT 大列（timeline 等仅需概要的场景，显著降低传输量）。
func (s *Store) batchQueryProgrammes(ctx context.Context, channelIDs []int64,
	from, to time.Time, withDesc bool, fn func(Programme) error) error {
	const batch = 500
	// overnight 跨入窗口的节目 start 下限：正常节目单单档不超 24h，
	// 限制扫描范围以走 idx_start 索引
	const overnight = 24 * time.Hour
	cols := "channel_id, start, stop, title, sub_title, '' AS descr, category"
	if withDesc {
		cols = "channel_id, start, stop, title, sub_title, IFNULL(descr,''), category"
	}
	for start := 0; start < len(channelIDs); start += batch {
		end := start + batch
		if end > len(channelIDs) {
			end = len(channelIDs)
		}
		ids := channelIDs[start:end]
		placeholders := ""
		for i := range ids {
			if i > 0 {
				placeholders += ","
			}
			placeholders += "?"
		}
		// 1) 从前一天跨入窗口的节目（窗口起点正在播出，如 23:50~00:40）
		q := `SELECT ` + cols + `
		      FROM epg_programmes
		      WHERE start >= ? AND start < ? AND stop > ? AND channel_id IN (` + placeholders + `)
		      ORDER BY start, channel_id`
		args := make([]any, 0, len(ids)+3)
		args = append(args, from.Add(-overnight), from, from)
		for _, id := range ids {
			args = append(args, id)
		}
		if err := s.queryStream(ctx, q, args, fn); err != nil {
			return err
		}
		// 2) 窗口内开始的节目
		q = `SELECT ` + cols + `
		     FROM epg_programmes
		     WHERE start >= ? AND start < ? AND channel_id IN (` + placeholders + `)
		     ORDER BY start, channel_id`
		args = args[:0]
		args = append(args, from, to)
		for _, id := range ids {
			args = append(args, id)
		}
		if err := s.queryStream(ctx, q, args, fn); err != nil {
			return err
		}
	}
	return nil
}

// queryStream 执行查询并逐行扫描回调，统一 rows 生命周期管理。
func (s *Store) queryStream(ctx context.Context, q string, args []any,
	fn func(Programme) error) error {
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return err
	}
	for rows.Next() {
		var p Programme
		if err := rows.Scan(&p.ChannelID, &p.Start, &p.Stop, &p.Title,
			&p.SubTitle, &p.Descr, &p.Category); err != nil {
			rows.Close()
			return err
		}
		if err := fn(p); err != nil {
			rows.Close()
			return err
		}
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	return rows.Close()
}

// ProgrammesBetween 查询频道集合与 [from, to) 相交的节目（含从前一天跨入的），
// 按开始时间排序。
func (s *Store) ProgrammesBetween(ctx context.Context, channelIDs []int64,
	from, to time.Time, withDesc bool) ([]Programme, error) {
	var out []Programme
	err := s.batchQueryProgrammes(ctx, channelIDs, from, to, withDesc, func(p Programme) error {
		out = append(out, p)
		return nil
	})
	return out, err
}

// StreamProgrammesBetween 与 ProgrammesBetween 相同，但以回调流式返回，
// 用于全量 XML 导出等大数据量场景。
func (s *Store) StreamProgrammesBetween(ctx context.Context, channelIDs []int64,
	from, to time.Time, withDesc bool, fn func(Programme) error) error {
	return s.batchQueryProgrammes(ctx, channelIDs, from, to, withDesc, fn)
}

// AnyChannelName 返回 [from, to) 内有节目的任意一个频道名（按名排序取首个，
// 结果稳定）。供 /api/epg 无参请求兜底：与公版 DIYP 接口一致，无参时返回
// 有效数据而非错误。无任何节目时返回空串。
func (s *Store) AnyChannelName(ctx context.Context, from, to time.Time) (string, error) {
	const overnight = 24 * time.Hour // 与 batchQueryProgrammes 一致：跨天节目 start 下限，保证走 idx_start
	var name string
	err := s.db.QueryRowContext(ctx,
		`SELECT c.name FROM epg_channels c
		 JOIN epg_programmes p ON p.channel_id = c.id
		 WHERE p.start >= ? AND p.start < ? AND p.stop > ?
		 ORDER BY c.name LIMIT 1`,
		from.Add(-overnight), to, from).Scan(&name)
	if err == sql.ErrNoRows {
		return "", nil
	}
	return name, err
}

// ProgramCountByChannel 统计频道集合在 [from, to) 内各自的节目数（选源参考）。
func (s *Store) ProgramCountByChannel(ctx context.Context, channelIDs []int64,
	from, to time.Time) (map[int64]int, error) {
	out := map[int64]int{}
	const batch = 500
	for start := 0; start < len(channelIDs); start += batch {
		end := min(start+batch, len(channelIDs))
		ids := channelIDs[start:end]
		args := make([]any, 0, len(ids)+2)
		args = append(args, from, to)
		ph := ""
		for i, id := range ids {
			if i > 0 {
				ph += ","
			}
			ph += "?"
			args = append(args, id)
		}
		q := `SELECT channel_id, COUNT(*) FROM epg_programmes
		      WHERE start >= ? AND start < ? AND channel_id IN (` + ph + `)
		      GROUP BY channel_id`
		rows, err := s.db.QueryContext(ctx, q, args...)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var id int64
			var n int
			if err := rows.Scan(&id, &n); err != nil {
				rows.Close()
				return nil, err
			}
			out[id] = n
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// Stats 返回库存统计信息。
func (s *Store) Stats(ctx context.Context) (*Stats, error) {
	var st Stats
	err := s.db.QueryRowContext(ctx,
		`SELECT
		   (SELECT COUNT(*) FROM epg_channels),
		   (SELECT COUNT(*) FROM epg_programmes),
		   (SELECT MIN(start) FROM epg_programmes),
		   (SELECT MAX(stop)  FROM epg_programmes)`).
		Scan(&st.ChannelCount, &st.ProgramCount, &st.Earliest, &st.Latest)
	if err != nil && err != sql.ErrNoRows {
		return nil, err
	}
	return &st, nil
}
