package server

import (
	"context"
	"log"
	"net/http"
	"net/url"
	"os"
	"path"
	"strings"

	"goepg/internal/iconlib"
)

// initIconLib 创建图标库实例（目录自动初始化地域分类子目录），
// 并把索引发布到 Redis，供 redis-cli / 其他进程按名直查图标路径。
func (s *Server) initIconLib() error {
	lib, err := iconlib.New(s.cfg.Icon.Dir)
	if err != nil {
		return err
	}
	s.icons = lib
	s.publishIconIndex()
	return nil
}

// iconIdxKey Redis 图标索引 hash 键：field=归一化频道名，value=图标相对路径。
const iconIdxKey = "icons:idx"

// publishIconIndex 整表发布索引快照到 Redis。
// /icon 热路径仍走进程内存索引（无网络开销）；此 hash 仅作共享只读副本，
// 失败只告警不影响服务。
func (s *Server) publishIconIndex() {
	snap := s.icons.Snapshot()
	if ok := s.cache.HSetAll(context.Background(), iconIdxKey, snap); ok && s.cfg.Redis.Addr != "" {
		log.Printf("[icons] 索引已发布到 Redis %s: %d 条", iconIdxKey, len(snap))
	}
}

// iconBind 频道图标绑定的内存视图：原频道名为 MySQL 主键（清理脏绑定时
// 需要反查删除），Path 为相对图标库根的斜杠路径（与库内一致）。
type iconBind struct {
	Channel string
	Path    string
}

// loadIconBinds 全量加载绑定到内存（启动与绑定变更后调用）。
// 失败只告警：内存表留空/留旧值，/icon 自动匹配兜底。
func (s *Server) loadIconBinds() {
	rows, err := s.store.ListIconBinds(context.Background())
	if err != nil {
		log.Printf("[icons] 加载图标绑定失败: %v", err)
		return
	}
	m := make(map[string]iconBind, len(rows))
	for _, row := range rows {
		m[iconlib.Norm(row.ChannelName)] = iconBind{Channel: row.ChannelName, Path: row.IconPath}
	}
	s.iconBindsMu.Lock()
	s.iconBinds = m
	s.iconBindsMu.Unlock()
}

// boundIcon 按归一化频道名查手动绑定。
func (s *Server) boundIcon(name string) (iconBind, bool) {
	s.iconBindsMu.RLock()
	defer s.iconBindsMu.RUnlock()
	b, ok := s.iconBinds[iconlib.Norm(name)]
	return b, ok
}

// dropIconBind 清理单条绑定（DB + 内存）。
func (s *Server) dropIconBind(b iconBind) {
	if err := s.store.DeleteIconBind(context.Background(), b.Channel); err != nil {
		log.Printf("[icons] 清理脏绑定失败: %v", err)
	}
	s.loadIconBinds()
}

// handleIcon GET /icon/{频道名}[.png]
// 匹配顺序：手动绑定 → 三级模糊匹配（跨目录），命中输出图片文件；
// 供播放器直连或 /api/epg 的 icon 字段引用。
func (s *Server) handleIcon(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	// 兼容 .png/.jpg 等后缀写法：仅作占位美观，匹配按去后缀文件名
	name = strings.TrimSuffix(name, path.Ext(name))
	if name == "" {
		http.NotFound(w, r)
		return
	}
	// 一级：手动绑定优先；绑定的文件已被删则清掉脏绑定，回退自动匹配
	if b, ok := s.boundIcon(name); ok {
		if abs, valid := s.safeIconPath(b.Path); valid {
			if _, err := os.Stat(abs); err == nil {
				serveIconFile(w, r, abs)
				return
			}
		}
		s.dropIconBind(b)
	}
	file, ok := s.icons.Match(name)
	if !ok {
		http.NotFound(w, r)
		return
	}
	serveIconFile(w, r, file)
}

// serveIconFile 输出图标文件。长缓存：图标默认不变，替换后客户端
// 最长 1 天过期重取（Last-Modified 变化即拿到新图）。
func serveIconFile(w http.ResponseWriter, r *http.Request, file string) {
	f, err := os.Open(file)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Cache-Control", "public, max-age=86400")
	w.Header().Set("Content-Type", contentTypeByExt(file))
	http.ServeContent(w, r, "", fi.ModTime(), f)
}

func contentTypeByExt(p string) string {
	switch strings.ToLower(path.Ext(p)) {
	case ".png":
		return "image/png"
	case ".jpg", ".jpeg":
		return "image/jpeg"
	case ".webp":
		return "image/webp"
	case ".gif":
		return "image/gif"
	default:
		return "application/octet-stream"
	}
}

// iconURL 生成对外图标地址：显式 base_url 优先，否则按请求 Host 动态生成
// （反代/换域名后自动跟随，无需改配置）。
func (s *Server) iconURL(r *http.Request, channelName string) string {
	base := s.cfg.Icon.BaseURL
	if base == "" {
		base = baseFromRequest(r)
	}
	return iconJoin(base, channelName)
}

// baseFromRequest 按请求推断站点基地址：反代优先 X-Forwarded-Host（Caddy 等
// 默认携带）；Nginx 需显式 proxy_set_header Host $host 才能让 r.Host 为原始域名。
func baseFromRequest(r *http.Request) string {
	scheme := "http"
	if r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https" {
		scheme = "https"
	}
	host := r.Header.Get("X-Forwarded-Host")
	if host == "" {
		host = r.Host
	}
	return scheme + "://" + host
}

// iconJoin 拼接图标地址（请求路径与预热共用：预热无请求上下文，直接用配置 base_url）。
func iconJoin(base, channelName string) string {
	return strings.TrimSuffix(base, "/") + "/icon/" + url.PathEscape(channelName) + ".png"
}
