// goepg：多源 EPG 节目单聚合服务。
// 抓取 XMLTV 源（xml/xml.gz）入库聚合，对外提供 XMLTV 全量下发、
// 单频道查询接口与节目单时间轴页面。单二进制部署，兼容 MySQL 5.6。
package main

import (
	"context"
	"flag"
	"log"
	"net/http"
	"os/signal"
	"syscall"
	"time"

	"goepg/internal/cache"
	"goepg/internal/config"
	"goepg/internal/server"
	"goepg/internal/store"
	"goepg/internal/syncer"
)

func main() {
	cfgPath := flag.String("c", "config.yaml", "配置文件路径")
	flag.Parse()

	cfg, err := config.Load(*cfgPath)
	if err != nil {
		log.Fatalf("[main] %v", err)
	}
	loc, err := time.LoadLocation(cfg.Server.Timezone)
	if err != nil {
		log.Fatalf("[main] 无效时区配置 %q: %v", cfg.Server.Timezone, err)
	}

	st, err := store.Open(cfg.MySQL.DSN, loc, cfg.MySQL.MaxOpenConns, cfg.MySQL.MaxIdleConns)
	if err != nil {
		log.Fatalf("[main] %v", err)
	}
	defer st.Close()
	if err := st.Migrate(); err != nil {
		log.Fatalf("[main] %v", err)
	}
	log.Printf("[main] 数据库就绪（MySQL 5.6 兼容模式）")

	ca := cache.New(cfg.Redis)
	defer ca.Close()
	if cfg.Redis.Addr != "" {
		log.Printf("[main] Redis 缓存已启用: %s", cfg.Redis.Addr)
	} else {
		log.Printf("[main] 未配置 Redis，使用进程内存缓存")
	}

	sy := syncer.New(cfg, st, loc)
	if err := sy.Seed(); err != nil { // 配置源导入数据库（仅新增，管理页为运行时权威）
		log.Fatalf("[main] 配置源导入失败: %v", err)
	}
	srv, err := server.New(cfg, st, sy, ca, loc)
	if err != nil {
		log.Fatalf("[main] %v", err)
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	go sy.Run(ctx)

	go func() {
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("[main] HTTP 服务异常退出: %v", err)
		}
	}()

	<-ctx.Done()
	log.Printf("[main] 收到退出信号，优雅停机…")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		log.Printf("[main] HTTP 停机关闭异常: %v", err)
	}
	log.Printf("[main] 已退出")
}
