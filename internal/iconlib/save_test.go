package iconlib

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// 各格式最小合法 magic bytes 头
var (
	pngHead  = append([]byte("\x89PNG\r\n\x1a\n"), 0, 0, 0, 13)
	jpgHead  = []byte{0xFF, 0xD8, 0xFF, 0xE0, 0, 16, 'J', 'F', 'I', 'F'}
	webpHead = append([]byte("RIFF"), 4, 0, 0, 0, 'W', 'E', 'B', 'P', ' ', 'V', 'P')
	gifHead  = []byte("GIF89a\x01\x00\x01\x00")
)

func TestDetectImageType(t *testing.T) {
	cases := []struct {
		data []byte
		want string
	}{
		{pngHead, ".png"},
		{jpgHead, ".jpg"},
		{webpHead, ".webp"},
		{gifHead, ".gif"},
		{[]byte("not an image"), ""},
		{nil, ""},
		{pngHead[:4], ""}, // 截断的头
	}
	for _, c := range cases {
		if got := DetectImageType(c.data); got != c.want {
			t.Errorf("DetectImageType(%q) = %q, want %q", c.data, got, c.want)
		}
	}
}

func TestCleanName(t *testing.T) {
	ok := map[string]string{
		"  CCTV-1 ": "CCTV-1",
		"湖南卫视":      "湖南卫视",
	}
	for in, want := range ok {
		if got := CleanName(in); got != want {
			t.Errorf("CleanName(%q) = %q, want %q", in, got, want)
		}
	}
	bad := []string{"", "  ", "a/b", "a\\b", ".hidden", "..", "\x00x", "x\x7fy", strings.Repeat("长", 129)}
	for _, in := range bad {
		if got := CleanName(in); got != "" {
			t.Errorf("CleanName(%q) 应拒绝，实际 %q", in, got)
		}
	}
}

func TestSaveIcon(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "央视"), 0755); err != nil {
		t.Fatal(err)
	}

	// 正常保存：名字自带扩展名时剥离避免 .png.png，扩展名按真实格式推断
	rel, replaced, err := SaveIcon(root, "央视", "CCTV-1.png", pngHead)
	if err != nil || replaced || rel != "央视/CCTV-1.png" {
		t.Fatalf("SaveIcon = %q, %v, %v", rel, replaced, err)
	}
	if _, err := os.Stat(filepath.Join(root, "央视", "CCTV-1.png")); err != nil {
		t.Fatal("文件未落盘:", err)
	}

	// 同目标再次保存 → 覆盖（目录名/图标名允许带空白，落盘前清洗）
	_, replaced, err = SaveIcon(root, " 央视 ", "CCTV-1 ", pngHead)
	if err != nil || !replaced {
		t.Fatalf("覆盖保存 replaced = %v, err = %v", replaced, err)
	}

	// 拒绝：坏格式 / 目录不存在 / 非法名 / 超大小
	if _, _, err := SaveIcon(root, "央视", "x", []byte("junk")); err == nil {
		t.Error("非图片数据应被拒绝")
	}
	if _, _, err := SaveIcon(root, "不存在的目录", "x", pngHead); err == nil {
		t.Error("目录不存在应被拒绝")
	}
	if _, _, err := SaveIcon(root, "央视", "../逃逸", pngHead); err == nil {
		t.Error("非法名应被拒绝")
	}
	if _, _, err := SaveIcon(root, "../", "x", pngHead); err == nil {
		t.Error("非法目录应被拒绝")
	}
	big := make([]byte, MaxIconSize+1)
	copy(big, pngHead)
	if _, _, err := SaveIcon(root, "央视", "big", big); err == nil {
		t.Error("超过大小上限应被拒绝")
	}
}
