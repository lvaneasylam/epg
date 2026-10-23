package iconlib

import (
	"os"
	"path/filepath"
	"testing"
)

func TestNorm(t *testing.T) {
	cases := map[string]string{
		"CCTV-1":   "cctv1",
		"cctv 1":   "cctv1",
		"ＣＣＴＶ１":    "cctv1",
		"CCTV_1综合": "cctv1综合",
		"翡翠台":      "翡翠台",
		"湖南卫视":     "湖南卫视",
		"广东体育(4K)": "广东体育4k",
	}
	for in, want := range cases {
		if got := Norm(in); got != want {
			t.Errorf("Norm(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestSnapshot 验证快照与索引一致：含归一化键与相对路径，且为独立副本。
func TestSnapshot(t *testing.T) {
	dir := t.TempDir()
	_ = os.MkdirAll(filepath.Join(dir, "辽宁"), 0755)
	_ = os.WriteFile(filepath.Join(dir, "辽宁", "GTV游戏竞技.png"), []byte("x"), 0644)
	lib, err := New(dir)
	if err != nil {
		t.Fatal(err)
	}
	snap := lib.Snapshot()
	if len(snap) != 1 {
		t.Fatalf("快照应含 1 条，实际 %d", len(snap))
	}
	if p, ok := snap["gtv游戏竞技"]; !ok || p != filepath.Join("辽宁", "GTV游戏竞技.png") {
		t.Errorf("快照条目错误: %q %q", p, p)
	}
	snap["gtv游戏竞技"] = "篡改" // 改副本不应影响库内索引
	if got, ok := lib.Match("GTV游戏竞技"); !ok || got == "篡改" {
		t.Errorf("快照应为独立副本，Match 受到影响: %q", got)
	}
}

func TestMatch(t *testing.T) {
	dir := t.TempDir()
	for _, g := range []string{"央视", "广东", "港澳"} {
		_ = os.MkdirAll(filepath.Join(dir, g), 0755)
	}
	files := map[string]string{
		"央视/CCTV-1.png": "",
		"央视/CCTV5+.png": "",
		"广东/广东体育.png":   "",
		"港澳/翡翠台.png":    "",
		"港澳/翡翠台HD.jpg":  "",
	}
	for p := range files {
		full := filepath.Join(dir, p)
		_ = os.WriteFile(full, []byte("x"), 0644)
	}
	lib, err := New(dir)
	if err != nil {
		t.Fatal(err)
	}
	cases := map[string]string{
		"CCTV-1": "央视/CCTV-1.png", // 精确
		"cctv1":  "央视/CCTV-1.png", // 归一化
		"ＣＣＴＶ－１": "央视/CCTV-1.png", // 全角
		"CCTV5+": "央视/CCTV5+.png", // 归一化
		"广东体育频道": "广东/广东体育.png",   // 去后缀词
		"翡翠台":    "港澳/翡翠台.png",    // 精确优先于包含
		"深圳卫视":   "",              // 无匹配
	}
	for name, want := range cases {
		got, ok := lib.Match(name)
		if want == "" {
			if ok {
				t.Errorf("Match(%q) 应未命中，实际 %s", name, got)
			}
			continue
		}
		if !ok {
			t.Errorf("Match(%q) 未命中", name)
			continue
		}
		rel, _ := filepath.Rel(dir, got)
		if rel != filepath.FromSlash(want) {
			t.Errorf("Match(%q) = %s, want %s", name, rel, want)
		}
	}
}
