package store

import (
	"context"
	"database/sql"
	"errors"
)

// ErrNoConfig 设备存在但没有存储过客户端配置
var ErrNoConfig = errors.New("device config not set")

// DeviceConfig 读取某设备的客户端配置（原样 JSON 字符串）。
// 客户端首次注册前或从未推送过配置时返回 ErrNoConfig。
func (s *Store) DeviceConfig(ctx context.Context, deviceID string) (string, error) {
	var cfg string
	err := s.db.QueryRowContext(ctx, `SELECT config_json FROM devices WHERE id = ?`, deviceID).Scan(&cfg)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrNoConfig
	}
	if err != nil {
		return "", err
	}
	if cfg == "" {
		return "", ErrNoConfig
	}
	return cfg, nil
}

// SaveDeviceConfig 保存客户端推送的配置原文（服务端只存不解释）。
// 设备尚未注册时也接受（先建一行占位，等注册请求补全字段）。
func (s *Store) SaveDeviceConfig(ctx context.Context, deviceID, cfgJSON string) error {
	if _, err := s.db.ExecContext(ctx, `
INSERT INTO devices (id, name, os, hostname, client_version, config_json, first_seen, last_seen)
VALUES (?, '', '', '', '', ?, datetime('now','localtime'), datetime('now','localtime'))
ON CONFLICT(id) DO UPDATE SET config_json = excluded.config_json`, deviceID, cfgJSON); err != nil {
		return err
	}
	return nil
}
