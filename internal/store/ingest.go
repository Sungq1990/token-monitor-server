package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// UsageItem 与客户端 collector 产出的结构一一对应；message_id 为空表示仅更新会话元信息。
type UsageItem struct {
	MessageID string  `json:"message_id"`
	SessionID string  `json:"session_id"`
	Model     string  `json:"model"`
	Provider  string  `json:"provider"`

	InputTokens      int64 `json:"input_tokens"`
	OutputTokens     int64 `json:"output_tokens"`
	CacheReadTokens  int64 `json:"cache_read_tokens"`
	CacheWriteTokens int64 `json:"cache_write_tokens"`
	ReasoningTokens  int64 `json:"reasoning_tokens"`
	TotalTokens      int64 `json:"total_tokens"`

	Cost       *float64   `json:"cost"`
	OccurredAt *time.Time `json:"occurred_at"`

	SessionTitle         string     `json:"session_title"`
	SessionStartedAt     *time.Time `json:"session_started_at"`
	SessionLastMessageAt *time.Time `json:"session_last_message_at"`
}

type Batch struct {
	Source string      `json:"source"`
	Items  []UsageItem `json:"items"`
}

type IngestRequest struct {
	DeviceID string  `json:"device_id"`
	Agent    string  `json:"agent"`
	Batches  []Batch `json:"batches"`
}

type IngestResult struct {
	UsageRows   int `json:"usage_rows"`
	SessionRows int `json:"session_rows"`
	Skipped     int `json:"skipped"`
}

type DeviceInfo struct {
	ID              string          `json:"device_id"`
	Name            string          `json:"name"`
	OS              string          `json:"os"`
	Hostname        string          `json:"hostname"`
	ClientVersion   string          `json:"client_version"`
	IntervalMinutes int             `json:"interval_minutes"`
	Agents          json.RawMessage `json:"agents"`
	Status          json.RawMessage `json:"status"`
}

// UpsertDevice 注册/心跳共用：更新设备元信息与 last_seen。
func (s *Store) UpsertDevice(ctx context.Context, d DeviceInfo, synced bool) error {
	if d.Agents == nil {
		d.Agents = json.RawMessage("[]")
	}
	if d.Status == nil {
		d.Status = json.RawMessage("{}")
	}
	if d.IntervalMinutes <= 0 {
		d.IntervalMinutes = 5
	}
	now := Now()
	var syncAt any
	if synced {
		syncAt = now
	}
	_, err := s.db.ExecContext(ctx, `
INSERT INTO devices (id, name, os, hostname, client_version, interval_minutes, agents_json, status_json, first_seen, last_seen, last_sync_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(id) DO UPDATE SET
  name = CASE WHEN excluded.name <> '' THEN excluded.name ELSE devices.name END,
  os = CASE WHEN excluded.os <> '' THEN excluded.os ELSE devices.os END,
  hostname = CASE WHEN excluded.hostname <> '' THEN excluded.hostname ELSE devices.hostname END,
  client_version = CASE WHEN excluded.client_version <> '' THEN excluded.client_version ELSE devices.client_version END,
  interval_minutes = excluded.interval_minutes,
  agents_json = CASE WHEN excluded.agents_json <> '[]' THEN excluded.agents_json ELSE devices.agents_json END,
  status_json = CASE WHEN excluded.status_json <> '{}' THEN excluded.status_json ELSE devices.status_json END,
  last_seen = excluded.last_seen,
  last_sync_at = COALESCE(excluded.last_sync_at, devices.last_sync_at)`,
		d.ID, d.Name, d.OS, d.Hostname, d.ClientVersion, d.IntervalMinutes,
		string(d.Agents), string(d.Status), now, now, syncAt)
	return err
}

// TouchDeviceSync 仅更新 last_seen / last_sync_at（ingest 成功后调用）。
func (s *Store) TouchDeviceSync(ctx context.Context, id string) error {
	now := Now()
	res, err := s.db.ExecContext(ctx, `UPDATE devices SET last_seen=?, last_sync_at=? WHERE id=?`, now, now, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		// 未注册就直接上报的设备：补一条最简记录
		_, err = s.db.ExecContext(ctx, `INSERT OR IGNORE INTO devices (id, first_seen, last_seen, last_sync_at) VALUES (?,?,?,?)`, id, now, now, now)
	}
	return err
}

const chunk = 400

// Ingest 幂等入库：token_usage 按 (device_id, agent, message_id) upsert，会话按 (device_id, agent, session_id) 合并。
func (s *Store) Ingest(ctx context.Context, req IngestRequest) (IngestResult, error) {
	var out IngestResult
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return out, err
	}
	defer tx.Rollback()

	for _, b := range req.Batches {
		// ---- 会话元信息合并 ----
		type sess struct {
			title, model  string
			started, last *time.Time
		}
		merged := map[string]*sess{}
		order := []string{}
		for _, it := range b.Items {
			if it.SessionID == "" {
				continue
			}
			cur, ok := merged[it.SessionID]
			if !ok {
				cur = &sess{}
				merged[it.SessionID] = cur
				order = append(order, it.SessionID)
			}
			if it.SessionTitle != "" {
				cur.title = it.SessionTitle
			}
			if it.Model != "" {
				cur.model = it.Model
			}
			cur.started = minT(cur.started, it.SessionStartedAt)
			cur.last = maxT(cur.last, it.SessionLastMessageAt)
			if it.OccurredAt != nil {
				cur.started = minT(cur.started, it.OccurredAt)
				cur.last = maxT(cur.last, it.OccurredAt)
			}
		}
		for _, sid := range order {
			m := merged[sid]
			_, err := tx.ExecContext(ctx, `
INSERT INTO agent_sessions (device_id, agent, session_id, title, model, started_at, last_message_at)
VALUES (?, ?, ?, NULLIF(?, ''), NULLIF(?, ''), ?, ?)
ON CONFLICT(device_id, agent, session_id) DO UPDATE SET
  title = COALESCE(excluded.title, agent_sessions.title),
  model = COALESCE(excluded.model, agent_sessions.model),
  started_at = COALESCE(MIN(excluded.started_at, agent_sessions.started_at), excluded.started_at, agent_sessions.started_at),
  last_message_at = COALESCE(MAX(excluded.last_message_at, agent_sessions.last_message_at), excluded.last_message_at, agent_sessions.last_message_at),
  updated_at = datetime('now','localtime')`,
				req.DeviceID, req.Agent, sid, m.title, m.model, FormatTime(m.started), FormatTime(m.last))
			if err != nil {
				return out, fmt.Errorf("session upsert: %w", err)
			}
			out.SessionRows++
		}

		// ---- token usage 批量 upsert ----
		items := make([]UsageItem, 0, len(b.Items))
		for _, it := range b.Items {
			if it.MessageID == "" {
				continue
			}
			if len(it.MessageID) > 191 {
				out.Skipped++
				continue
			}
			if it.TotalTokens == 0 {
				it.TotalTokens = it.InputTokens + it.OutputTokens + it.CacheReadTokens + it.CacheWriteTokens
			}
			items = append(items, it)
		}
		for i := 0; i < len(items); i += chunk {
			end := i + chunk
			if end > len(items) {
				end = len(items)
			}
			part := items[i:end]
			var sb strings.Builder
			sb.WriteString(`INSERT INTO token_usage (device_id, agent, session_id, message_id, model, provider, input_tokens, output_tokens, cache_read_tokens, cache_write_tokens, reasoning_tokens, total_tokens, cost, occurred_at) VALUES `)
			args := make([]any, 0, len(part)*14)
			for j, it := range part {
				if j > 0 {
					sb.WriteString(",")
				}
				sb.WriteString("(?,?,NULLIF(?,''),?,NULLIF(?,''),NULLIF(?,''),?,?,?,?,?,?,?,?)")
				var cost any
				if it.Cost != nil {
					cost = *it.Cost
				}
				args = append(args, req.DeviceID, req.Agent, it.SessionID, it.MessageID, it.Model, it.Provider,
					it.InputTokens, it.OutputTokens, it.CacheReadTokens, it.CacheWriteTokens, it.ReasoningTokens, it.TotalTokens,
					cost, FormatTime(it.OccurredAt))
			}
			sb.WriteString(` ON CONFLICT(device_id, agent, message_id) DO UPDATE SET
  session_id = COALESCE(excluded.session_id, token_usage.session_id),
  model = COALESCE(excluded.model, token_usage.model),
  provider = COALESCE(excluded.provider, token_usage.provider),
  input_tokens = excluded.input_tokens, output_tokens = excluded.output_tokens,
  cache_read_tokens = excluded.cache_read_tokens, cache_write_tokens = excluded.cache_write_tokens,
  reasoning_tokens = excluded.reasoning_tokens, total_tokens = excluded.total_tokens,
  cost = COALESCE(excluded.cost, token_usage.cost),
  occurred_at = COALESCE(excluded.occurred_at, token_usage.occurred_at)`)
			if _, err := tx.ExecContext(ctx, sb.String(), args...); err != nil {
				return out, fmt.Errorf("usage upsert: %w", err)
			}
			out.UsageRows += len(part)
		}
	}
	if err := tx.Commit(); err != nil {
		return out, err
	}
	return out, nil
}

func minT(a, b *time.Time) *time.Time {
	if a == nil {
		return b
	}
	if b == nil || a.Before(*b) {
		return a
	}
	return b
}

func maxT(a, b *time.Time) *time.Time {
	if a == nil {
		return b
	}
	if b == nil || a.After(*b) {
		return a
	}
	return b
}

// ---- 单价 ----

type PricingItem struct {
	Agent          string  `json:"agent"`
	Model          string  `json:"model"`
	Provider       string  `json:"provider,omitempty"`
	InputPerM      float64 `json:"input_per_m"`
	OutputPerM     float64 `json:"output_per_m"`
	CacheReadPerM  float64 `json:"cache_read_per_m"`
	CacheWritePerM float64 `json:"cache_write_per_m"`
	Configured     bool    `json:"configured"`
	UpdatedBy      string  `json:"updated_by,omitempty"`
}

func (s *Store) SavePricing(ctx context.Context, items []PricingItem, by string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, it := range items {
		if it.Agent == "" || it.Model == "" {
			continue
		}
		_, err := tx.ExecContext(ctx, `
INSERT INTO model_pricing (agent, model, input_per_m, output_per_m, cache_read_per_m, cache_write_per_m, updated_by)
VALUES (?,?,?,?,?,?,?)
ON CONFLICT(agent, model) DO UPDATE SET
  input_per_m=excluded.input_per_m, output_per_m=excluded.output_per_m,
  cache_read_per_m=excluded.cache_read_per_m, cache_write_per_m=excluded.cache_write_per_m,
  updated_by=excluded.updated_by, updated_at=datetime('now','localtime')`,
			it.Agent, it.Model, it.InputPerM, it.OutputPerM, it.CacheReadPerM, it.CacheWritePerM, by)
		if err != nil {
			return err
		}
	}
	return tx.Commit()
}

// ListPricing 返回「出现过的 agent+model」∪「已配置单价」，供设置页展示。
func (s *Store) ListPricing(ctx context.Context, deviceID string) ([]PricingItem, error) {
	byKey := map[string]*PricingItem{}
	var keys []string
	q := `SELECT agent, model, COALESCE(MAX(provider),'') FROM token_usage WHERE model IS NOT NULL AND model <> ''`
	var args []any
	if deviceID != "" {
		q += ` AND device_id = ?`
		args = append(args, deviceID)
	}
	q += ` GROUP BY agent, model`
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var a, m, p string
		if err := rows.Scan(&a, &m, &p); err != nil {
			rows.Close()
			return nil, err
		}
		k := a + "\x00" + m
		byKey[k] = &PricingItem{Agent: a, Model: m, Provider: p}
		keys = append(keys, k)
	}
	rows.Close()

	rows, err = s.db.QueryContext(ctx, `SELECT agent, model, input_per_m, output_per_m, cache_read_per_m, cache_write_per_m, updated_by FROM model_pricing`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var it PricingItem
		if err := rows.Scan(&it.Agent, &it.Model, &it.InputPerM, &it.OutputPerM, &it.CacheReadPerM, &it.CacheWritePerM, &it.UpdatedBy); err != nil {
			return nil, err
		}
		it.Configured = true
		k := it.Agent + "\x00" + it.Model
		if cur, ok := byKey[k]; ok {
			it.Provider = cur.Provider
			*cur = it
		} else if deviceID == "" {
			byKey[k] = &it
			keys = append(keys, k)
		}
	}
	out := make([]PricingItem, 0, len(keys))
	for _, k := range keys {
		it := byKey[k]
		if it.Provider == "" {
			it.Provider = DetectProvider(it.Model)
		}
		out = append(out, *it)
	}
	return out, nil
}

var providerRules = [][2]string{
	{"claude", "anthropic"}, {"anthropic", "anthropic"},
	{"gpt", "openai"}, {"o1", "openai"}, {"o3", "openai"}, {"o4", "openai"}, {"codex", "openai"}, {"openai", "openai"},
	{"gemini", "google"},
	{"glm", "zhipu"}, {"zhipu", "zhipu"},
	{"deepseek", "deepseek"},
	{"qwen", "alibaba"}, {"qwq", "alibaba"},
	{"kimi", "moonshot"}, {"moonshot", "moonshot"},
	{"grok", "xai"},
	{"doubao", "bytedance"}, {"seed", "bytedance"},
	{"minimax", "minimax"}, {"abab", "minimax"},
	{"mistral", "mistral"}, {"llama", "meta"},
	{"ernie", "baidu"}, {"hunyuan", "tencent"},
	{"step", "stepfun"}, {"yi-", "01ai"},
}

func DetectProvider(model string) string {
	m := strings.ToLower(model)
	if m == "" {
		return "unknown"
	}
	if i := strings.Index(m, "/"); i > 0 {
		return m[:i]
	}
	for _, r := range providerRules {
		if strings.Contains(m, r[0]) {
			return r[1]
		}
	}
	return "other"
}

var ErrNotFound = sql.ErrNoRows
