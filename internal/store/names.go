// 频道名统一简体的存量配置迁移：hidden/bindings/icon-binds 三张表按
// channel_name 键控，频道名入库转简体后这些键需同步转换，否则用户配置
// （隐藏、手动绑定）对繁体名频道失效。启动时一次性执行，幂等。
package store

import (
	"context"
	"log"
	"strings"

	"goepg/internal/t2s"
)

// planRenames 计算需改名的行：old -> 转简体后的 new。
// 撞名（转换后的名字在现有集合中已存在）跳过，保留现有行——
// 两行语义相同，旧行留着无害（频道名已变，永远查不到，管理页可清）。
// 撞名比较按小写：表 collation 为 utf8mb4_general_ci，主键层
// 「MOMO亲子台」与「momo亲子台」同键，精确比较会漏判（实测踩坑）。
func planRenames(names []string) map[string]string {
	set := make(map[string]bool, len(names))
	for _, n := range names {
		set[strings.ToLower(n)] = true
	}
	plan := map[string]string{}
	for _, n := range names {
		nn := t2s.Convert(n)
		if nn != n && !set[strings.ToLower(nn)] {
			plan[n] = nn
		}
	}
	return plan
}

// MigrateNamesToSimplified 把三张配置表的繁体 channel_name 转为简体。
// 失败仅告警不阻断启动：迁移是增强项，表数据本身未损坏。
func (s *Store) MigrateNamesToSimplified(ctx context.Context) {
	tables := []string{"epg_hidden_channels", "epg_bindings", "epg_icon_bindings"}
	for _, table := range tables {
		rows, err := s.db.QueryContext(ctx, `SELECT channel_name FROM `+table)
		if err != nil {
			log.Printf("[store] 迁移 %s 读取失败: %v", table, err)
			return
		}
		var names []string
		for rows.Next() {
			var n string
			if err := rows.Scan(&n); err != nil {
				rows.Close()
				log.Printf("[store] 迁移 %s 读取失败: %v", table, err)
				return
			}
			names = append(names, n)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			log.Printf("[store] 迁移 %s 读取失败: %v", table, err)
			return
		}

		plan := planRenames(names)
		for old, nw := range plan {
			if _, err := s.db.ExecContext(ctx,
				`UPDATE `+table+` SET channel_name=? WHERE channel_name=?`, nw, old); err != nil {
				// 单条失败（如并发写入导致的意外撞名）跳过继续，不中止整表
				log.Printf("[store] 迁移 %s：%q -> %q 失败(跳过): %v", table, old, nw, err)
			}
		}
		if len(plan) > 0 {
			log.Printf("[store] %s 频道名已转简体 %d 条", table, len(plan))
		}
	}
}
