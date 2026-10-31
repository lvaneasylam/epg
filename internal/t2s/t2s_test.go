package t2s

import "testing"

// 频道名场景的繁→简转换：字级映射，简体/英文/数字原样。
func TestConvert(t *testing.T) {
	cases := []struct{ in, want string }{
		{"", ""},
		{"CCTV-5", "CCTV-5"},                     // 纯英文数字不动
		{"纯简体频道", "纯简体频道"},                 // 简体不动
		{"衛視中文台", "卫视中文台"},                 // 港台频道名
		{"鳳凰衛視中文台", "凤凰卫视中文台"},           //
		{"澳洲公廣頻道", "澳洲公广频道"},              //
		{"東森戲劇台", "东森戏剧台"},                 //
		{"ARIRANG阿里郎頻道", "ARIRANG阿里郎频道"},    // 繁简混合
		{"澳門綜藝97", "澳门综艺97"},                // 带数字
		{"CATCHPLAY電影台", "CATCHPLAY电影台"},      // 带英文
		{"無綫財經資訊台", "无线财经资讯台"},            //
		{"戲曲頻道·經典", "戏曲频道·经典"},             // 间隔符
	}
	for _, c := range cases {
		if got := Convert(c.in); got != c.want {
			t.Errorf("Convert(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// 映射表自身健康度：键值均为有效汉字（map[rune]rune 天然单字，校验非零值）。
func TestTableValid(t *testing.T) {
	for k, v := range table {
		if k == 0 || v == 0 {
			t.Fatalf("零值映射: %U -> %U", k, v)
		}
	}
	if len(table) < 3000 { // 字级覆盖面下限，防生成脚本缺字
		t.Fatalf("映射表过小: %d 对", len(table))
	}
}

// 性能无关正确性，但确保不 panic 于任意输入（含扩展区汉字）。
func TestConvertNoPanic(t *testing.T) {
	_ = Convert("𠀀𠀁 ext-B")
}
