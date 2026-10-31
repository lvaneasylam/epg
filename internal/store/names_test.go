package store

import "testing"

// 重命名计划：繁体行转换，撞名（简体行已存在）跳过，简体行不动。
func TestPlanRenames(t *testing.T) {
	cases := []struct {
		name  string
		in    []string
		want  map[string]string
	}{
		{
			"纯简体无需迁移",
			[]string{"CCTV-1", "湖南卫视"},
			map[string]string{},
		},
		{
			"繁体转换",
			[]string{"衛視中文台", "東森戲劇台"},
			map[string]string{"衛視中文台": "卫视中文台", "東森戲劇台": "东森戏剧台"},
		},
		{
			"繁简并存撞名：繁体行跳过（简体行保留）",
			[]string{"鳳凰衛視", "凤凰卫视"},
			map[string]string{},
		},
		{
			"简繁混合集合：仅繁体行转换（翡翠台三字简繁同形，不动）",
			[]string{"翡翠台", "無綫財經台", "TVB財經台"},
			map[string]string{"無綫財經台": "无线财经台", "TVB財經台": "TVB财经台"},
		},
		{
			"空表",
			[]string{},
			map[string]string{},
		},
		{
			// MySQL utf8mb4_general_ci 主键比较不区分大小写：
			// 「momo親子台」转简后与已有「MOMO亲子台」在 DB 层同键，须跳过
			"大小写撞名（ci collation）：跳过",
			[]string{"MOMO亲子台", "momo親子台", "momo綜合台"},
			map[string]string{"momo綜合台": "momo综合台"},
		},
	}
	for _, c := range cases {
		got := planRenames(c.in)
		if len(got) != len(c.want) {
			t.Errorf("%s: 计划 %v, want %v", c.name, got, c.want)
			continue
		}
		for k, v := range c.want {
			if got[k] != v {
				t.Errorf("%s: %q -> %q, got %q", c.name, k, v, got[k])
			}
		}
	}
}
