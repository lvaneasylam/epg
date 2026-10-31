// Package iconlib 提供本地频道图标（台标）库：按地域子目录组织文件，
// 对外统一做归一化模糊匹配，与图标所在目录无关。
package iconlib

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// defaultGroups 预建的地域分类目录，仅用于人工归档管理；
// 匹配时递归全量索引，图标放错目录不影响命中。
var defaultGroups = []string{
	"央视", "卫视", "北京", "上海", "广东", "浙江", "江苏", "湖南",
	"港澳", "台湾", "国际", "网络", "其他",
}

// GroupOrder 预建分类目录顺序（供目录树按业务顺序优先排序展示）。
func GroupOrder() []string { return defaultGroups }

// Library 图标库索引。
type Library struct {
	dir string

	mu       sync.RWMutex
	index    map[string]string // norm(文件名去扩展) -> 文件相对路径
	rootMod  map[string]os.FileInfo
	lastScan time.Time
}

// New 创建图标库：确保目录与分类子目录存在并建立初始索引。
func New(dir string) (*Library, error) {
	if err := os.MkdirAll(dir, 0755); err != nil {
		return nil, err
	}
	readme := filepath.Join(dir, "README.txt")
	if _, err := os.Stat(readme); os.IsNotExist(err) {
		_ = os.WriteFile(readme, []byte(
			"频道图标库：把 png/jpg/webp 图片按地域放入对应子目录，文件名即频道名。\n"+
				"目录仅用于分类管理，匹配时全目录递归生效（例：CCTV-1.png、翡翠台.png）。\n"+
				"匹配规则：忽略大小写/空格/横杠等符号差异，支持常见后缀词（台、频道、高清、HD）省略。\n"), 0644)
	}
	for _, g := range defaultGroups {
		_ = os.MkdirAll(filepath.Join(dir, g), 0755)
	}
	l := &Library{dir: dir, index: map[string]string{}, rootMod: map[string]os.FileInfo{}}
	l.rescan()
	return l, nil
}

// Match 按频道名模糊匹配图标，返回文件绝对路径。
// 三级匹配：归一化全等 → 去常见后缀词后相等 → 包含匹配（取最长命中）。
func (l *Library) Match(name string) (string, bool) {
	l.mu.RLock()
	if time.Since(l.lastScan) > 30*time.Second { // 周期检测目录变化，支持热拷贝
		l.mu.RUnlock()
		l.Refresh()
		l.mu.RLock()
	}
	idx := l.index
	l.mu.RUnlock()
	if len(idx) == 0 {
		return "", false
	}
	n := Norm(name)
	if n == "" {
		return "", false
	}
	// 一级：归一化全等
	if p, ok := idx[n]; ok {
		return filepath.Join(l.dir, p), true
	}
	// 二级：去常见后缀词
	nt := stripTail(n)
	if nt != "" && nt != n {
		if p, ok := idx[nt]; ok {
			return filepath.Join(l.dir, p), true
		}
	}
	// 三级：包含匹配，取最长的文件名（信息量最大者）
	best, bestLen := "", 0
	for k, p := range idx {
		if (strings.Contains(n, k) || strings.Contains(k, n)) && len(k) > bestLen {
			if k == "" {
				continue
			}
			best, bestLen = p, len(k)
		}
	}
	if best != "" {
		return filepath.Join(l.dir, best), true
	}
	return "", false
}

// Snapshot 返回当前索引快照（归一化名 → 图标相对路径），
// 供启动时发布到 Redis 等共享存储，外部工具可直接按名查路径。
func (l *Library) Snapshot() map[string]string {
	l.mu.RLock()
	defer l.mu.RUnlock()
	out := make(map[string]string, len(l.index))
	for k, v := range l.index {
		out[k] = v
	}
	return out
}

// Refresh 重建索引（目录有变化时才真正重扫）。
func (l *Library) Refresh() {
	l.mu.Lock()
	defer l.mu.Unlock()
	if !l.dirChanged() && l.index != nil {
		l.lastScan = time.Now()
		return
	}
	l.rescanLocked()
}

func (l *Library) rescan() {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.rescanLocked()
}

func (l *Library) rescanLocked() {
	idx := map[string]string{}
	_ = filepath.Walk(l.dir, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return nil
		}
		ext := strings.ToLower(filepath.Ext(path))
		switch ext {
		case ".png", ".jpg", ".jpeg", ".webp", ".gif":
		default:
			return nil
		}
		base := strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
		key := Norm(base)
		if key == "" {
			return nil
		}
		rel, _ := filepath.Rel(l.dir, path)
		if _, exists := idx[key]; !exists { // 同名优先先扫描到的
			idx[key] = rel
		}
		return nil
	})
	l.index = idx
	l.lastScan = time.Now()
	l.dirChanged() // 更新 mod 快照
}

// dirChanged 比较根目录与一级子目录的 mtime，检测增删文件。
func (l *Library) dirChanged() bool {
	changed := false
	entries, _ := os.ReadDir(l.dir)
	snapshot := map[string]os.FileInfo{}
	check := func(name string) {
		fi, err := os.Stat(filepath.Join(l.dir, name))
		if err != nil {
			return
		}
		snapshot[name] = fi
		if old, ok := l.rootMod[name]; !ok || old.ModTime() != fi.ModTime() {
			changed = true
		}
	}
	check(".")
	for _, e := range entries {
		if e.IsDir() {
			check(e.Name())
		}
	}
	l.rootMod = snapshot
	return changed
}

// tailWords 归一化后可省略的常见后缀词（长词在前避免残留）。
var tailWords = []string{
	"电视台", "电视频道", "频道", "超清", "高清", "蓝光", "标准",
	"hd", "fhd", "uhd", "4k", "8k", "台",
}

// stripTail 依次剥离常见后缀词。
func stripTail(n string) string {
	for _, w := range tailWords {
		if strings.HasSuffix(n, w) && len(n) > len(w) {
			return strings.TrimSuffix(n, w)
		}
	}
	return n
}

// Norm 频道名归一化：小写、全角转半角、去空白与全部中英文标点符号。
// CCTV-1 / cctv1 / ＣＣＴＶ１ / CCTV 1 全部归一为 cctv1。
func Norm(s string) string {
	s = strings.ToLower(s)
	var b strings.Builder
	for _, r := range s {
		switch {
		case r == ' ' || r == '\t' || r == '　':
			continue
		case r >= '！' && r <= '～': // 全角 ASCII → 半角
			r -= '！' - '!'
		}
		if isSymbol(r) {
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}

func isSymbol(r rune) bool {
	switch {
	case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
		return false
	case r >= 0x4e00 && r <= 0x9fff: // CJK 基本（含简繁）
		return false
	case r >= 0x3040 && r <= 0x30ff: // 假名
		return false
	case r >= 0xac00 && r <= 0xd7af: // 谚文
		return false
	case r >= 0xc0 && r <= 0x24f: // 拉丁扩展（é ü 等）
		return false
	case r >= 'A' && r <= 'Z':
		return false
	}
	return true
}
