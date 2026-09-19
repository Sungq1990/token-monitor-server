package store

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

// Store 封装 SQLite 连接。单写多读：SQLite 用 WAL + busy_timeout 即可满足多设备上报。
type Store struct {
	db *sql.DB
}

const schema = `
CREATE TABLE IF NOT EXISTS devices (
  id             TEXT PRIMARY KEY,
  name           TEXT NOT NULL DEFAULT '',
  os             TEXT NOT NULL DEFAULT '',
  hostname       TEXT NOT NULL DEFAULT '',
  client_version TEXT NOT NULL DEFAULT '',
  interval_minutes INTEGER NOT NULL DEFAULT 5,
  agents_json    TEXT NOT NULL DEFAULT '[]',
  status_json    TEXT NOT NULL DEFAULT '{}',
  config_json    TEXT NOT NULL DEFAULT '',
  first_seen     TEXT NOT NULL,
  last_seen      TEXT NOT NULL,
  last_sync_at   TEXT
);

CREATE TABLE IF NOT EXISTS agent_sessions (
  id              INTEGER PRIMARY KEY AUTOINCREMENT,
  device_id       TEXT NOT NULL,
  agent           TEXT NOT NULL,
  session_id      TEXT NOT NULL,
  title           TEXT,
  model           TEXT,
  started_at      TEXT,
  last_message_at TEXT,
  created_at      TEXT NOT NULL DEFAULT (datetime('now','localtime')),
  updated_at      TEXT NOT NULL DEFAULT (datetime('now','localtime')),
  UNIQUE(device_id, agent, session_id)
);

CREATE TABLE IF NOT EXISTS token_usage (
  id                 INTEGER PRIMARY KEY AUTOINCREMENT,
  device_id          TEXT NOT NULL,
  agent              TEXT NOT NULL,
  session_id         TEXT,
  message_id         TEXT NOT NULL,
  model              TEXT,
  provider           TEXT,
  input_tokens       INTEGER NOT NULL DEFAULT 0,
  output_tokens      INTEGER NOT NULL DEFAULT 0,
  cache_read_tokens  INTEGER NOT NULL DEFAULT 0,
  cache_write_tokens INTEGER NOT NULL DEFAULT 0,
  reasoning_tokens   INTEGER NOT NULL DEFAULT 0,
  total_tokens       INTEGER NOT NULL DEFAULT 0,
  cost               REAL,
  occurred_at        TEXT,
  created_at         TEXT NOT NULL DEFAULT (datetime('now','localtime')),
  UNIQUE(device_id, agent, message_id)
);
CREATE INDEX IF NOT EXISTS idx_usage_occurred ON token_usage(occurred_at);
CREATE INDEX IF NOT EXISTS idx_usage_device_occurred ON token_usage(device_id, occurred_at);
CREATE INDEX IF NOT EXISTS idx_usage_agent_occurred ON token_usage(agent, occurred_at);
CREATE INDEX IF NOT EXISTS idx_usage_model_occurred ON token_usage(model, occurred_at);
CREATE INDEX IF NOT EXISTS idx_usage_session ON token_usage(device_id, agent, session_id);

-- 模型单价：¥ / 百万 token，按 agent+model 全局生效（各客户端推送，后写覆盖）
CREATE TABLE IF NOT EXISTS model_pricing (
  id               INTEGER PRIMARY KEY AUTOINCREMENT,
  agent            TEXT NOT NULL,
  model            TEXT NOT NULL,
  input_per_m      REAL NOT NULL DEFAULT 0,
  output_per_m     REAL NOT NULL DEFAULT 0,
  cache_read_per_m REAL NOT NULL DEFAULT 0,
  cache_write_per_m REAL NOT NULL DEFAULT 0,
  updated_by       TEXT NOT NULL DEFAULT '',
  updated_at       TEXT NOT NULL DEFAULT (datetime('now','localtime')),
  UNIQUE(agent, model)
);
`

func Open(path string) (*Store, error) {
	if dir := filepath.Dir(path); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, fmt.Errorf("create data dir: %w", err)
		}
	}
	dsn := fmt.Sprintf("file:%s?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=foreign_keys(ON)&_pragma=synchronous(NORMAL)", path)
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	// modernc sqlite 单连接写更稳；读多时也够用
	db.SetMaxOpenConns(1)
	db.SetConnMaxLifetime(0)
	if _, err := db.Exec(schema); err != nil {
		db.Close()
		return nil, fmt.Errorf("migrate: %w", err)
	}
	// 旧库升级：v1.0 没有 config_json 列
	if _, err := db.Exec(`ALTER TABLE devices ADD COLUMN config_json TEXT NOT NULL DEFAULT ''`); err != nil {
		if !strings.Contains(err.Error(), "duplicate column") {
			db.Close()
			return nil, fmt.Errorf("migrate config_json: %w", err)
		}
	}
	return &Store{db: db}, nil
}

func (s *Store) Close() error { return s.db.Close() }

func (s *Store) Ping(ctx context.Context) error { return s.db.PingContext(ctx) }

// 存储统一用服务器本地时区的 naive 字符串，便于 SQLite strftime 按天/小时分桶
const TimeLayout = "2006-01-02 15:04:05"

func FormatTime(t *time.Time) any {
	if t == nil || t.IsZero() {
		return nil
	}
	return t.Local().Format(TimeLayout)
}

func Now() string { return time.Now().Format(TimeLayout) }
