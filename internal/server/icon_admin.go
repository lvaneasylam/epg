// 频道图标库管理 API（/api/admin/icons/*，token 鉴权）：
// 目录树 / 上传 / URL 导入 / 新建目录 / 删除 / 缩略图输出 / 频道绑定。
package server

import (
	"encoding/json"
	"io"
	"log"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"goepg/internal/iconlib"
)

// maxFetchSize 上传/下载单次读取上限（读满即截断，SaveIcon 再按 2MB 精确校验）。
const maxFetchSize = iconlib.MaxIconSize + 1

// iconFileView 目录内单个图标文件。
type iconFileView struct {
	Name string `json:"name"` // 文件名（含扩展名）
	Path string `json:"path"` // 相对图标库根的路径（斜杠分隔）
	Mod  int64  `json:"mod"`  // mtime Unix 秒，前端拼 ?v= 绕浏览器缓存
}

// iconDirView 一级分类目录（含全部文件，一次全量返回，前端切目录零延迟）。
type iconDirView struct {
	Name  string         `json:"name"`
	Count int            `json:"count"`
	Files []iconFileView `json:"files"`
}

// adminIconTree GET /api/admin/icons/tree 目录树 + 每目录文件清单。
func (s *Server) adminIconTree(w http.ResponseWriter, r *http.Request) {
	entries, err := os.ReadDir(s.cfg.Icon.Dir)
	if err != nil {
		http.Error(w, "图标目录读取失败", http.StatusInternalServerError)
		return
	}
	out := make([]iconDirView, 0, len(entries))
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		files, err := listIconFiles(s.cfg.Icon.Dir, e.Name())
		if err != nil {
			http.Error(w, "图标目录读取失败", http.StatusInternalServerError)
			return
		}
		out = append(out, iconDirView{Name: e.Name(), Count: len(files), Files: files})
	}
	sortDirViews(out)
	writeJSON(w, http.StatusOK, out)
}

// sortDirViews 目录排序：预建分类（央视/卫视/省份…）按业务顺序优先，
// 用户新建目录排其后按名称，找目录更顺手。
func sortDirViews(dirs []iconDirView) {
	order := iconlib.GroupOrder()
	idx := make(map[string]int, len(order))
	for i, g := range order {
		idx[g] = i
	}
	sort.SliceStable(dirs, func(i, j int) bool {
		oi, oki := idx[dirs[i].Name]
		oj, okj := idx[dirs[j].Name]
		if oki && okj {
			return oi < oj
		}
		if oki != okj {
			return oki // 预建目录在前
		}
		return dirs[i].Name < dirs[j].Name
	})
}

// listIconFiles 列出某一级子目录下的全部图标文件（按扩展名白名单过滤）。
func listIconFiles(root, dir string) ([]iconFileView, error) {
	entries, err := os.ReadDir(filepath.Join(root, dir))
	if err != nil {
		return nil, err
	}
	out := make([]iconFileView, 0) // 空目录序列化为 [] 而非 null（前端直接 .filter）
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		switch strings.ToLower(path.Ext(e.Name())) {
		case ".png", ".jpg", ".jpeg", ".webp", ".gif":
		default:
			continue
		}
		fi, err := e.Info()
		if err != nil {
			continue
		}
		out = append(out, iconFileView{
			Name: e.Name(), Path: dir + "/" + e.Name(), Mod: fi.ModTime().Unix(),
		})
	}
	return out, nil
}

// safeIconPath 校验相对路径（斜杠分隔）位于图标库目录内（防穿越），返回绝对路径。
func (s *Server) safeIconPath(rel string) (string, bool) {
	rel = strings.TrimPrefix(path.Clean("/"+rel), "/") // Clean 消灭 ../ 等写法
	if rel == "" {
		return "", false
	}
	abs := filepath.Join(s.cfg.Icon.Dir, filepath.FromSlash(rel))
	if !strings.HasPrefix(abs, s.cfg.Icon.Dir+string(filepath.Separator)) {
		return "", false
	}
	return abs, true
}

// adminIconRaw GET /api/admin/icons/raw?path= 缩略图输出（带鉴权，避免公开目录枚举）。
func (s *Server) adminIconRaw(w http.ResponseWriter, r *http.Request) {
	abs, ok := s.safeIconPath(r.URL.Query().Get("path"))
	if !ok {
		http.NotFound(w, r)
		return
	}
	// 管理页需即时看到替换效果，禁缓存（时间戳参数另可复用）
	w.Header().Set("Cache-Control", "no-store")
	http.ServeFile(w, r, abs)
}

// adminIconUpload POST /api/admin/icons/upload  multipart: dir、name、file
// 拖拽/选文件上传，同目录同名直接覆盖。
func (s *Server) adminIconUpload(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseMultipartForm(maxFetchSize); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "上传内容过大或格式错误"})
		return
	}
	f, _, err := r.FormFile("file")
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "缺少文件"})
		return
	}
	defer f.Close()
	s.saveIcon(w, r, r.FormValue("dir"), r.FormValue("name"), r.FormValue("old_path"),
		io.LimitReader(f, maxFetchSize))
}

// iconFetchPayload URL 导入请求体。
type iconFetchPayload struct {
	Dir     string `json:"dir"`
	Name    string `json:"name"`
	URL     string `json:"url"`
	OldPath string `json:"old_path"` // 替换模式：旧文件路径，扩展名变化时删除旧文件并迁移绑定
}

// adminIconFetch POST /api/admin/icons/fetch  body: {dir,name,url}
// 服务端下载远端图片落盘（可用于替换本地图标）。
func (s *Server) adminIconFetch(w http.ResponseWriter, r *http.Request) {
	var p iconFetchPayload
	if err := decodeJSONBody(r, &p); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "请求体格式错误"})
		return
	}
	if !strings.HasPrefix(p.URL, "http://") && !strings.HasPrefix(p.URL, "https://") {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "地址仅支持 http/https"})
		return
	}
	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Get(p.URL)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": "下载失败：" + err.Error()})
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": "下载失败：HTTP " + resp.Status})
		return
	}
	s.saveIcon(w, r, p.Dir, p.Name, p.OldPath, io.LimitReader(resp.Body, maxFetchSize))
}

// saveIcon 上传/下载共用：读尽数据流，校验落盘并刷新索引发布。
// oldPath 非空表示替换模式：新图扩展名与旧图不同时，删除旧文件并把
// 指向旧文件的频道绑定迁移到新文件。
func (s *Server) saveIcon(w http.ResponseWriter, r *http.Request, dir, name, oldPath string, src io.Reader) {
	data, err := io.ReadAll(src)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "读取数据失败"})
		return
	}
	rel, replaced, err := iconlib.SaveIcon(s.cfg.Icon.Dir, dir, name, data)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	if oldPath != "" && oldPath != rel {
		if oldAbs, ok := s.safeIconPath(oldPath); ok {
			if err := s.store.UpdateIconBindPath(r.Context(), oldPath, rel); err != nil {
				log.Printf("[icons] 绑定迁移失败 %s → %s: %v", oldPath, rel, err)
			}
			if err := os.Remove(oldAbs); err != nil && !os.IsNotExist(err) {
				log.Printf("[icons] 旧图标删除失败 %s: %v", oldAbs, err)
			}
			s.loadIconBinds()
			replaced = true
		}
	}
	s.afterIconChange()
	msg := "已保存"
	if replaced {
		msg = "已保存（替换了原有图标）"
	}
	writeJSON(w, http.StatusOK, map[string]string{"path": rel, "message": msg})
}

// iconMovePayload 图标移动请求体。
type iconMovePayload struct {
	Path string `json:"path"` // 源文件相对路径（斜杠分隔）
	Dir  string `json:"dir"`  // 目标分组目录名
}

// adminIconMove POST /api/admin/icons/move  body: {path,dir}
// 把图标移动到另一分组目录（拖拽整理错误分组）。目标同名图标按
// 上传同规则覆盖，源/被覆盖文件上的频道绑定迁移到新路径。
func (s *Server) adminIconMove(w http.ResponseWriter, r *http.Request) {
	var p iconMovePayload
	if err := decodeJSONBody(r, &p); err != nil || p.Path == "" || p.Dir == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "参数不完整"})
		return
	}
	rel := path.Clean(p.Path)
	if _, ok := s.safeIconPath(rel); !ok {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "路径不合法"})
		return
	}
	newRel, coveredRel, err := iconlib.MoveIcon(s.cfg.Icon.Dir, rel, p.Dir)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	// 源路径与被覆盖文件路径上的绑定都改指新路径；失败仅告警
	// （脏绑定在 /icon 命中时自愈回退自动匹配，与替换模式同款容错）
	for _, old := range []string{rel, coveredRel} {
		if old == "" || old == newRel {
			continue
		}
		if err := s.store.UpdateIconBindPath(r.Context(), old, newRel); err != nil {
			log.Printf("[icons] 绑定迁移失败 %s → %s: %v", old, newRel, err)
		}
	}
	s.loadIconBinds()
	s.afterIconChange()
	msg := "已移动到 " + p.Dir
	if coveredRel != "" {
		msg += "（覆盖了同名图标）"
	}
	writeJSON(w, http.StatusOK, map[string]string{"path": newRel, "message": msg})
}

// adminIconMkdir POST /api/admin/icons/mkdir  body: {"name":"新分类"}
// 新建一级子目录（如新增省份）。
func (s *Server) adminIconMkdir(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Name string `json:"name"`
	}
	if err := decodeJSONBody(r, &body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "请求体格式错误"})
		return
	}
	name := iconlib.CleanName(body.Name)
	if name == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "目录名不合法"})
		return
	}
	if fi, err := os.Stat(filepath.Join(s.cfg.Icon.Dir, name)); err == nil && fi.IsDir() {
		writeJSON(w, http.StatusConflict, map[string]string{"error": "目录已存在"})
		return
	}
	if err := os.MkdirAll(filepath.Join(s.cfg.Icon.Dir, name), 0755); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"message": "已创建目录 " + name})
}

// adminIconDelete DELETE /api/admin/icons/file?path= 删除图标文件，
// 并连带清理指向它的频道绑定。
func (s *Server) adminIconDelete(w http.ResponseWriter, r *http.Request) {
	rel := path.Clean(r.URL.Query().Get("path"))
	abs, ok := s.safeIconPath(rel)
	if !ok {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "路径不合法"})
		return
	}
	if _, err := os.Stat(abs); err != nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "文件不存在"})
		return
	}
	if err := os.Remove(abs); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	// 连带清理绑定；失败仅告警（残留脏绑定会在 /icon 命中时自愈清理）
	if err := s.store.DeleteIconBindsByPath(r.Context(), rel); err != nil {
		log.Printf("[icons] 清理绑定失败: %v", err)
	}
	s.loadIconBinds()
	s.afterIconChange()
	writeJSON(w, http.StatusOK, map[string]string{"message": "已删除"})
}

// adminIconBindList GET /api/admin/icons/binds 全部频道图标绑定。
func (s *Server) adminIconBindList(w http.ResponseWriter, r *http.Request) {
	rows, err := s.store.ListIconBinds(r.Context())
	if err != nil {
		http.Error(w, "数据库查询失败", http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, rows)
}

// adminIconBindSet PUT /api/admin/icons/binds  body: {"channel_name","icon_path"}
// 把频道锁定到指定图标文件（优先于自动匹配）。
func (s *Server) adminIconBindSet(w http.ResponseWriter, r *http.Request) {
	var body struct {
		ChannelName string `json:"channel_name"`
		IconPath    string `json:"icon_path"`
	}
	if err := decodeJSONBody(r, &body); err != nil ||
		strings.TrimSpace(body.ChannelName) == "" || len(body.ChannelName) > 128 || body.IconPath == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "参数不完整"})
		return
	}
	abs, ok := s.safeIconPath(body.IconPath)
	if !ok {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "图标路径不合法"})
		return
	}
	if _, err := os.Stat(abs); err != nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "图标文件不存在"})
		return
	}
	clean := path.Clean(body.IconPath)
	if err := s.store.SetIconBind(r.Context(), body.ChannelName, clean); err != nil {
		http.Error(w, "数据库操作失败", http.StatusInternalServerError)
		return
	}
	s.loadIconBinds()
	writeJSON(w, http.StatusOK, map[string]string{"message": "已绑定图标"})
}

// adminIconBindDelete DELETE /api/admin/icons/binds/{channel} 解除频道图标绑定。
func (s *Server) adminIconBindDelete(w http.ResponseWriter, r *http.Request) {
	if err := s.store.DeleteIconBind(r.Context(), r.PathValue("channel")); err != nil {
		http.Error(w, "数据库操作失败", http.StatusInternalServerError)
		return
	}
	s.loadIconBinds()
	writeJSON(w, http.StatusOK, map[string]string{"message": "已解除绑定，恢复自动匹配"})
}

// afterIconChange 图标文件落盘/删除后：重建索引并发布 Redis 共享副本。
func (s *Server) afterIconChange() {
	s.icons.Refresh()
	s.publishIconIndex()
}

// decodeJSONBody 限长解析 JSON 请求体（管理端请求体都很小，1MB 上限足够）。
func decodeJSONBody(r *http.Request, v any) error {
	return json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(v)
}
