package server

import (
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"goepg/internal/config"
	"goepg/internal/iconlib"
)

// pngHead 最小 PNG magic bytes 头（构造测试图标文件用）。
var testPNG = append([]byte("\x89PNG\r\n\x1a\n"), 1, 2, 3, 4)

// newIconTestServer 构造仅含图标库相关依赖的 Server（不连 MySQL）。
func newIconTestServer(t *testing.T) *Server {
	t.Helper()
	dir := t.TempDir()
	return &Server{cfg: &config.Config{Icon: config.IconConfig{Dir: dir}}}
}

func TestSafeIconPath(t *testing.T) {
	s := newIconTestServer(t)
	root := s.cfg.Icon.Dir
	cases := []struct {
		in     string
		wantOK bool
	}{
		{"央视/CCTV-1.png", true},
		{"CCTV-1.png", true},
		{"a/b/c.png", true},
		{"", false},
		// 穿越写法经 Clean 后仍落在根目录内（不会逃出图标库）
		{"../../etc/passwd", true},
		{"../..", false}, // Clean("/") → 空
	}
	for _, c := range cases {
		abs, ok := s.safeIconPath(c.in)
		if ok != c.wantOK {
			t.Errorf("safeIconPath(%q) ok = %v, want %v", c.in, ok, c.wantOK)
			continue
		}
		if ok && !strings.HasPrefix(abs, root+string(filepath.Separator)) {
			t.Errorf("safeIconPath(%q) = %q 逃出图标目录 %q", c.in, abs, root)
		}
	}
}

// TestHandleIconBindPriority 手动绑定优先于自动匹配：
// 无绑定时「测试频道」无匹配文件 → 404；绑定后同请求返回绑定文件内容。
func TestHandleIconBindPriority(t *testing.T) {
	s := newIconTestServer(t)
	lib, err := iconlib.New(s.cfg.Icon.Dir)
	if err != nil {
		t.Fatal(err)
	}
	s.icons = lib
	s.iconBinds = map[string]iconBind{}
	// 干扰项：让自动匹配对该频道名必然失败
	if err := os.WriteFile(filepath.Join(s.cfg.Icon.Dir, "湖南卫视.png"), testPNG, 0644); err != nil {
		t.Fatal(err)
	}
	// 绑定目标：根目录散文件（自动匹配同样找不到它，因为文件名与频道名无关）
	if err := os.WriteFile(filepath.Join(s.cfg.Icon.Dir, "绑定图标.png"), testPNG, 0644); err != nil {
		t.Fatal(err)
	}
	lib.Refresh()

	get := func(name string) *httptest.ResponseRecorder {
		req := httptest.NewRequest("GET", "/icon/"+name, nil)
		req.SetPathValue("name", name) // 直接调 handler 需手动注入路径参数
		w := httptest.NewRecorder()
		s.handleIcon(w, req)
		return w
	}

	if w := get("测试频道.png"); w.Code != 404 {
		t.Fatalf("无绑定时应 404，实际 %d", w.Code)
	}
	s.iconBindsMu.Lock()
	s.iconBinds[iconlib.Norm("测试频道")] = iconBind{Channel: "测试频道", Path: "绑定图标.png"}
	s.iconBindsMu.Unlock()
	w := get("测试频道.png")
	if w.Code != 200 {
		t.Fatalf("绑定后应 200，实际 %d", w.Code)
	}
	if !strings.HasPrefix(w.Body.String(), "\x89PNG") {
		t.Errorf("应返回绑定文件内容，实际 %q", w.Body.String())
	}
	// 自动匹配不受影响
	if w := get("湖南卫视"); w.Code != 200 {
		t.Errorf("自动匹配应保持命中，实际 %d", w.Code)
	}
}

func TestAdminIconTree(t *testing.T) {
	s := newIconTestServer(t)
	for _, p := range []string{"央视/CCTV-1.png", "央视/CCTV5.png", "其他/翡翠台.webp", "空目录"} {
		full := filepath.Join(s.cfg.Icon.Dir, filepath.FromSlash(p))
		if err := os.MkdirAll(filepath.Dir(full), 0755); err != nil {
			t.Fatal(err)
		}
		if !strings.HasSuffix(p, "/") {
			if err := os.WriteFile(full, testPNG, 0644); err != nil {
				t.Fatal(err)
			}
		}
	}
	// 非图片文件应被忽略
	if err := os.WriteFile(filepath.Join(s.cfg.Icon.Dir, "央视", "README.txt"), []byte("x"), 0644); err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest("GET", "/api/admin/icons/tree", nil)
	w := httptest.NewRecorder()
	s.adminIconTree(w, req)
	body := w.Body.String()
	for _, want := range []string{`"name":"央视"`, `"count":2`, `"name":"其他"`, `"count":1`, `CCTV-1.png`} {
		if !strings.Contains(body, want) {
			t.Errorf("tree 响应缺少 %q: %s", want, body)
		}
	}
	if strings.Contains(body, "README.txt") {
		t.Error("非图片文件不应出现在目录树")
	}
	if strings.Contains(body, `"count":0`) && strings.Contains(body, "空目录") {
		// 空目录本身应保留（可在其中上传），仅校验其存在
		if !strings.Contains(body, `"name":"空目录"`) {
			t.Error("空目录应保留在树中")
		}
	}
}
