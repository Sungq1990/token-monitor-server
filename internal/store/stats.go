package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"math"
	"strings"
	"time"
)

// 所有使用 sumCols 的查询必须给 token_usage 起别名 t 并带上 pricingJoin
const sumCols = `
COALESCE(SUM(t.input_tokens),0)        AS input_tokens,
COALESCE(SUM(t.output_tokens),0)       AS output_tokens,
COALESCE(SUM(t.cache_read_tokens),0)   AS cache_read_tokens,
COALESCE(SUM(t.cache_write_tokens),0)  AS cache_write_tokens,
COALESCE(SUM(t.reasoning_tokens),0)    AS reasoning_tokens,
COALESCE(SUM(t.total_tokens),0)        AS total_tokens,
COALESCE(SUM(CASE WHEN mp.id IS NULL THEN COALESCE(t.cost,0) ELSE (
        t.input_tokens       * COALESCE(mp.input_per_m,0)
      + t.output_tokens      * COALESCE(mp.output_per_m,0)
      + t.cache_read_tokens  * COALESCE(mp.cache_read_per_m,0)
      + t.cache_write_tokens * COALESCE(mp.cache_write_per_m,0)
    ) / 1000000.0 END),0)              AS cost,
COUNT(t.id)                            AS requests,
COUNT(DISTINCT t.session_id)           AS sessions`

const pricingJoin = `LEFT JOIN model_pricing mp ON mp.agent = t.agent AND mp.model = t.model`

type Totals struct {
	InputTokens      int64   `json:"input_tokens"`
	OutputTokens     int64   `json:"output_tokens"`
	CacheReadTokens  int64   `json:"cache_read_tokens"`
	CacheWriteTokens int64   `json:"cache_write_tokens"`
	ReasoningTokens  int64   `json:"reasoning_tokens"`
	TotalTokens      int64   `json:"total_tokens"`
	Cost             float64 `json:"cost"`
	Requests         int64   `json:"requests"`
	Sessions         int64   `json:"sessions"`
}

type Filter struct {
	Start, End time.Time
	DeviceID   string
	Agent      string
	Model      string
}

func (f Filter) where() (string, []any) {
	cond := []string{"t.occurred_at >= ?", "t.occurred_at < ?"}
	args := []any{f.Start.Format(TimeLayout), f.End.Format(TimeLayout)}
	if f.DeviceID != "" {
		cond = append(cond, "t.device_id = ?")
		args = append(args, f.DeviceID)
	}
	if f.Agent != "" {
		cond = append(cond, "t.agent = ?")
		args = append(args, f.Agent)
	}
	if f.Model != "" {
		cond = append(cond, "t.model = ?")
		args = append(args, f.Model)
	}
	return strings.Join(cond, " AND "), args
}

func scanTotals(sc interface{ Scan(...any) error }, extra ...any) (Totals, error) {
	var t Totals
	dst := append(extra, &t.InputTokens, &t.OutputTokens, &t.CacheReadTokens, &t.CacheWriteTokens,
		&t.ReasoningTokens, &t.TotalTokens, &t.Cost, &t.Requests, &t.Sessions)
	err := sc.Scan(dst...)
	t.Cost = math.Round(t.Cost*10000) / 10000
	return t, err
}

func (s *Store) Totals(ctx context.Context, f Filter) (Totals, error) {
	cond, args := f.where()
	row := s.db.QueryRowContext(ctx, fmt.Sprintf(`SELECT %s FROM token_usage t %s WHERE %s`, sumCols, pricingJoin, cond), args...)
	return scanTotals(row)
}

type KeyedTotals struct {
	Key string `json:"key"`
	Totals
}

// Breakdown 按 agent / model / device_id 分组
func (s *Store) Breakdown(ctx context.Context, f Filter, group string) ([]KeyedTotals, error) {
	switch group {
	case "agent", "model", "device_id":
	default:
		return nil, fmt.Errorf("bad group %q", group)
	}
	cond, args := f.where()
	rows, err := s.db.QueryContext(ctx, fmt.Sprintf(
		`SELECT COALESCE(t.%s,'') AS k, %s FROM token_usage t %s WHERE %s GROUP BY t.%s ORDER BY total_tokens DESC`,
		group, sumCols, pricingJoin, cond, group), args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []KeyedTotals{}
	for rows.Next() {
		var k KeyedTotals
		t, err := scanTotals(rows, &k.Key)
		if err != nil {
			return nil, err
		}
		k.Totals = t
		out = append(out, k)
	}
	return out, rows.Err()
}

type DayTotals struct {
	Day string `json:"day"`
	Totals
}

// Daily 按时间桶聚合：day -> "2026-09-16"，hour -> "2026-09-16 14"，month -> "2026-09"
func (s *Store) Daily(ctx context.Context, f Filter, bucket string) ([]DayTotals, error) {
	var expr string
	switch bucket {
	case "hour":
		expr = "strftime('%Y-%m-%d %H', t.occurred_at)"
	case "month":
		expr = "strftime('%Y-%m', t.occurred_at)"
	default:
		expr = "strftime('%Y-%m-%d', t.occurred_at)"
	}
	cond, args := f.where()
	rows, err := s.db.QueryContext(ctx, fmt.Sprintf(
		`SELECT %s AS day, %s FROM token_usage t %s WHERE %s GROUP BY day ORDER BY day`,
		expr, sumCols, pricingJoin, cond), args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []DayTotals{}
	for rows.Next() {
		var d DayTotals
		var day sql.NullString
		t, err := scanTotals(rows, &day)
		if err != nil {
			return nil, err
		}
		d.Day = day.String
		d.Totals = t
		out = append(out, d)
	}
	return out, rows.Err()
}

type SessionRow struct {
	DeviceID      string  `json:"device_id"`
	Agent         string  `json:"agent"`
	SessionID     string  `json:"session_id"`
	Title         *string `json:"title"`
	Model         *string `json:"model"`
	StartedAt     *string `json:"started_at"`
	LastMessageAt *string `json:"last_message_at"`
	Totals
}

func (s *Store) Sessions(ctx context.Context, f Filter, limit, offset int) ([]SessionRow, error) {
	cond, args := f.where()
	args = append(args, limit, offset)
	rows, err := s.db.QueryContext(ctx, fmt.Sprintf(`
SELECT t.device_id, t.agent, COALESCE(t.session_id,''),
       MAX(s.title), MAX(COALESCE(t.model, s.model)),
       MIN(t.occurred_at), MAX(t.occurred_at), %s
FROM token_usage t
LEFT JOIN agent_sessions s ON s.device_id = t.device_id AND s.agent = t.agent AND s.session_id = t.session_id
%s
WHERE %s
GROUP BY t.device_id, t.agent, t.session_id
ORDER BY MAX(t.occurred_at) DESC
LIMIT ? OFFSET ?`, sumCols, pricingJoin, cond), args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []SessionRow{}
	for rows.Next() {
		var r SessionRow
		t, err := scanTotals(rows, &r.DeviceID, &r.Agent, &r.SessionID, &r.Title, &r.Model, &r.StartedAt, &r.LastMessageAt)
		if err != nil {
			return nil, err
		}
		r.Totals = t
		if r.Title == nil || *r.Title == "" {
			if r.SessionID != "" {
				sid := r.SessionID
				if len(sid) > 24 {
					sid = sid[:24]
				}
				tt := "(" + sid + "...)"
				r.Title = &tt
			}
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

type UsageRow struct {
	ID               int64    `json:"id"`
	DeviceID         string   `json:"device_id"`
	Agent            string   `json:"agent"`
	SessionID        *string  `json:"session_id"`
	MessageID        string   `json:"message_id"`
	Model            *string  `json:"model"`
	Provider         *string  `json:"provider"`
	InputTokens      int64    `json:"input_tokens"`
	OutputTokens     int64    `json:"output_tokens"`
	CacheReadTokens  int64    `json:"cache_read_tokens"`
	CacheWriteTokens int64    `json:"cache_write_tokens"`
	ReasoningTokens  int64    `json:"reasoning_tokens"`
	TotalTokens      int64    `json:"total_tokens"`
	Cost             *float64 `json:"cost"`
	OccurredAt       *string  `json:"occurred_at"`
}

func (s *Store) Usage(ctx context.Context, f Filter, sessionID string, limit, offset int) ([]UsageRow, error) {
	cond, args := f.where()
	if sessionID != "" {
		cond += " AND t.session_id = ?"
		args = append(args, sessionID)
	}
	args = append(args, limit, offset)
	rows, err := s.db.QueryContext(ctx, fmt.Sprintf(`
SELECT t.id, t.device_id, t.agent, t.session_id, t.message_id, t.model, t.provider,
       t.input_tokens, t.output_tokens, t.cache_read_tokens, t.cache_write_tokens, t.reasoning_tokens, t.total_tokens,
       CASE WHEN mp.id IS NULL THEN t.cost ELSE (
         t.input_tokens*mp.input_per_m + t.output_tokens*mp.output_per_m
       + t.cache_read_tokens*mp.cache_read_per_m + t.cache_write_tokens*mp.cache_write_per_m)/1000000.0 END,
       t.occurred_at
FROM token_usage t %s WHERE %s ORDER BY t.occurred_at DESC, t.id DESC LIMIT ? OFFSET ?`, pricingJoin, cond), args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []UsageRow{}
	for rows.Next() {
		var r UsageRow
		if err := rows.Scan(&r.ID, &r.DeviceID, &r.Agent, &r.SessionID, &r.MessageID, &r.Model, &r.Provider,
			&r.InputTokens, &r.OutputTokens, &r.CacheReadTokens, &r.CacheWriteTokens, &r.ReasoningTokens, &r.TotalTokens,
			&r.Cost, &r.OccurredAt); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// ---- 设备 ----

type Device struct {
	ID              string          `json:"device_id"`
	Name            string          `json:"name"`
	OS              string          `json:"os"`
	Hostname        string          `json:"hostname"`
	ClientVersion   string          `json:"client_version"`
	IntervalMinutes int             `json:"interval_minutes"`
	Agents          json.RawMessage `json:"agents"`
	Status          json.RawMessage `json:"status"`
	FirstSeen       string          `json:"first_seen"`
	LastSeen        string          `json:"last_seen"`
	LastSyncAt      *string         `json:"last_sync_at"`
	Online          bool            `json:"online"`
	UsageRows       int64           `json:"usage_rows"`
}

func (s *Store) Devices(ctx context.Context) ([]Device, error) {
	rows, err := s.db.QueryContext(ctx, `
SELECT d.id, d.name, d.os, d.hostname, d.client_version, d.interval_minutes, d.agents_json, d.status_json,
       d.first_seen, d.last_seen, d.last_sync_at,
       (SELECT COUNT(*) FROM token_usage u WHERE u.device_id = d.id)
FROM devices d ORDER BY d.last_seen DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Device{}
	now := time.Now()
	for rows.Next() {
		var d Device
		var agents, status string
		if err := rows.Scan(&d.ID, &d.Name, &d.OS, &d.Hostname, &d.ClientVersion, &d.IntervalMinutes, &agents, &status,
			&d.FirstSeen, &d.LastSeen, &d.LastSyncAt, &d.UsageRows); err != nil {
			return nil, err
		}
		d.Agents = json.RawMessage(agents)
		d.Status = json.RawMessage(status)
		if !json.Valid(d.Agents) {
			d.Agents = json.RawMessage("[]")
		}
		if !json.Valid(d.Status) {
			d.Status = json.RawMessage("{}")
		}
		if ls, err := time.ParseInLocation(TimeLayout, d.LastSeen, time.Local); err == nil {
			// 超过 2 个采集周期（至少 3 分钟）没有心跳视为离线
			grace := time.Duration(d.IntervalMinutes)*2*time.Minute + time.Minute
			if grace < 3*time.Minute {
				grace = 3 * time.Minute
			}
			d.Online = now.Sub(ls) <= grace
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

func (s *Store) DeleteDevice(ctx context.Context, id string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, q := range []string{
		`DELETE FROM token_usage WHERE device_id = ?`,
		`DELETE FROM agent_sessions WHERE device_id = ?`,
		`DELETE FROM devices WHERE id = ?`,
	} {
		if _, err := tx.ExecContext(ctx, q, id); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *Store) LastSyncByDevice(ctx context.Context) (map[string]*string, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, last_sync_at FROM devices`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]*string{}
	for rows.Next() {
		var id string
		var ts *string
		if err := rows.Scan(&id, &ts); err != nil {
			return nil, err
		}
		out[id] = ts
	}
	return out, nil
}
