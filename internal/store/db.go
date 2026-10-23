// Package store 提供 MySQL 5.6 兼容的数据访问层。
//
// 兼容性要点：
//   - utf8mb4 下 InnoDB 索引前缀最长 767 字节（innodb_large_prefix 默认关闭），
//     因此参与索引的列宽均受控：source(48)+xmltv_id(128)、name(128)
//   - 不使用 JSON / CHECK 等 5.6 不支持的特性
//   - 大量删除使用 DELETE ... LIMIT 分批执行
package store

import (
	"database/sql"
	"fmt"
	"time"

	"github.com/go-sql-driver/mysql"
)

// Store MySQL 数据访问入口。
type Store struct {
	db *sql.DB
}

// Open 建立连接池并强制统一 DSN 关键参数（parseTime、loc=配置时区），保证
// DATETIME 与 Go time.Time 的时区换算一致。
// 目标库不存在时自动创建（幂等，utf8mb4），免去部署前的手工建库步骤。
func Open(dsn string, tz *time.Location, maxOpen, maxIdle int) (*Store, error) {
	cfg, err := mysql.ParseDSN(dsn)
	if err != nil {
		return nil, fmt.Errorf("无效的 MySQL DSN: %w", err)
	}
	cfg.ParseTime = true
	cfg.Loc = tz
	cfg.Params = map[string]string{"charset": "utf8mb4"}

	if dbName := cfg.DBName; dbName != "" {
		cfg.DBName = "" // 不指定库先连服务端，用于自动建库
		admin, err := sql.Open("mysql", cfg.FormatDSN())
		if err != nil {
			return nil, err
		}
		_, err = admin.Exec("CREATE DATABASE IF NOT EXISTS `" + dbName +
			"` DEFAULT CHARACTER SET utf8mb4 COLLATE utf8mb4_general_ci")
		admin.Close()
		if err != nil {
			return nil, fmt.Errorf("自动建库 %s 失败（账号可能缺少 CREATE 权限，可先手动执行 "+
				"CREATE DATABASE %s DEFAULT CHARACTER SET utf8mb4）: %w", dbName, dbName, err)
		}
		cfg.DBName = dbName
	}

	db, err := sql.Open("mysql", cfg.FormatDSN())
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(maxOpen)
	db.SetMaxIdleConns(maxIdle)
	db.SetConnMaxLifetime(time.Hour)
	if err := db.Ping(); err != nil {
		return nil, fmt.Errorf("MySQL 连接失败: %w", err)
	}
	return &Store{db: db}, nil
}

// Close 关闭连接池。
func (s *Store) Close() error { return s.db.Close() }

// Migrate 建表（幂等），无需 ORM，纯 SQL 最可控。
// 注意：驱动默认不支持多语句，逐条执行。
func (s *Store) Migrate() error {
	schema := []string{
		`CREATE TABLE IF NOT EXISTS epg_channels (
  id         INT UNSIGNED  NOT NULL AUTO_INCREMENT,
  source     VARCHAR(48)   NOT NULL COMMENT '所属源名称',
  priority   INT           NOT NULL DEFAULT 0 COMMENT '源优先级，越小越优先',
  xmltv_id   VARCHAR(128)  NOT NULL COMMENT 'XMLTV channel id',
  name       VARCHAR(128)  NOT NULL COMMENT '频道显示名，对外查询键',
  icon       VARCHAR(512)  NOT NULL DEFAULT '',
  updated_at DATETIME      NOT NULL,
  PRIMARY KEY (id),
  UNIQUE KEY uk_source_xmltv (source, xmltv_id),
  KEY idx_name (name)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4`,
		`CREATE TABLE IF NOT EXISTS epg_programmes (
  id         BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
  channel_id INT UNSIGNED    NOT NULL,
  start      DATETIME        NOT NULL,
  stop       DATETIME        NOT NULL,
  title      VARCHAR(255)    NOT NULL,
  sub_title  VARCHAR(255)    NOT NULL DEFAULT '',
  descr      TEXT            NULL,
  category   VARCHAR(128)    NOT NULL DEFAULT '',
  PRIMARY KEY (id),
  UNIQUE KEY uk_channel_start (channel_id, start),
  KEY idx_start (start)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4`,
		`CREATE TABLE IF NOT EXISTS epg_sources (
  id           INT UNSIGNED  NOT NULL AUTO_INCREMENT,
  name         VARCHAR(48)   NOT NULL,
  url          VARCHAR(512)  NOT NULL,
  priority     INT           NOT NULL DEFAULT 10,
  interval_min INT           NOT NULL DEFAULT 0 COMMENT '独立同步间隔(分钟)，0用全局',
  enabled      TINYINT       NOT NULL DEFAULT 1,
  created_at   DATETIME      NOT NULL,
  updated_at   DATETIME      NOT NULL,
  PRIMARY KEY (id),
  UNIQUE KEY uk_name (name)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4`,
		`CREATE TABLE IF NOT EXISTS epg_bindings (
  id                INT UNSIGNED NOT NULL AUTO_INCREMENT,
  channel_name      VARCHAR(128) NOT NULL COMMENT '对外频道名',
  target_channel_id INT UNSIGNED NOT NULL COMMENT '锁定的 epg_channels.id',
  created_at        DATETIME     NOT NULL,
  updated_at        DATETIME     NOT NULL,
  PRIMARY KEY (id),
  UNIQUE KEY uk_channel_name (channel_name)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4`,
		`CREATE TABLE IF NOT EXISTS epg_hidden_channels (
  channel_name VARCHAR(128) NOT NULL COMMENT '对外频道名（存在即隐藏，不在首页时间轴显示）',
  created_at   DATETIME     NOT NULL,
  PRIMARY KEY (channel_name)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4`,
	}
	for _, ddl := range schema {
		if _, err := s.db.Exec(ddl); err != nil {
			return fmt.Errorf("建表失败: %w", err)
		}
	}
	return nil
}
