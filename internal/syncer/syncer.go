// Package syncer 负责 EPG 源的定时抓取、解析入库与过期数据清理。
// 源定义存于数据库（管理页可增删改，即时生效），配置文件仅作首次种子导入。
package syncer

import (
	"context"
	"fmt"
	"log"
	"os"
	"strings"
	"sync"
	"time"
	"unicode"

	"goepg/internal/config"
	"goepg/internal/epgtime"
	"goepg/internal/store"
	"goepg/internal/xmltv"
)

// 源同步状态。
const (
	StateIdle    = "idle"
	StateRunning = "running"
	StateOK      = "ok"
	StateError   = "error"
)

// SourceStatus 单个源的最近一次同步状态（内存态，重启后重置）。
type SourceStatus struct {
	Name         string     `json:"name"`
	URL          string     `json:"url"`
	State        string     `json:"state"`
	Message      string     `json:"message,omitempty"`
	ChannelCount int        `json:"channel_count"`
	ProgramCount int        `json:"program_count"`
	LastStart    *time.Time `json:"last_start,omitempty"`
	LastDone     *time.Time `json:"last_done,omitempty"`
	NextSyncAt   *time.Time `json:"next_sync_at,omitempty"`
	DurationSec  float64    `json:"duration_sec,omitempty"`
}

// Syncer 同步引擎：调度抓取、写库、清理，并维护源状态。
type Syncer struct {
	cfg   *config.Config
	store *store.Store
	loc   *time.Location

	mu      sync.Mutex
	running bool // 是否有全局同步在跑
	status  map[string]*SourceStatus
}

// New 创建同步引擎。
func New(cfg *config.Config, st *store.Store, loc *time.Location) *Syncer {
	return &Syncer{cfg: cfg, store: st, loc: loc, status: map[string]*SourceStatus{}}
}

// st 惰性创建源状态。
func (s *Syncer) st(name, url string) *SourceStatus {
	s.mu.Lock()
	defer s.mu.Unlock()
	v, ok := s.status[name]
	if !ok {
		v = &SourceStatus{Name: name, URL: url, State: StateIdle}
		s.status[name] = v
	}
	if url != "" {
		v.URL = url
	}
	return v
}

// Statuses 返回全部源状态快照。
func (s *Syncer) Statuses() []SourceStatus {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]SourceStatus, 0, len(s.status))
	for _, v := range s.status {
		out = append(out, *v)
	}
	return out
}

// StatusOf 单源状态。
func (s *Syncer) StatusOf(name string) *SourceStatus {
	s.mu.Lock()
	defer s.mu.Unlock()
	if v, ok := s.status[name]; ok {
		sv := *v
		return &sv
	}
	return nil
}

// IsRunning 是否有全局同步任务进行中。
func (s *Syncer) IsRunning() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.running
}

// DataVersion 返回全部源最近一次成功同步时刻的 Unix 秒（取最大值），
// 作为对外缓存/ETag 的数据版本依据；从未同步过返回 0。
func (s *Syncer) DataVersion() int64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	var max int64
	for _, st := range s.status {
		if st.LastDone != nil && st.LastDone.Unix() > max {
			max = st.LastDone.Unix()
		}
	}
	return max
}

// TriggerSync 手动触发一次全量同步（忽略各源间隔）；已有全局任务在跑时返回 false。
func (s *Syncer) TriggerSync() bool {
	s.mu.Lock()
	if s.running {
		s.mu.Unlock()
		return false
	}
	s.running = true
	s.mu.Unlock()
	go func() {
		defer func() {
			s.mu.Lock()
			s.running = false
			s.mu.Unlock()
		}()
		s.syncAll(context.Background(), true)
	}()
	return true
}

// TriggerSource 手动刷新单源（按源名）。
func (s *Syncer) TriggerSource(name string) error {
	ctx := context.Background()
	sources, err := s.store.ListSources(ctx)
	if err != nil {
		return err
	}
	for _, row := range sources {
		if row.Name == name {
			go func(row store.SourceRow) {
				if err := s.syncOne(ctx, row); err != nil {
					log.Printf("[syncer] 手动刷新源 %s 失败: %v", row.Name, err)
				}
			}(row)
			return nil
		}
	}
	return store.ErrNotFound
}

// Run 主循环：可选启动即全量同步，之后每 30 秒检查各源是否到期（独立间隔）。
func (s *Syncer) Run(ctx context.Context) {
	if s.cfg.EPG.SyncOnStart {
		s.syncAll(ctx, true)
	}
	t := time.NewTicker(30 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			s.syncAll(ctx, false)
		}
	}
}

// intervalOf 源生效的同步间隔：源自身配置优先，0 回退全局。
func (s *Syncer) intervalOf(row store.SourceRow) time.Duration {
	if row.IntervalMin > 0 {
		return time.Duration(row.IntervalMin) * time.Minute
	}
	return s.cfg.EPG.SyncInterval
}

// syncAll 同步源集合；force=true 忽略间隔全量同步，否则仅同步到期源。
func (s *Syncer) syncAll(ctx context.Context, force bool) {
	s.mu.Lock()
	if s.running && !force {
		s.mu.Unlock()
		return
	}
	if force {
		s.running = true
	}
	s.mu.Unlock()
	if force {
		defer func() {
			s.mu.Lock()
			s.running = false
			s.mu.Unlock()
		}()
	}

	sources, err := s.store.ListSources(ctx)
	if err != nil {
		log.Printf("[syncer] 读取源列表失败: %v", err)
		return
	}
	now := time.Now()
	sem := make(chan struct{}, s.cfg.EPG.SyncConcurrency)
	var wg sync.WaitGroup
	for _, row := range sources {
		if !row.Enabled {
			continue
		}
		if !force {
			if st := s.StatusOf(row.Name); st != nil && st.LastDone != nil &&
				now.Sub(*st.LastDone) < s.intervalOf(row) {
				continue // 未到期
			}
		}
		wg.Add(1)
		go func(row store.SourceRow) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			if err := s.syncOne(ctx, row); err != nil {
				log.Printf("[syncer] 源 %s 同步失败: %v", row.Name, err)
			}
		}(row)
	}
	wg.Wait()

	if force {
		s.cleanup(ctx, sources)
	}
}

// syncOne 同步单个源：下载到临时文件 → 两遍解析（先频道后节目）→ 批量入库。
// 文件落盘而非驻留内存，内存占用与源文件体积无关。
func (s *Syncer) syncOne(ctx context.Context, src store.SourceRow) error {
	st := s.st(src.Name, src.URL)
	start := time.Now()
	st.State = StateRunning
	st.LastStart = &start

	err := s.doSyncOne(ctx, src)

	s.mu.Lock()
	done := time.Now()
	st.LastDone = &done
	st.DurationSec = done.Sub(start).Seconds()
	next := done.Add(s.intervalOf(src))
	st.NextSyncAt = &next
	if err != nil {
		st.State = StateError
		st.Message = err.Error()
	} else {
		st.State = StateOK
		st.Message = ""
	}
	s.mu.Unlock()
	return err
}

func (s *Syncer) doSyncOne(ctx context.Context, src store.SourceRow) error {
	begin := time.Now()
	tmp, err := os.CreateTemp("", "goepg-"+src.Name+"-*.bin")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	tmp.Close()
	defer os.Remove(tmpPath)

	size, err := fetchToFile(ctx, src.URL, s.cfg.EPG.FetchTimeout,
		int64(s.cfg.EPG.MaxSourceSizeMB)*1024*1024, tmpPath)
	if err != nil {
		return fmt.Errorf("下载失败: %w", err)
	}

	// 第一遍：频道（量级小，全量读入）
	r, err := openMaybeGzip(tmpPath)
	if err != nil {
		return err
	}
	channels, err := xmltv.ParseChannels(r)
	r.Close()
	if err != nil {
		return fmt.Errorf("解析频道失败: %w", err)
	}
	if len(channels) == 0 {
		return fmt.Errorf("源数据中未找到任何频道")
	}
	// 同源内按归一化频道名去重：源里同时存在 "CCTV1" 与 "CCTV1 高清" 这类
	// 仅分辨率标记不同的频道时，保留先出现的（分辨率不影响节目内容）
	inputs := make([]store.ChannelInput, 0, len(channels))
	seenName := make(map[string]bool, len(channels))
	skippedDup := 0
	for _, c := range channels {
		norm := normalizeChName(c.Name)
		if seenName[norm] {
			skippedDup++
			continue
		}
		seenName[norm] = true
		inputs = append(inputs, store.ChannelInput{
			XmltvID: truncate(c.XmltvID, 128),
			Name:    truncate(c.Name, 128),
			Icon:    truncate(c.Icon, 512),
		})
	}
	chMap, err := s.store.ReplaceSourceChannels(ctx, src.Name, src.Priority, inputs)
	if err != nil {
		return err
	}

	// 第二遍：节目（流式回调，500 条/批入库）
	// 窗口 = [今天-history_days, 今天+keep_days]：源是滚动窗口，
	// 但源内仍含昨日数据时一并入库，历史节目得以保留
	windowFrom := epgtime.DayStart(time.Now(), s.loc).AddDate(0, 0, -s.cfg.EPG.HistoryDays)
	windowTo := epgtime.DayStart(time.Now(), s.loc).AddDate(0, 0, s.cfg.EPG.KeepDays)

	r2, err := openMaybeGzip(tmpPath)
	if err != nil {
		return err
	}
	defer r2.Close()

	progCount := 0
	hasProg := make(map[int64]bool, len(chMap)) // 本次同步实际写入过节目的频道
	batch := make([]store.Programme, 0, 500)
	flush := func() error {
		if len(batch) == 0 {
			return nil
		}
		if err := s.store.InsertProgrammes(ctx, batch); err != nil {
			return err
		}
		progCount += len(batch)
		batch = batch[:0]
		return nil
	}
	_, perr := xmltv.ParseProgrammes(r2, s.loc, func(pi xmltv.ProgrammeInput) error {
		if pi.Title == "" || isJunkTitle(pi.Title) {
			return nil
		}
		chID, ok := chMap[strings.ToLower(pi.XmltvID)]
		if !ok {
			return nil // programme 引用了未声明的频道
		}
		// 只保留保留窗口内的节目，库外数据不入库
		if pi.Start.Before(windowFrom) || !pi.Start.Before(windowTo) {
			return nil
		}
		// XMLTV stop 可缺省：以 Start==Stop 标记，这里按默认时长补齐
		if !pi.Stop.After(pi.Start) {
			pi.Stop = pi.Start.Add(s.cfg.EPG.DefaultDuration)
		}
		batch = append(batch, store.Programme{
			ChannelID: chID,
			Start:     pi.Start,
			Stop:      pi.Stop,
			Title:     truncate(pi.Title, 255),
			SubTitle:  truncate(pi.SubTitle, 255),
			Descr:     truncate(pi.Descr, 60000),
			Category:  truncate(pi.Category, 128),
		})
		hasProg[chID] = true
		if len(batch) >= 500 {
			return flush()
		}
		return nil
	})
	if perr != nil {
		return fmt.Errorf("解析节目失败: %w", perr)
	}
	if err := flush(); err != nil {
		return err
	}

	// 删除本次同步零节目的频道：源里声明了但没有节目数据的频道没有价值，
	// 不删除会以空频道形式出现在前台
	empty := make([]int64, 0)
	for _, id := range chMap {
		if !hasProg[id] {
			empty = append(empty, id)
		}
	}
	if len(empty) > 0 {
		if err := s.store.DeleteChannels(ctx, empty); err != nil {
			return fmt.Errorf("清理无节目频道失败: %w", err)
		}
	}

	log.Printf("[syncer] 源 %s 同步完成: %d 字节(原始), %d 频道(同名跳过 %d, 清理无节目 %d), %d 节目, 耗时 %s",
		src.Name, size, len(chMap)-len(empty), skippedDup, len(empty), progCount,
		time.Since(begin).Round(time.Second))

	s.mu.Lock()
	if st := s.status[src.Name]; st != nil {
		st.ChannelCount = len(chMap) - len(empty)
		st.ProgramCount = progCount
	}
	s.mu.Unlock()
	return nil
}

// cleanup 过期节目清理 + 已移除源的遗留数据清扫，在每轮全量同步后执行。
// 清理线 = 今天-history_days（含当天跨天节目：start 早于今天但未越过历史窗口）。
func (s *Syncer) cleanup(ctx context.Context, sources []store.SourceRow) {
	before := epgtime.DayStart(time.Now(), s.loc).AddDate(0, 0, -s.cfg.EPG.HistoryDays)
	if n, err := s.store.CleanupProgrammes(ctx, before); err != nil {
		log.Printf("[syncer] 过期清理失败: %v", err)
	} else if n > 0 {
		log.Printf("[syncer] 已清理过期节目 %d 条", n)
	}
	keep := make([]string, 0, len(sources)+len(s.cfg.Sources))
	for _, row := range sources {
		keep = append(keep, row.Name)
	}
	for _, src := range s.cfg.Sources { // 尚未入库种子也保留
		keep = append(keep, src.Name)
	}
	if removed, err := s.store.SweepOrphanSources(ctx, keep); err != nil {
		log.Printf("[syncer] 孤儿源清扫失败: %v", err)
	} else if len(removed) > 0 {
		log.Printf("[syncer] 已清理失效源: %v", removed)
	}
}

// Seed 把配置文件中的源导入数据库（仅新增，不覆盖管理页修改）。
func (s *Syncer) Seed() error {
	seeds := make([]struct {
		Name, URL string
		Priority  int
		Enabled   bool
	}, 0, len(s.cfg.Sources))
	for _, src := range s.cfg.Sources {
		seeds = append(seeds, struct {
			Name, URL string
			Priority  int
			Enabled   bool
		}{src.Name, src.URL, src.Priority, src.Enabled})
	}
	if len(seeds) == 0 {
		return nil
	}
	return s.store.SeedSources(context.Background(), seeds)
}

// junkTitleKeywords 水印/占位节目标题关键词（简繁体）。
// 仅收录"无信息量"的确定形态，避免误杀真实节目（如「香港都會大學提供:知識新天地」）。
var junkTitleKeywords = []string{
	"提供节目单服务", "提供節目單服務", // 源推广：由 https://xxx 提供节目单服务
	"暂无节目单", "暫無節目單",     // 暂无节目单1/2/3
	"暂未提供", "暫未提供",         // 精彩节目-暂未提供节目预告信息
	"尚未提供當日節目表",           // 頻道尚未提供當日節目表
	"未能提供節目表",              // 此頻道未能提供節目表敬請原諒!
}

// isJunkTitle 判断是否源推广水印或"无节目"占位条目（入库时丢弃）。
func isJunkTitle(title string) bool {
	for _, kw := range junkTitleKeywords {
		if strings.Contains(title, kw) {
			return true
		}
	}
	return false
}

// resolutionWords 频道名中的分辨率标记词（简繁体）：分辨率不影响节目内容，
// 去掉后同名视为同一频道。长词在前，先删 "全高清" 才不会残留 "全"。
var resolutionWords = []string{
	"全高清", "超高清", "標清", "标清", "超清", "高清",
	"FHD", "UHD", "HD", "SD", "4K", "8K",
}

// normalizeChName 频道名归一化（仅作同源去重的比较键，不改动入库原名）：
// 先删分辨率标记词再删空白（顺序不能反，否则 "tvN HD" 的 HD 会因
// 紧邻字母被当作单词内部），最后去残留首尾分隔符并统一小写。
// "CCTV1 高清"、"CCTV1高清"、"tvN HD" 分别归一为 "cctv1"、"tvn"。
func normalizeChName(name string) string {
	s := name
	for _, w := range resolutionWords {
		s = removeWord(s, w)
	}
	s = strings.Map(func(r rune) rune {
		if unicode.IsSpace(r) {
			return -1
		}
		return r
	}, s)
	return strings.Trim(strings.ToLower(s), "-_·")
}

// removeWord 删除 s 中所有出现的 word（大小写不敏感），仅当 word 前后
// 紧邻的都不是 ASCII 字母时才删：防误伤 CHILDHOOD 等单词内部的 HD；
// 数字不受限（CCTV1HD 的 HD 是分辨率标记，应删）。
func removeWord(s, word string) string {
	lw := strings.ToLower(word)
	lower := strings.ToLower(s)
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); {
		j := strings.Index(lower[i:], lw)
		if j < 0 {
			b.WriteString(s[i:])
			break
		}
		abs := i + j
		end := abs + len(lw)
		prev, next := byte(0), byte(0)
		if abs > 0 {
			prev = lower[abs-1]
		}
		if end < len(lower) {
			next = lower[end]
		}
		if isASCIILetter(prev) || isASCIILetter(next) {
			b.WriteString(s[i:end]) // 非独立词，原样保留
		} else {
			b.WriteString(s[i:abs])
		}
		i = end
	}
	return b.String()
}

// isASCIILetter 判断是否 ASCII 字母。
func isASCIILetter(c byte) bool {
	return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z'
}

// truncate 按 rune 截断字符串，保证不切坏 UTF-8 且不超出列宽。
func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	runes := []rune(s)
	if len(runes) <= max {
		return s // 字节数超限但字符数未超（utf8mb4 按字符计），无需截断
	}
	return string(runes[:max])
}
