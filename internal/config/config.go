// Package config 负责 goepg 配置的定义、加载与默认值填充。
package config

import (
	"fmt"
	"os"
	"time"

	"gopkg.in/yaml.v3"
)

// Config 应用全量配置，与 config.yaml 一一对应。
type Config struct {
	Server  ServerConfig `yaml:"server"`
	MySQL   MySQLConfig  `yaml:"mysql"`
	Redis   RedisConfig  `yaml:"redis"`
	EPG     EPGConfig    `yaml:"epg"`
	Icon    IconConfig   `yaml:"icon"`
	Admin   AdminConfig  `yaml:"admin"`
	Sources []Source     `yaml:"sources"`
}

// ServerConfig HTTP 服务配置。
type ServerConfig struct {
	Addr     string `yaml:"addr"`
	Timezone string `yaml:"timezone"`
}

// MySQLConfig 数据库配置（要求 MySQL 5.6+）。
type MySQLConfig struct {
	DSN          string `yaml:"dsn"`
	MaxOpenConns int    `yaml:"max_open_conns"`
	MaxIdleConns int    `yaml:"max_idle_conns"`
}

// RedisConfig 缓存配置，Addr 为空则降级为进程内存缓存。
type RedisConfig struct {
	Addr     string        `yaml:"addr"`
	Password string        `yaml:"password"`
	DB       int           `yaml:"db"`
	CacheTTL time.Duration `yaml:"cache_ttl"`
}

// EPGConfig 采集与保留策略。
type EPGConfig struct {
	KeepDays        int           `yaml:"keep_days"`
	SyncInterval    time.Duration `yaml:"sync_interval"`
	SyncConcurrency int           `yaml:"sync_concurrency"`
	SyncOnStart     bool          `yaml:"sync_on_start"`
	FetchTimeout    time.Duration `yaml:"fetch_timeout"`
	MaxSourceSizeMB int           `yaml:"max_source_size_mb"`
	DefaultDuration time.Duration `yaml:"default_duration"`
}

// Source 单个 EPG 源。
type Source struct {
	Name        string `yaml:"name"`
	URL         string `yaml:"url"`
	Priority    int    `yaml:"priority"`
	Enabled     bool   `yaml:"enabled"`
	IntervalMin int    `yaml:"interval_min"` // 该源独立同步间隔（分钟），0 用全局
}

// IconConfig 频道图标库配置。
type IconConfig struct {
	Dir     string `yaml:"dir"`      // 图标目录（按地域子目录组织）
	BaseURL string `yaml:"base_url"` // 对外 icon URL 前缀，留空按请求 Host 动态生成
}

// AdminConfig 管理接口配置。
type AdminConfig struct {
	Token string `yaml:"token"` // 非空时管理 API 与管理页需携带该令牌
}

// Default 返回一份可直接运行的默认配置（无源）。
func Default() *Config {
	return &Config{
		Server: ServerConfig{Addr: ":8080", Timezone: "Asia/Shanghai"},
		MySQL:  MySQLConfig{DSN: "", MaxOpenConns: 20, MaxIdleConns: 5},
		Redis:  RedisConfig{Addr: "", CacheTTL: 5 * time.Minute},
		EPG: EPGConfig{
			KeepDays:        7,
			SyncInterval:    6 * time.Hour,
			SyncConcurrency: 2,
			SyncOnStart:     true,
			FetchTimeout:    3 * time.Minute,
			MaxSourceSizeMB: 200,
			DefaultDuration: 30 * time.Minute,
		},
	}
}

// Load 从 path 读取配置并校验，path 不存在时写出默认配置并返回错误提示。
func Load(path string) (*Config, error) {
	cfg := Default()
	raw, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			// 首次运行：落一份默认配置方便用户修改
			if def, e := yaml.Marshal(cfg); e == nil {
				_ = os.WriteFile(path, def, 0644)
			}
			return nil, fmt.Errorf("配置文件 %s 不存在，已生成默认模板，请修改后重新启动", path)
		}
		return nil, err
	}
	if err := yaml.Unmarshal(raw, cfg); err != nil {
		return nil, fmt.Errorf("解析配置文件失败: %w", err)
	}
	cfg.applyDefaults()
	if err := cfg.validate(); err != nil {
		return nil, err
	}
	return cfg, nil
}

// applyDefaults 补齐零值字段，保证行为可预期。
func (c *Config) applyDefaults() {
	if c.Server.Addr == "" {
		c.Server.Addr = ":8080"
	}
	if c.Server.Timezone == "" {
		c.Server.Timezone = "Asia/Shanghai"
	}
	if c.MySQL.MaxOpenConns <= 0 {
		c.MySQL.MaxOpenConns = 20
	}
	if c.MySQL.MaxIdleConns <= 0 {
		c.MySQL.MaxIdleConns = 5
	}
	if c.Redis.CacheTTL <= 0 {
		c.Redis.CacheTTL = 5 * time.Minute
	}
	if c.EPG.KeepDays <= 0 {
		c.EPG.KeepDays = 7
	}
	if c.EPG.SyncInterval <= 0 {
		c.EPG.SyncInterval = 6 * time.Hour
	}
	if c.EPG.SyncConcurrency <= 0 {
		c.EPG.SyncConcurrency = 2
	}
	if c.EPG.FetchTimeout <= 0 {
		c.EPG.FetchTimeout = 3 * time.Minute
	}
	if c.EPG.MaxSourceSizeMB <= 0 {
		c.EPG.MaxSourceSizeMB = 200
	}
	if c.EPG.DefaultDuration <= 0 {
		c.EPG.DefaultDuration = 30 * time.Minute
	}
	if c.Icon.Dir == "" {
		c.Icon.Dir = "data/icons"
	}
}

// validate 校验关键字段，尽早暴露配置错误。
func (c *Config) validate() error {
	if c.MySQL.DSN == "" {
		return fmt.Errorf("mysql.dsn 不能为空")
	}
	seen := make(map[string]bool, len(c.Sources))
	for i, s := range c.Sources {
		if !s.Enabled {
			continue
		}
		if s.Name == "" {
			return fmt.Errorf("sources[%d].name 不能为空", i)
		}
		if seen[s.Name] {
			return fmt.Errorf("源名称重复: %s", s.Name)
		}
		seen[s.Name] = true
		if s.URL == "" {
			return fmt.Errorf("源 %s 缺少 url", s.Name)
		}
	}
	return nil
}

// EnabledSources 返回启用的源列表。
func (c *Config) EnabledSources() []Source {
	out := make([]Source, 0, len(c.Sources))
	for _, s := range c.Sources {
		if s.Enabled {
			out = append(out, s)
		}
	}
	return out
}
