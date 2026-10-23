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

// handleIcon GET /icon/{频道名}[.png]
// 模糊匹配本地图标库（跨目录），命中输出图片文件；供播放器直连或 /api/epg 的 icon 字段引用。
func (s *Server) handleIcon(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	// 兼容 .png/.jpg 等后缀写法：仅作占位美观，匹配按去后缀文件名
	name = strings.TrimSuffix(name, path.Ext(name))
	if name == "" {
		http.NotFound(w, r)
		return
	}
	file, ok := s.icons.Match(name)
	if !ok {
		http.NotFound(w, r)
		return
	}
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
	// 长缓存：图标不变，匹配变更靠文件名维度
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
		scheme := "http"
		if r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https" {
			scheme = "https"
		}
		base = scheme + "://" + r.Host
	}
	return strings.TrimSuffix(base, "/") + "/icon/" + url.PathEscape(channelName) + ".png"
}
