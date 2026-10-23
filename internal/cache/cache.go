// Package cache 提供接口响应缓存：配置了 Redis 用 Redis，否则降级为进程内存缓存。
package cache

import (
	"context"
	"encoding/json"
	"log"
	"sync"
	"time"

	"github.com/redis/go-redis/v9"

	"goepg/internal/config"
)

// Cache 缓存接口，value 为完整响应字节。
type Cache interface {
	Get(ctx context.Context, key string) ([]byte, bool)
	Set(ctx context.Context, key string, val []byte, ttl time.Duration)
	// HSetAll 整表替换写入 hash（先删后写），用于发布共享索引；成功返回 true。
	HSetAll(ctx context.Context, key string, m map[string]string) bool
	Close() error
}

// New 按配置创建缓存实例。
func New(cfg config.RedisConfig) Cache {
	if cfg.Addr == "" {
		return newMemCache()
	}
	rdb := redis.NewClient(&redis.Options{
		Addr:     cfg.Addr,
		Password: cfg.Password,
		DB:       cfg.DB,
	})
	return &redisCache{rdb: rdb}
}

// memCache 进程内存缓存：带 TTL、惰性过期，读写锁保护。
type memCache struct {
	mu    sync.RWMutex
	items map[string]memItem
}

type memItem struct {
	val    []byte
	expire time.Time
}

func newMemCache() *memCache {
	c := &memCache{items: make(map[string]memItem)}
	go func() {
		// 周期清扫过期项，防止长期运行下 map 无限增长
		for range time.Tick(10 * time.Minute) {
			now := time.Now()
			c.mu.Lock()
			for k, v := range c.items {
				if now.After(v.expire) {
					delete(c.items, k)
				}
			}
			c.mu.Unlock()
		}
	}()
	return c
}

func (c *memCache) Get(_ context.Context, key string) ([]byte, bool) {
	c.mu.RLock()
	it, ok := c.items[key]
	c.mu.RUnlock()
	if !ok || time.Now().After(it.expire) {
		return nil, false
	}
	return it.val, true
}

func (c *memCache) Set(_ context.Context, key string, val []byte, ttl time.Duration) {
	c.mu.Lock()
	c.items[key] = memItem{val: val, expire: time.Now().Add(ttl)}
	c.mu.Unlock()
}

func (c *memCache) Close() error { return nil }

// HSetAll 内存降级实现：hash 以 JSON 整体落一个键，仅保证接口完整（无外部消费者）。
func (c *memCache) HSetAll(_ context.Context, key string, m map[string]string) bool {
	raw, err := json.Marshal(m)
	if err != nil {
		return false
	}
	c.mu.Lock()
	c.items["hash:"+key] = memItem{val: raw, expire: time.Now().Add(30 * 24 * time.Hour)}
	c.mu.Unlock()
	return true
}

// redisCache Redis 实现。
type redisCache struct {
	rdb *redis.Client
}

func (c *redisCache) Get(ctx context.Context, key string) ([]byte, bool) {
	val, err := c.rdb.Get(ctx, key).Bytes()
	if err != nil {
		return nil, false // 含 redis.Nil 与网络错误，统一视为未命中
	}
	return val, true
}

func (c *redisCache) Set(ctx context.Context, key string, val []byte, ttl time.Duration) {
	if err := c.rdb.Set(ctx, key, val, ttl).Err(); err != nil {
		log.Printf("[cache] Redis 写入失败: %v", err)
	}
}

func (c *redisCache) Close() error { return c.rdb.Close() }

// HSetAll 先删后整表写入（pipeline 原子提交），保证 hash 与当前索引一致、无残留旧项。
func (c *redisCache) HSetAll(ctx context.Context, key string, m map[string]string) bool {
	pipe := c.rdb.Pipeline()
	pipe.Del(ctx, key)
	if len(m) > 0 {
		pipe.HSet(ctx, key, m)
	}
	if _, err := pipe.Exec(ctx); err != nil {
		log.Printf("[cache] Redis hash 写入失败: %v", err)
		return false
	}
	return true
}
