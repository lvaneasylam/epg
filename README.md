# goepg — 多源 EPG 节目单聚合服务

汇集多个 XMLTV 格式的 EPG 节目单源（`.xml` / `.xml.gz` 自动识别解压），聚合入库后统一对外提供查询。Go 实现，单二进制部署，**兼容 MySQL 5.6**。

## 功能

- **多源抓取**：定时并发抓取 EPG 源，gzip 魔数自动检测（与扩展名无关），流式两遍解析（先频道后节目），内存占用与源文件体积无关
- **保留策略**：全库可配置保留 N 天节目（默认 7 天），解析时即过滤窗口外数据，过期数据分批清理
- **同名频道合并**：不同源存在相同频道时，按源优先级（`priority` 越小越优先）取最优数据
- **对外接口**：
  - `GET /api/xml[?days=N]` — 汇总全部频道重新封装为 XMLTV 下发（支持 ETag/304、gzip）
  - `GET /api/epg?ch={频道名}&date=YYYY-MM-DD` — 单频道当日节目 JSON（`date` 缺省今天）
- **节目单页面**：`GET /` 时间轴网格视图（频道 × 24h，节目卡片按时长等比排布，红色当前时间线、播出中高亮、日期切换、点击看节目详情）
- **缓存**：Redis 可选（`addr` 留空自动降级进程内存缓存）
- **运维接口**：`GET /api/status` 库存统计与源状态、`POST /api/sync` 手动触发同步

## 快速开始

```bash
# 1. 准备数据库（MySQL 5.6+，建库即可，表自动创建）
#    CREATE DATABASE goepg DEFAULT CHARACTER SET utf8mb4;

# 2. 准备配置
cp config.example.yaml config.yaml
#    修改 mysql.dsn 与 sources 列表

# 3. 运行（首次启动会自动同步一次）
./dist/goepg-linux-amd64 -c config.yaml
```

## 构建

需要 Go 1.22+：

```bash
# Linux 服务器（常用部署目标）
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -ldflags="-s -w" -o goepg .

# macOS (Apple Silicon)
CGO_ENABLED=0 GOOS=darwin GOARCH=arm64 go build -trimpath -ldflags="-s -w" -o goepg .
```

产物为单个静态二进制（前端页面已 `go:embed` 内嵌），拷贝到服务器即可运行，无外部文件依赖（配置文件除外）。

## 配置说明

见 `config.example.yaml`，关键项：

| 配置 | 说明 |
|---|---|
| `mysql.dsn` | MySQL 5.6 连接串，程序会强制 `parseTime=true`、`loc=配置时区`、`charset=utf8mb4` |
| `server.timezone` | 业务时区（决定"哪一天"的划分与页面显示），如 `Asia/Shanghai` |
| `redis.addr` | 留空则使用进程内存缓存 |
| `epg.keep_days` | 全库节目保留天数（今天起算，含今天） |
| `epg.sync_interval` | 自动同步间隔，如 `6h`、`30m` |
| `sources[].name` | 源唯一标识，入库关联用；改名等同于换源（旧数据会被清扫） |
| `sources[].priority` | 数字越小优先级越高，同名频道取该值最小的源 |
| `sources[].enabled` | `false` 跳过该源，其数据将在下轮同步后被清理 |

## 接口示例

```bash
# 全量 XMLTV（给播放器/订阅工具用）
curl http://127.0.0.1:8080/api/xml -o epg.xml --compressed

# 单频道节目（date 可省略）
curl "http://127.0.0.1:8080/api/epg?ch=翡翠台&date=2026-10-23"

# 运行状态
curl http://127.0.0.1:8080/api/status

# 手动触发同步
curl -X POST http://127.0.0.1:8080/api/sync
```

`/api/epg` 响应示例：

```json
{
  "channel": {"name": "翡翠台"},
  "date": "2026-10-23",
  "count": 2,
  "programmes": [
    {
      "start": "2026-10-23T08:00:00+08:00",
      "stop": "2026-10-23T09:00:00+08:00",
      "title": "香港早晨",
      "sub_title": "",
      "desc": "晨间新闻资讯节目",
      "category": "新闻"
    }
  ]
}
```

## MySQL 5.6 兼容要点

- 索引列宽受控（utf8mb4 下 InnoDB 索引前缀 767 字节限制：`source(48)+xmltv_id(128)`、`name(128)`）
- 不使用 JSON 类型 / CHECK 约束 / CTE / 窗口函数等 5.6 不支持的特性
- 大删除使用 `DELETE ... LIMIT` 分批执行，避免长事务锁表
- 建表幂等（`CREATE TABLE IF NOT EXISTS`），无需手动迁移

## systemd 部署示例

```ini
[Unit]
Description=goepg EPG service
After=network.target mysql.service

[Service]
ExecStart=/opt/goepg/goepg -c /opt/goepg/config.yaml
Restart=always
RestartSec=5
# 服务器时区需与 server.timezone 一致，或显式设置
Environment=TZ=Asia/Shanghai

[Install]
WantedBy=multi-user.target
```

## 项目结构

```
main.go                    入口：装配各模块、优雅停机
internal/config/           配置定义、加载、校验
internal/store/            MySQL 5.6 兼容数据层（建表/查询/批量写入/清理）
internal/xmltv/            XMLTV 流式解析（多时间格式兼容）
internal/syncer/           下载（gzip 魔数检测）、定时并发同步、保留期清理
internal/cache/            Redis / 内存双模式缓存
internal/server/           HTTP 服务与页面数据接口
internal/epgtime/          时区安全的时间工具（防 Truncate 时区陷阱）
web/                       前端静态资源（go:embed 内嵌）
dist/                      预编译二进制
```

## 开发

```bash
go build ./...   # 编译
go vet ./...     # 静态检查
go test ./...    # 单元测试（XMLTV 解析、时区归一化、频道去重、XML 生成）
```
