# 图标库管理与频道图标绑定 · 设计

日期：2026-10-29
状态：已确认（方案 A，MySQL 存储，图标文件落盘文件系统）

## 1. 背景与目标

现状：`data/icons/` 按省份/分类一级目录组织（央视/卫视/北京/…共 30+ 个），
文件名即频道名，`internal/iconlib` 做归一化模糊匹配（跨目录生效），
`/icon/{频道名}` 输出图片。维护图标只能 scp 拷文件 + 手动点「刷新图标索引」。

目标：管理页新增「图标库」能力——

1. 上传图标：输入名字 + 拖拽/选文件上传；或输入 URL 由服务端下载导入
2. 目录树选择目标目录（按省份），单击选中，支持在线新建子目录
3. 浏览已有图标（缩略图网格）、删除、同名直接覆盖
4. 频道 ⇄ 图标手动绑定：优先于自动模糊匹配

## 2. 总体架构（方案 A：文件系统为真相源 + MySQL 绑定表）

```
拖拽/选文件上传 ─┐
                ├→ POST /api/admin/icons/upload | fetch
URL 服务端下载 ──┘        │ 校验（见 §5）→ data/icons/{目录}/{名字}.{真实扩展名}
                          │ 同目录同名直接覆盖
                          ▼
            s.icons.Refresh() + publishIconIndex()   ← 复用现有逻辑，立即生效
```

- 图标文件始终是磁盘文件（保留热拷贝迁移能力），不入库
- 手动绑定存 MySQL 新表，`/icon` 匹配时优先查进程内存绑定 map（O(1)，无 DB 开销）

## 3. 数据库（MySQL 5.6 兼容，加入 db.go schema 列表）

```sql
CREATE TABLE IF NOT EXISTS epg_icon_bindings (
  channel_name VARCHAR(128) NOT NULL COMMENT '对外频道名（查询键）',
  icon_path    VARCHAR(255) NOT NULL COMMENT '图标相对路径，相对 icon 目录',
  created_at   DATETIME     NOT NULL,
  updated_at   DATETIME     NOT NULL,
  PRIMARY KEY (channel_name)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4
```

- 主键 channel_name（128×4=512B < 767B 前缀限制，合规）；icon_path 不建索引
- 写入用 `ON DUPLICATE KEY UPDATE`（同 SetBinding 风格）
- 新文件 `internal/store/iconbind.go`：Set/Delete/List/All（返回 map）

## 4. API（挂 /api/admin/icons 前缀，走现有 token 鉴权）

| 方法 | 路径 | 说明 |
|---|---|---|
| GET | /api/admin/icons/tree | 目录列表 + 每目录文件名数组（一次全量，前端切目录零延迟） |
| GET | /api/admin/icons/raw?path= | 缩略图文件输出（带鉴权，防公开目录枚举） |
| POST | /api/admin/icons/upload | multipart：dir、name、file |
| POST | /api/admin/icons/fetch | JSON：{dir, name, url}，服务端下载 |
| POST | /api/admin/icons/mkdir | JSON：{name}，新建一级子目录 |
| DELETE | /api/admin/icons/file?path= | 删除文件 + 清理指向它的绑定 |
| GET | /api/admin/icons/binds | 绑定列表 |
| PUT | /api/admin/icons/binds | JSON：{channel_name, icon_path} |
| DELETE | /api/admin/icons/binds/{channel} | 解绑 |

## 5. 校验与安全

- **名字**：TrimSpace、拒绝 `/` `\` 与控制字符、不得以 `.` 开头、长度 ≤128；
  落盘文件名 = 名字 + 按真实图片类型推断的扩展名
- **图片识别**：magic bytes 探测 png/jpg/webp/gif（不信任上传 Content-Type 与 URL 后缀）
- **大小**：上传 ≤2MB；URL 下载 `io.LimitReader` 5MB 截断
- **URL**：仅 http/https，10s 超时；管理端已有 token 鉴权，不做额外 SSRF 拦截
  （内网部署场景源地址常为内网）
- **目录**：仅一级、上传时必须已存在；mkdir 名做同样的字符白名单校验
- **路径穿越**：所有 path 参数 filepath.Clean 后必须仍位于 icon 目录内
- **覆盖**：同目录同文件名直接覆盖（已确认），界面上标注「已替换」

## 6. /icon/{name} 匹配优先级（修改 handleIcon）

1. 内存绑定 map：key = `iconlib.Norm(频道名)` → icon_path；
   绑定文件不存在（被删）时回退自动匹配并清掉脏绑定
2. 现有三级模糊匹配（归一化全等 → 去后缀词 → 包含最长命中）

绑定 map 生命周期：Server 启动时从 MySQL 全量加载；绑定/解绑/删文件时
同步更新内存 + MySQL。`/icon` 响应 `Cache-Control: max-age=86400` 不变，
替换同名图标后播放器最长 1 天见新图（管理页缩略图加 `?v=mtime` 绕过）；
绑定不改变 epg JSON 里的 URL 形态，无需失效接口缓存。

## 7. 前端（admin 页新标签「图标库」）

布局：左目录树 / 右内容区。

- **目录树**：单击选中高亮；顶部「＋新建目录」；显示每目录图标数
- **上传卡**：
  - 名字输入框（即频道名/文件名）
  - 拖拽区（drop 高亮，也支持点击选文件）
  - URL 导入行：名字 + 地址 + 「下载导入」按钮
- **图标网格**：选中目录的图标卡片——缩略图（lazy，`?v=mtime`）、文件名、
  悬浮操作（删除/绑定频道）；已绑定的卡片显示徽标，悬停列出绑定频道
- **绑定弹窗**：选中图标 → 输入频道名（datalist 自动补全，数据来自
  GET /api/admin/bindings）→ 保存/解绑
- 上传成功自动选中目标目录并高亮新图标；删除需二次确认

频道绑定页本次不加图标列（YAGNI，后续需要再加入口很容易）。

## 8. 文件清单

新增：
- `internal/store/iconbind.go` — 绑定 CRUD
- `internal/server/icon_admin.go` — 图标管理 API
- `internal/iconlib/save.go` — SaveIcon（校验+落盘）+ 图片类型探测（可单测纯函数）

修改：
- `internal/store/db.go` — +1 条 DDL
- `internal/server/server.go` — 注册路由 + 绑定 map 加载
- `internal/server/icon.go` — handleIcon 绑定优先
- `web/static/admin.html` / `admin.js` / `admin.css` — 新标签页

## 9. 测试

- `iconlib`：图片类型探测、名字清洗、路径穿越防护（纯函数单测）
- `server`：upload/fetch/tree/delete/bind 集成测试 + `/icon` 绑定优先级用例
- 风格参照现有 `iconlib_test.go` / `epg_test.go`
