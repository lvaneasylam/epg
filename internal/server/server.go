// Package server 提供 HTTP 服务：对外 EPG 接口、页面与运行状态接口。
package server

import (
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"log"
	"net"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"goepg/internal/cache"
	"goepg/internal/config"
	"goepg/internal/iconlib"
	"goepg/internal/store"
	"goepg/internal/syncer"
	"goepg/web"
)

// Server HTTP 服务，聚合各模块依赖。
type Server struct {
	cfg    *config.Config
	store  *store.Store
	syncer *syncer.Syncer
	cache  cache.Cache
	icons  *iconlib.Library
	loc    *time.Location

	// xmlMu 串行化全量 XML 生成（重操作），实现简单版 single-flight：
	// 并发请求等待锁后先做 ETag 命中检查，避免重复生成。
	xmlMu sync.Mutex

	// bindVer 绑定变更计数：与同步版本共同构成响应缓存键的数据版本，
	// 绑定/解绑后旧缓存立即失效，避免接口返回过期数据。
	bindVer atomic.Int64

	// iconBinds 频道图标手动绑定的内存表（key=归一化频道名），
	// /icon 热路径 O(1) 直查，写操作全量重载（见 loadIconBinds）。
	iconBindsMu sync.RWMutex
	iconBinds   map[string]iconBind

	// warmMu 热缓存预热执行互斥：手动触发（admin）与自动调度共用，
	// 防止并发预热重复打满数据库与 Redis。
	warmMu sync.Mutex

	httpServer *http.Server
}

// dataVersion 聚合数据版本（最近同步时间戳 + 绑定变更计数），
// 用于接口缓存键与全量 XML 的 ETag。
func (s *Server) dataVersion() string {
	return fmt.Sprintf("%d-%d", s.syncer.DataVersion(), s.bindVer.Load())
}

// New 创建 HTTP 服务并注册路由（Go 1.22+ 方法路由）。
func New(cfg *config.Config, st *store.Store, sy *syncer.Syncer, ca cache.Cache, loc *time.Location) (*Server, error) {
	s := &Server{cfg: cfg, store: st, syncer: sy, cache: ca, loc: loc}
	if err := s.initIconLib(); err != nil {
		return nil, err
	}
	s.loadIconBinds() // 频道图标手动绑定（失败告警不阻断启动）
	mux := http.NewServeMux()
	mux.HandleFunc("GET /{$}", s.handlePage)
	// 兼容 PHP 公版 EPG 接口路径：部分播放器配置模板固定请求 /index.php?ch=，
	// 直接按 DIYP 接口响应（浏览器不会访问该路径，无需 UA 分流）
	mux.HandleFunc("GET /index.php", s.handleEPG)
	mux.HandleFunc("GET /admin", s.requireAdmin(s.handleAdminPage))
	// 静态资源 no-cache：embed FS 无真实 mtime，FileServer 发不出
	// Last-Modified 验证器，浏览器将按启发式缓存长期持有旧版 CSS/JS，
	// 升级二进制后页面样式不更新（须每次回源协商）
	mux.Handle("GET /static/", noCacheStatic(http.StripPrefix("/static/",
		http.FileServer(http.FS(web.StaticFS())))))
	// 部分客户端（电视盒子浏览器等）不解析 HTML link 标签，固定请求根路径图标
	mux.HandleFunc("GET /favicon.ico", s.handleFavicon)
	mux.HandleFunc("GET /icon/{name}", s.handleIcon)
	mux.HandleFunc("GET /api/xml", s.handleXML)
	mux.HandleFunc("GET /api/epg", s.handleEPG)
	mux.HandleFunc("GET /api/timeline", s.handleTimeline)
	mux.HandleFunc("GET /api/status", s.handleStatus)
	mux.HandleFunc("POST /api/sync", s.handleSyncNow)

	// 管理 API（token 鉴权）
	admin := http.NewServeMux()
	admin.HandleFunc("GET /sources", s.adminListSources)
	admin.HandleFunc("POST /sources", s.adminCreateSource)
	admin.HandleFunc("PUT /sources/{id}", s.adminUpdateSource)
	admin.HandleFunc("DELETE /sources/{id}", s.adminDeleteSource)
	admin.HandleFunc("POST /sources/{id}/toggle", s.adminToggleSource)
	admin.HandleFunc("POST /sources/{id}/sync", s.adminSyncSource)
	admin.HandleFunc("GET /bindings", s.adminListBindings)
	admin.HandleFunc("GET /bindings/candidates", s.adminBindingCandidates)
	admin.HandleFunc("PUT /bindings", s.adminSetBinding)
	admin.HandleFunc("DELETE /bindings/{name}", s.adminDeleteBinding)
	admin.HandleFunc("PUT /hidden", s.adminSetHidden)
	admin.HandleFunc("POST /icons/refresh", s.adminRefreshIcons)
	admin.HandleFunc("GET /icons/tree", s.adminIconTree)
	admin.HandleFunc("GET /icons/raw", s.adminIconRaw)
	admin.HandleFunc("POST /icons/upload", s.adminIconUpload)
	admin.HandleFunc("POST /icons/fetch", s.adminIconFetch)
	admin.HandleFunc("POST /icons/move", s.adminIconMove)
	admin.HandleFunc("POST /icons/mkdir", s.adminIconMkdir)
	admin.HandleFunc("DELETE /icons/file", s.adminIconDelete)
	admin.HandleFunc("GET /icons/binds", s.adminIconBindList)
	admin.HandleFunc("PUT /icons/binds", s.adminIconBindSet)
	admin.HandleFunc("DELETE /icons/binds/{channel}", s.adminIconBindDelete)
	admin.HandleFunc("POST /warm", s.adminWarmEPG)
	mux.Handle("/api/admin/", http.StripPrefix("/api/admin", s.requireAdminHandler(admin)))

	s.httpServer = &http.Server{
		Addr:              cfg.Server.Addr,
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
	}
	go s.watchEPGWarm() // 昨/今/明响应热缓存：启动即预热，同步/绑定变更/跨天自动刷新
	return s, nil
}

// adminLoginHTML 管理页未授权时输出的密码输入页，提交后以 ?token= 重新进入。
const adminLoginHTML = `<!DOCTYPE html><html lang="zh"><head><meta charset="utf-8">
<meta name="viewport" content="width=device-width,initial-scale=1"><title>EPG 管理 · 登录</title></head>
<body style="margin:0;display:flex;align-items:center;justify-content:center;height:100vh;
background:#f5f6f8;font-family:system-ui,-apple-system,sans-serif">
<form onsubmit="location.search='?token='+encodeURIComponent(this.p.value);return false"
 style="background:#fff;padding:28px 26px;border-radius:10px;box-shadow:0 4px 16px rgba(0,0,0,.08);text-align:center">
<div style="font-size:16px;font-weight:600;margin-bottom:4px">EPG 配置管理</div>
<div style="font-size:12px;color:#888;margin-bottom:18px">请输入管理密码</div>
<input type="password" name="p" autofocus placeholder="密码"
 style="width:220px;height:34px;border:1px solid #d9d9d9;border-radius:6px;padding:0 10px;font-size:14px;outline:none">
<button style="height:34px;margin-left:8px;padding:0 16px;border:0;border-radius:6px;
background:#1677ff;color:#fff;font-size:14px;cursor:pointer">进入</button>
<div style="font-size:12px;color:#c00;margin-top:12px">未授权：密码与配置文件 admin.token 不符</div>
</form></body></html>`

// requireAdmin 页面级鉴权：token 非空时要求 ?token= 或 Authorization。
func (s *Server) requireAdmin(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !s.checkAdminToken(r) {
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(adminLoginHTML))
			return
		}
		next(w, r)
	}
}

// requireAdminHandler 中间件形式的鉴权（包整个 admin mux）。
// 管理数据实时性强（上传/删除/绑定后立即重取），统一禁用浏览器缓存。
func (s *Server) requireAdminHandler(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		if !s.checkAdminToken(r) {
			writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "未授权"})
			return
		}
		next.ServeHTTP(w, r)
	})
}

// checkAdminToken 校验管理令牌；未配置 token 时直接放行（内网部署场景）。
func (s *Server) checkAdminToken(r *http.Request) bool {
	if s.cfg.Admin.Token == "" {
		return true
	}
	if r.URL.Query().Get("token") == s.cfg.Admin.Token {
		return true
	}
	return r.Header.Get("Authorization") == "Bearer "+s.cfg.Admin.Token
}

// ListenAndServe 阻塞启动 HTTP 服务。
func (s *Server) ListenAndServe() error {
	ln, err := newListener(s.httpServer.Addr)
	if err != nil {
		return err
	}
	log.Printf("[server] HTTP 服务已启动，监听 %s", ln.Addr())
	return s.httpServer.Serve(ln)
}

// newListener 按配置地址创建监听器。
// 通配地址显式使用 tcp4：Go 1.26 起 darwin 上 "tcp" 网络的通配监听会创建
// IPv6-only socket（0.0.0.0 显式地址同样如此），导致 IPv4 请求不可达。
func newListener(addr string) (net.Listener, error) {
	if host, port, err := net.SplitHostPort(addr); err == nil &&
		(host == "" || host == "0.0.0.0") {
		return net.Listen("tcp4", net.JoinHostPort("0.0.0.0", port))
	}
	return net.Listen("tcp", addr)
}

// Shutdown 优雅停机。
func (s *Server) Shutdown(ctx context.Context) error {
	return s.httpServer.Shutdown(ctx)
}

// tz 业务时区。
func (s *Server) tz() *time.Location { return s.loc }

// playerUAKeywords 播放器 User-Agent 标识（小写包含匹配）：命中则根路径 / 直接按
// DIYP 接口响应，播放器可将站点根地址直接配置为 EPG 接口（如 APTV 自动追加 ?ch=）。
var playerUAKeywords = []string{"aptv"}

// isPlayerUA 判断请求是否来自已知播放器（UA 嗅探）。
func isPlayerUA(ua string) bool {
	ua = strings.ToLower(ua)
	for _, k := range playerUAKeywords {
		if strings.Contains(ua, k) {
			return true
		}
	}
	return false
}

// handlePage 输出内嵌的节目单页面；播放器 UA（如 APTV）访问根路径时
// 直接转交 DIYP 接口处理（/?ch=频道名 即可获取节目单）。
func (s *Server) handlePage(w http.ResponseWriter, r *http.Request) {
	if isPlayerUA(r.UserAgent()) {
		s.handleEPG(w, r)
		return
	}
	data, err := web.IndexHTML()
	if err != nil {
		http.Error(w, "页面资源缺失", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	_, _ = w.Write(data)
}

// noCacheStatic 给静态资源响应加 no-cache：浏览器可缓存但使用前须回源
// 校验，保证二进制升级（内嵌资源已变）后客户端立即拿到新版。
func noCacheStatic(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-cache")
		h.ServeHTTP(w, r)
	})
}

// handleFavicon 从内嵌静态资源输出站点图标（图标极少变化，允许长缓存）。
func (s *Server) handleFavicon(w http.ResponseWriter, r *http.Request) {
	data, err := fs.ReadFile(web.StaticFS(), "favicon.ico")
	if err != nil {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "image/x-icon")
	w.Header().Set("Cache-Control", "public, max-age=604800")
	_, _ = w.Write(data)
}

// writeJSON 统一 JSON 输出（禁用浏览器嗅探）。
func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}
