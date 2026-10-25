// Package cache 提供接口响应缓存：配置了 Redis 用 Redis，否则降级为进程内存缓存。
package cache

import (
	"context"
	"encoding/json"
	"log"
	"strings"
	"sync"
	"time"

	"github.com/redis/go-redis/v9"

	"goepg/internal/config"
)

// Cache 缓存接口，value 为完整响应字节。
type Cache interface {
	Get(ctx context.Context, key string) ([]byte, bool)
	Set(ctx context.Context, key string, val []byte, ttl time.Duration)
	// MSet 批量写入同 TTL 键值（响应预热用），redis 实现走 pipeline 降低往返。
	MSet(ctx context.Context, kvs []KV, ttl time.Duration)
	// ClearPrefix 删除指定前缀的全部键（源删除后清理孤儿缓存键：
	// 预热只写现存频道，已删频道的旧键不会被覆盖）。
	ClearPrefix(ctx context.Context, prefix string)
	// HSetAll 整表替换写入 hash（先删后写），用于发布共享索引；成功返回 true。
	HSetAll(ctx context.Context, key string, m map[string]string) bool
	Close() error
}

// KV 批量写入条目（MSet 用）。
type KV struct {
	Key string
	Val []byte
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
		// timeline 热缓存为数 MB 单键，默认 3s 在慢网络下读取超时会被误判为
		// 未命中，反而触发更慢的全量重建；放宽到 15s
		ReadTimeout:  15 * time.Second,
		WriteTimeout: 15 * time.Second,
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

func (c *memCache) MSet(_ context.Context, kvs []KV, ttl time.Duration) {
	for _, kv := range kvs {
		c.Set(context.Background(), kv.Key, kv.Val, ttl)
	}
}

func (c *memCache) ClearPrefix(_ context.Context, prefix string) {
	c.mu.Lock()
	for k := range c.items {
		if strings.HasPrefix(k, prefix) {
			delete(c.items, k)
		}
	}
	c.mu.Unlock()
}

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

// MSet pipeline 分批写入（每批 500），大批量键值一次网络往返批次完成。
func (c *redisCache) MSet(ctx context.Context, kvs []KV, ttl time.Duration) {
	const batch = 500
	for i := 0; i < len(kvs); i += batch {
		end := min(i+batch, len(kvs))
		pipe := c.rdb.Pipeline()
		for _, kv := range kvs[i:end] {
			pipe.Set(ctx, kv.Key, kv.Val, ttl)
		}
		if _, err := pipe.Exec(ctx); err != nil {
			log.Printf("[cache] Redis 批量写入失败: %v", err)
		}
	}
}

// ClearPrefix SCAN 遍历前缀键并 UNLINK（异步删除，不阻塞 Redis）。
func (c *redisCache) ClearPrefix(ctx context.Context, prefix string) {
	var cursor uint64
	del := 0
	for {
		keys, next, err := c.rdb.Scan(ctx, cursor, prefix+"*", 1000).Result()
		if err != nil {
			log.Printf("[cache] Redis SCAN 失败: %v", err)
			return
		}
		if len(keys) > 0 {
			if n, err := c.rdb.Unlink(ctx, keys...).Result(); err != nil {
				log.Printf("[cache] Redis 删除失败: %v", err)
				return
			} else {
				del += int(n)
			}
		}
		cursor = next
		if cursor == 0 {
			break
		}
	}
	log.Printf("[cache] 已清理前缀 %q 共 %d 键", prefix, del)
}

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
