package server

// /api/epg 响应热缓存预热：昨/今/明三天全部对外频道的 DIYP 响应在
// 同步完成后主动写入缓存（对齐 XML"更新时顺便生成"的模式），
// 播放器请求直达缓存，不再回源数据库。

import (
	"context"
	"encoding/json"
	"log"
	"time"

	"goepg/internal/cache"
	"goepg/internal/epgtime"
	"goepg/internal/store"
)

// tryWarmEPG 尝试触发预热（异步执行，几十秒不阻塞调用方）：
// 已有预热在进行中时返回 false。手动触发（admin）与自动调度共用此入口。
func (s *Server) tryWarmEPG() bool {
	if !s.warmMu.TryLock() {
		return false
	}
	go func() {
		defer s.warmMu.Unlock()
		s.warmEPGCache()
	}()
	return true
}

const (
	// epgWarmTTL 热缓存有效期：只需覆盖同步周期（默认 6h）与跨天滚动窗口，
	// 每次预热都会覆盖刷新。
	epgWarmTTL = 48 * time.Hour
	// warmAfterChange 数据版本变化（同步完成/绑定变更）后延迟预热：
	// 错开同步收尾的数据库负载。
	warmAfterChange = 5 * time.Minute
	// warmDailyClock 每日固定预热时刻（业务时区）：跨天后昨/今/明窗口滚动。
	warmDailyClock = 5 * time.Minute
)

// watchEPGWarm 预热调度，每分钟检查一次，触发点二选一命中即预热：
//   - 数据版本变化后 5 分钟（启动视为一次变化，即启动 +5min 先热一次；
//     连续变化重新计时，跟随最后一次更新）
//   - 每天 00:05（业务时区），跨天滚动热窗口
//
// 单 goroutine 串行执行，天然避免并发预热。
func (s *Server) watchEPGWarm() {
	lastVer := s.dataVersion()
	verChangedAt := time.Now()                             // 启动视为一次版本变化
	lastWarmedVer := ""                                    // 已据此版本完成预热的版本号
	lastDaily := epgtime.FormatDate(time.Now().In(s.tz())) // 启动日已完成（若 00:05 已过，由启动 +5min 预热覆盖；跨天后自然触发）
	for {
		now := time.Now().In(s.tz())
		if ver := s.dataVersion(); ver != lastVer {
			lastVer, verChangedAt = ver, time.Now()
		}
		due := false
		if lastVer != lastWarmedVer && time.Since(verChangedAt) >= warmAfterChange {
			lastWarmedVer = lastVer
			due = true
		}
		if today := epgtime.FormatDate(now); lastDaily != today &&
			now.After(epgtime.DayStart(now, s.tz()).Add(warmDailyClock)) {
			lastDaily = today
			due = true
		}
		if due {
			s.tryWarmEPG()
		}
		time.Sleep(time.Minute)
	}
}

// warmEPGCache 预热昨/今/明三天全部对外频道的 /api/epg 响应。
// 逐天处理控制内存峰值；空节目频道也写缓存（值为合法空结构），
// 避免无节目频道每次请求都回源。
func (s *Server) warmEPGCache() {
	if s.cfg.Icon.BaseURL == "" {
		log.Printf("[warm] 未配置 icon.base_url，epg 热缓存预热跳过" +
			"（icon 地址依赖请求 Host 动态生成，无法预生成稳定地址）")
		return
	}
	ctx := context.Background()
	all, err := s.store.AllChannels(ctx)
	if err != nil {
		log.Printf("[warm] 预热失败（频道查询）: %v", err)
		return
	}
	bindings, err := s.store.BindingChannelMap(ctx)
	if err != nil {
		log.Printf("[warm] 预热失败（绑定查询）: %v", err)
		return
	}
	channels := store.ApplyBindings(all, bindings) // 对外代表频道，与 /api/epg 查找口径一致

	start := time.Now()
	total := 0
	today := epgtime.DayStart(time.Now(), s.tz())
	for off := -1; off <= 1; off++ {
		day := today.AddDate(0, 0, off)
		dateStr := epgtime.FormatDate(day)

		ids := make([]int64, 0, len(channels))
		for _, c := range channels {
			ids = append(ids, c.ID)
		}
		progs, err := s.store.ProgrammesBetween(ctx, ids, day, day.Add(24*time.Hour), true)
		if err != nil {
			log.Printf("[warm] 预热失败（%s 节目查询）: %v", dateStr, err)
			return
		}
		byChannel := make(map[int64][]store.Programme, len(channels))
		for _, p := range progs {
			byChannel[p.ChannelID] = append(byChannel[p.ChannelID], p)
		}

		kvs := make([]cache.KV, 0, len(channels))
		for _, c := range channels {
			body := buildEPGBody(c.Name, dateStr, iconJoin(s.cfg.Icon.BaseURL, c.Name),
				byChannel[c.ID], day, s.tz())
			kvs = append(kvs, cache.KV{Key: epgCacheKey(c.Name, dateStr), Val: body})
		}
		s.cache.MSet(ctx, kvs, epgWarmTTL)
		total += len(kvs)
	}
	log.Printf("[warm] epg 热缓存预热完成: %d 频道 × 3 天 = %d 键, 耗时 %s",
		len(channels), total, time.Since(start).Round(time.Millisecond))

	s.warmTimeline(ctx, all, bindings, today)
}

// warmTimeline 预热当天 timeline 分片缓存（首页时间轴，只预热今天：
// 历史/明天按需回填即可）。分片 gzip 存储，命中时逐片拉取渐进输出。
func (s *Server) warmTimeline(ctx context.Context, all []store.Channel, bindings store.BindingMap, today time.Time) {
	hidden, err := s.store.HiddenSet(ctx)
	if err != nil {
		log.Printf("[warm] timeline 预热失败（隐藏名单查询）: %v", err)
		return
	}
	channels := store.FilterHidden(store.ApplyBindings(all, bindings), hidden)
	dateStr := epgtime.FormatDate(today)

	meta, _ := json.Marshal(timelineMeta{Date: dateStr, DayStart: today.Format(time.RFC3339)})
	var shards [][]byte
	n, err := s.forEachTimelineShard(ctx, today, channels, s.cfg.Icon.BaseURL,
		func(i int, shard []byte) error {
			if i == 0 {
				shard = append(append(meta, '\n'), shard...)
			}
			shards = append(shards, gzipBytes(shard))
			return nil
		})
	if err != nil {
		log.Printf("[warm] timeline 预热失败: %v", err)
		return
	}
	idxRaw, _ := json.Marshal(timelineIdx{timelineMeta{Date: dateStr, DayStart: today.Format(time.RFC3339)}, n})
	s.cache.Set(ctx, timelineIdxKey(dateStr), idxRaw, epgWarmTTL)
	for i, sh := range shards {
		s.cache.Set(ctx, timelineShardKey(dateStr, i), sh, epgWarmTTL)
	}
	total := 0
	for _, sh := range shards {
		total += len(sh)
	}
	log.Printf("[warm] timeline 热缓存预热完成: %d 频道, %d 片, %d 字节(压缩后)",
		len(channels), n, total)
}
