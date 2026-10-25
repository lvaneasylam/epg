package server

// /api/timeline：首页时间轴数据。NDJSON 流式输出——首行 meta，之后每行一个
// 频道；逐批（500 频道）查询、逐批推送，配合 gzip 让浏览器渐进渲染。
//
// 缓存按"分片 gzip"存储：完整响应切成 N 片（每片一批频道行，meta 行并入首片），
// 各自 gzip 后落 Redis——分片两重意义：
//   - 命中时逐片拉取逐片写出，首字节时间从"全部拉完"降为"第一片到达"
//     （跨公网小带宽连 Redis 时，7MB 单键整体拉取是 TTFB 大头）
//   - 拉取总量降 89%（gzip 后共约 0.8MB）
// 回放为多个 gzip 成员顺序拼接（HTTP Content-Encoding 标准允许，主流客户端
// 解码器均支持）。片数记录在 idx 键；片键随频道数自然变化，频道增减后整体重建。
// 响应带短 Cache-Control，浏览器二次访问零请求。
//
// 仅配置 icon.base_url 时启用缓存（icon 地址可预生成）；未配置仍享受
// 流式 + gzip，只是不缓存。

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"goepg/internal/epgtime"
	"goepg/internal/store"
)

// timelineBatch 每批查询并推送的频道数（与 store 查询批大小一致，即分片大小）。
const timelineBatch = 500

// timelineClientCache 浏览器短缓存：数据随预热刷新（同步 6h 一次），
// 分钟级本地缓存安全，二次访问零请求。
const timelineClientCache = "public, max-age=180"

// timelineChannel NDJSON 频道行（页面网格渲染用，精简字段省流量）。
type timelineChannel struct {
	Name       string              `json:"name"`
	Icon       string              `json:"icon,omitempty"`
	Programmes []timelineProgramme `json:"programmes"`
}

type timelineProgramme struct {
	Start time.Time `json:"start"`
	Stop  time.Time `json:"stop"`
	Title string    `json:"title"`
}

// timelineMeta NDJSON 首行的全局元信息。
type timelineMeta struct {
	Date     string `json:"date"`
	DayStart string `json:"day_start"` // RFC3339，前端网格定位基准
}

// timelineIdx 分片索引键的值：meta 与片数。
type timelineIdx struct {
	timelineMeta
	Shards int `json:"shards"`
}

func timelineIdxKey(dateStr string) string { return "epg:timeline:" + dateStr + ":idx" }
func timelineShardKey(dateStr string, i int) string {
	return "epg:tl:" + dateStr + ":" + strconv.Itoa(i)
}

// forEachTimelineShard 逐批生成各分片的原始 NDJSON 行（不含 meta 行），
// 每批回调 emit(片序号, 该片字节)。分片边界即查询批次，频道数变化时
// 片数随之变化，旧分片整体作废。
func (s *Server) forEachTimelineShard(ctx context.Context, day time.Time,
	channels []store.Channel, iconBase string, emit func(i int, shard []byte) error) (int, error) {
	shard := 0
	for start := 0; start < len(channels); start += timelineBatch {
		end := min(start+timelineBatch, len(channels))
		batch := channels[start:end]
		ids := make([]int64, 0, len(batch))
		for _, c := range batch {
			ids = append(ids, c.ID)
		}
		progs, err := s.store.ProgrammesBetween(ctx, ids, day, day.Add(24*time.Hour), false)
		if err != nil {
			return shard, err
		}
		byID := make(map[int64][]timelineProgramme, len(batch))
		for _, p := range progs {
			byID[p.ChannelID] = append(byID[p.ChannelID],
				timelineProgramme{Start: p.Start, Stop: p.Stop, Title: p.Title})
		}
		var rows bytes.Buffer
		for _, c := range batch {
			ps := byID[c.ID]
			if len(ps) == 0 {
				continue // 当日无节目的频道不下发
			}
			row, err := json.Marshal(timelineChannel{Name: c.Name, Icon: iconJoin(iconBase, c.Name), Programmes: ps})
			if err != nil {
				continue
			}
			rows.Write(row)
			rows.WriteByte('\n')
		}
		if err := emit(shard, rows.Bytes()); err != nil {
			return shard, err
		}
		shard++
	}
	return shard, nil
}

// gzipBytes 压缩字节（分片缓存写入用）。
func gzipBytes(b []byte) []byte {
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	_, _ = gz.Write(b)
	_ = gz.Close()
	return buf.Bytes()
}

// handleTimeline GET /api/timeline?date=YYYY-MM-DD
func (s *Server) handleTimeline(w http.ResponseWriter, r *http.Request) {
	dateStr := strings.TrimSpace(r.URL.Query().Get("date"))
	if dateStr == "" {
		dateStr = epgtime.FormatDate(time.Now().In(s.tz()))
	}
	day, ok := epgtime.ParseDate(dateStr, s.tz())
	if !ok {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "date 参数格式应为 YYYY-MM-DD"})
		return
	}

	gzipOK := strings.Contains(r.Header.Get("Accept-Encoding"), "gzip")
	w.Header().Set("Content-Type", "application/x-ndjson; charset=utf-8")
	w.Header().Set("Cache-Control", timelineClientCache)
	flush := func() {
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
	}

	// 命中回放：idx 存在且客户端支持 gzip（缓存为 gzip 分片），逐片拉取，
	// 解压后喂给统一的 gzip 输出流（单成员，所有客户端可解——多成员拼接
	// 在 curl 等解码器下只解首成员，不可依赖），逐片 flush 渐进推送。
	if s.cfg.Icon.BaseURL != "" && gzipOK {
		if idxRaw, hit := s.cache.Get(r.Context(), timelineIdxKey(dateStr)); hit {
			var idx timelineIdx
			if json.Unmarshal(idxRaw, &idx) == nil && idx.Date == dateStr && idx.Shards > 0 {
				w.Header().Set("Content-Encoding", "gzip")
				w.Header().Set("X-Cache", "hit")
				gzOut := gzip.NewWriter(w)
				for i := 0; i < idx.Shards; i++ {
					gzShard, ok := s.cache.Get(r.Context(), timelineShardKey(dateStr, i))
					if !ok {
						break // 片部分丢失（极低概率）：流到此为止，前端已拿到部分数据
					}
					zr, err := gzip.NewReader(bytes.NewReader(gzShard))
					if err != nil {
						break
					}
					_, _ = io.Copy(gzOut, zr)
					_ = zr.Close()
					_ = gzOut.Flush() // 同步刷出但不结束流，客户端可渐进解压
					flush()
				}
				_ = gzOut.Close()
				return
			}
		}
	}

	all, err := s.store.AllChannels(r.Context())
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "数据库查询失败"})
		return
	}
	bindings, err := s.store.BindingChannelMap(r.Context())
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "数据库查询失败"})
		return
	}
	hidden, err := s.store.HiddenSet(r.Context())
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "数据库查询失败"})
		return
	}
	channels := store.FilterHidden(store.ApplyBindings(all, bindings), hidden)

	w.Header().Set("X-Cache", "miss")
	// icon 基地址：配置优先；未配置按请求 Host 动态生成（此路径不缓存）
	iconBase := s.cfg.Icon.BaseURL
	if iconBase == "" {
		iconBase = baseFromRequest(r)
	}

	// 流式生成：客户端（可选 gzip）+ 分片累积（原始行，结束后压缩入缓存；
	// meta 行并入首片缓存，回放时无需单独处理）
	meta, _ := json.Marshal(timelineMeta{Date: dateStr, DayStart: day.Format(time.RFC3339)})
	var client io.Writer = w
	var gz *gzip.Writer
	if gzipOK {
		w.Header().Set("Content-Encoding", "gzip")
		gz = gzip.NewWriter(w)
		client = gz
		defer gz.Close()
	}
	if _, err := client.Write(append(meta, '\n')); err != nil {
		return
	}
	flush()

	cacheable := s.cfg.Icon.BaseURL != ""
	var shards [][]byte
	_, genErr := s.forEachTimelineShard(r.Context(), day, channels, iconBase,
		func(i int, shard []byte) error {
			if cacheable {
				// 缓存副本：首片前拼 meta 行（meta 已在流开头写给客户端，不重复写）
				cacheShard := shard
				if i == 0 {
					cacheShard = append(append(meta, '\n'), shard...)
				}
				shards = append(shards, cacheShard)
			}
			if _, err := client.Write(shard); err != nil {
				return err
			}
			flush()
			return nil
		})
	if genErr == nil && cacheable && len(shards) > 0 {
		idxRaw, _ := json.Marshal(timelineIdx{timelineMeta{Date: dateStr, DayStart: day.Format(time.RFC3339)}, len(shards)})
		s.cache.Set(r.Context(), timelineIdxKey(dateStr), idxRaw, s.epgCacheTTL(dateStr))
		for i, sh := range shards {
			s.cache.Set(r.Context(), timelineShardKey(dateStr, i), gzipBytes(sh), s.epgCacheTTL(dateStr))
		}
	}
}
