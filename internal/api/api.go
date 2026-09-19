package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"token-monitor-server/internal/store"
)

const Version = "1.0.0"

var (
	agentRe  = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,31}$`)
	deviceRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.:-]{0,63}$`)
)

type Server struct {
	st     *store.Store
	static fs.FS
	mux    *http.ServeMux
}

func New(st *store.Store, static fs.FS) *Server {
	s := &Server{st: st, static: static, mux: http.NewServeMux()}
	s.routes()
	return s
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	start := time.Now()
	// 允许客户端 / 其他前端跨域调用；无鉴权，本身就是内网工具
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, OPTIONS")
	w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
	if r.Method == http.MethodOptions {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	s.mux.ServeHTTP(w, r)
	if strings.HasPrefix(r.URL.Path, "/api/") && r.Method != http.MethodGet {
		log.Printf("%s %s %s", r.Method, r.URL.Path, time.Since(start).Round(time.Millisecond))
	}
}

func (s *Server) routes() {
	m := s.mux
	// 客户端上报接口
	m.HandleFunc("POST /api/v1/devices/register", s.handleRegister)
	m.HandleFunc("POST /api/v1/devices/heartbeat", s.handleRegister)
	m.HandleFunc("POST /api/v1/ingest", s.handleIngest)
	m.HandleFunc("GET /api/v1/devices/{id}/config", s.handleGetDeviceConfig)
	m.HandleFunc("PUT /api/v1/devices/{id}/config", s.handlePutDeviceConfig)
	m.HandleFunc("GET /api/v1/pricing", s.handleGetPricing)
	m.HandleFunc("PUT /api/v1/pricing", s.handlePutPricing)

	// 面板接口
	m.HandleFunc("GET /api/health", s.handleHealth)
	m.HandleFunc("GET /api/devices", s.handleDevices)
	m.HandleFunc("GET /api/devices/{id}/config", s.handleGetDeviceConfig)
	m.HandleFunc("DELETE /api/devices/{id}", s.handleDeleteDevice)
	m.HandleFunc("GET /api/pricing", s.handleGetPricing)
	m.HandleFunc("POST /api/pricing", s.handlePutPricing)
	m.HandleFunc("GET /api/stats/today", s.handleToday)
	m.HandleFunc("GET /api/stats/range", s.handleRange)
	m.HandleFunc("GET /api/stats/agents", s.handleAgents)
	m.HandleFunc("GET /api/stats/models", s.handleModels)
	m.HandleFunc("GET /api/stats/devices", s.handleStatsDevices)
	m.HandleFunc("GET /api/stats/sessions", s.handleSessions)
	m.HandleFunc("GET /api/usage", s.handleUsage)
	m.HandleFunc("GET /api/session/{agent}/{sid}", s.handleSessionDetail)

	m.Handle("GET /static/", http.StripPrefix("/static/", http.FileServer(http.FS(s.static))))
	// favicon：与客户端同一张 appicon 生成的 PNG
	m.HandleFunc("GET /favicon.ico", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		w.Header().Set("Cache-Control", "public, max-age=86400")
		http.ServeFileFS(w, r, s.static, "favicon.png")
	})
	m.HandleFunc("GET /favicon.png", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		w.Header().Set("Cache-Control", "public, max-age=86400")
		http.ServeFileFS(w, r, s.static, "favicon.png")
	})
	m.HandleFunc("GET /{$}", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-cache")
		http.ServeFileFS(w, r, s.static, "index.html")
	})
}

// ---- helpers ----

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, code int, msg string) {
	writeJSON(w, code, map[string]any{"detail": msg})
}

func readJSON(w http.ResponseWriter, r *http.Request, v any, limit int64) error {
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, limit))
	if err := dec.Decode(v); err != nil {
		return fmt.Errorf("invalid JSON body: %w", err)
	}
	return nil
}

func ctxOf(r *http.Request) (context.Context, context.CancelFunc) {
	return context.WithTimeout(r.Context(), 60*time.Second)
}

// parseDT 接受 YYYY-MM-DD 或 ISO；纯日期 end 取次日 00:00（闭开区间）
func parseDT(s string, end bool, def time.Time) (time.Time, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return def, nil
	}
	if len(s) == 10 {
		d, err := time.ParseInLocation("2006-01-02", s, time.Local)
		if err != nil {
			return def, fmt.Errorf("bad date %q", s)
		}
		if end {
			return d.AddDate(0, 0, 1), nil
		}
		return d, nil
	}
	for _, layout := range []string{time.RFC3339, "2006-01-02T15:04:05", "2006-01-02 15:04:05", "2006-01-02T15:04"} {
		if t, err := time.ParseInLocation(layout, s, time.Local); err == nil {
			return t, nil
		}
	}
	return def, fmt.Errorf("bad datetime %q", s)
}

func (s *Server) filter(r *http.Request) (store.Filter, error) {
	q := r.URL.Query()
	now := time.Now()
	today0 := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.Local)
	start, err := parseDT(q.Get("start"), false, today0)
	if err != nil {
		return store.Filter{}, err
	}
	end, err := parseDT(q.Get("end"), true, now.Add(time.Second))
	if err != nil {
		return store.Filter{}, err
	}
	return store.Filter{
		Start: start, End: end,
		DeviceID: strings.TrimSpace(q.Get("device_id")),
		Agent:    strings.TrimSpace(q.Get("agent")),
		Model:    strings.TrimSpace(q.Get("model")),
	}, nil
}

func intParam(r *http.Request, key string, def, min, max int) int {
	v, err := strconv.Atoi(r.URL.Query().Get(key))
	if err != nil {
		return def
	}
	if v < min {
		return min
	}
	if v > max {
		return max
	}
	return v
}

// ---- 客户端接口 ----

type registerReq struct {
	store.DeviceInfo
}

func (s *Server) handleRegister(w http.ResponseWriter, r *http.Request) {
	var req registerReq
	if err := readJSON(w, r,&req, 1<<20); err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	req.ID = strings.TrimSpace(req.ID)
	if !deviceRe.MatchString(req.ID) {
		writeErr(w, 422, "device_id 非法（1-64 位字母数字 _ . : -）")
		return
	}
	if len(req.Name) > 128 {
		req.Name = req.Name[:128]
	}
	if req.Agents != nil && !json.Valid(req.Agents) {
		writeErr(w, 422, "agents 必须是 JSON")
		return
	}
	if req.Status != nil && !json.Valid(req.Status) {
		writeErr(w, 422, "status 必须是 JSON")
		return
	}
	ctx, cancel := ctxOf(r)
	defer cancel()
	if err := s.st.UpsertDevice(ctx, req.DeviceInfo, false); err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true, "device_id": req.ID, "server_version": Version, "server_time": time.Now().Format(time.RFC3339)})
}

func (s *Server) handleIngest(w http.ResponseWriter, r *http.Request) {
	var req store.IngestRequest
	if err := readJSON(w, r,&req, 64<<20); err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	req.DeviceID = strings.TrimSpace(req.DeviceID)
	req.Agent = strings.TrimSpace(req.Agent)
	if !deviceRe.MatchString(req.DeviceID) {
		writeErr(w, 422, "device_id 非法")
		return
	}
	if !agentRe.MatchString(req.Agent) {
		writeErr(w, 422, "agent 非法")
		return
	}
	n := 0
	for _, b := range req.Batches {
		n += len(b.Items)
	}
	if n > 200000 {
		writeErr(w, 413, "单次上报条数过多，请分批")
		return
	}
	ctx, cancel := ctxOf(r)
	defer cancel()
	res, err := s.st.Ingest(ctx, req)
	if err != nil {
		log.Printf("ingest failed device=%s agent=%s: %v", req.DeviceID, req.Agent, err)
		writeErr(w, 500, err.Error())
		return
	}
	if err := s.st.TouchDeviceSync(ctx, req.DeviceID); err != nil {
		log.Printf("touch device %s: %v", req.DeviceID, err)
	}
	writeJSON(w, 200, res)
}

func (s *Server) handleGetPricing(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := ctxOf(r)
	defer cancel()
	items, err := s.st.ListPricing(ctx, strings.TrimSpace(r.URL.Query().Get("device_id")))
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"pricing": items})
}

func (s *Server) handlePutPricing(w http.ResponseWriter, r *http.Request) {
	var body struct {
		DeviceID string              `json:"device_id"`
		Items    []store.PricingItem `json:"items"`
	}
	// 兼容旧面板：直接 POST 一个数组
	raw := json.RawMessage{}
	if err := readJSON(w, r,&raw, 4<<20); err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	if len(raw) > 0 && raw[0] == '[' {
		if err := json.Unmarshal(raw, &body.Items); err != nil {
			writeErr(w, 400, err.Error())
			return
		}
	} else if err := json.Unmarshal(raw, &body); err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	for i := range body.Items {
		it := &body.Items[i]
		it.Agent, it.Model = strings.TrimSpace(it.Agent), strings.TrimSpace(it.Model)
		if it.Agent == "" || it.Model == "" || len(it.Model) > 128 || !agentRe.MatchString(it.Agent) {
			writeErr(w, 422, fmt.Sprintf("第 %d 条 agent/model 非法", i+1))
			return
		}
		for _, v := range []float64{it.InputPerM, it.OutputPerM, it.CacheReadPerM, it.CacheWritePerM} {
			if v < 0 || v > 1e6 {
				writeErr(w, 422, fmt.Sprintf("第 %d 条单价超出范围", i+1))
				return
			}
		}
	}
	ctx, cancel := ctxOf(r)
	defer cancel()
	by := body.DeviceID
	if by == "" {
		by = "web"
	}
	if err := s.st.SavePricing(ctx, body.Items, by); err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"saved": len(body.Items)})
}

// ---- 客户端配置存取（配置以服务端为准，本地文件只是缓存） ----

func deviceIDOf(r *http.Request) (string, bool) {
	id := r.PathValue("id")
	if !deviceRe.MatchString(id) {
		return "", false
	}
	return id, true
}

func (s *Server) handleGetDeviceConfig(w http.ResponseWriter, r *http.Request) {
	id, ok := deviceIDOf(r)
	if !ok {
		writeErr(w, 422, "非法设备标识")
		return
	}
	ctx, cancel := ctxOf(r)
	defer cancel()
	cfg, err := s.st.DeviceConfig(ctx, id)
	if errors.Is(err, store.ErrNoConfig) {
		writeJSON(w, 200, map[string]any{"device_id": id, "config": nil})
		return
	}
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	var raw json.RawMessage = []byte(cfg)
	writeJSON(w, 200, map[string]any{"device_id": id, "config": raw})
}

func (s *Server) handlePutDeviceConfig(w http.ResponseWriter, r *http.Request) {
	id, ok := deviceIDOf(r)
	if !ok {
		writeErr(w, 422, "非法设备标识")
		return
	}
	var raw json.RawMessage
	if err := readJSON(w, r, &raw, 1<<20); err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	// 只要求是合法 JSON 对象，字段由客户端负责（服务端只存不解释）
	if !json.Valid(raw) || len(raw) == 0 || raw[0] != '{' {
		writeErr(w, 422, "配置必须是 JSON 对象")
		return
	}
	ctx, cancel := ctxOf(r)
	defer cancel()
	if err := s.st.SaveDeviceConfig(ctx, id, string(raw)); err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true, "device_id": id})
}

// ---- 面板接口 ----

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := ctxOf(r)
	defer cancel()
	if err := s.st.Ping(ctx); err != nil {
		writeErr(w, 503, "sqlite: "+err.Error())
		return
	}
	devices, err := s.st.Devices(ctx)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	online := 0
	for _, d := range devices {
		if d.Online {
			online++
		}
	}
	writeJSON(w, 200, map[string]any{
		"status": "ok", "version": Version, "db": "sqlite",
		"devices": len(devices), "online": online,
		"now": time.Now().Format("2006-01-02T15:04:05"),
	})
}

func (s *Server) handleDevices(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := ctxOf(r)
	defer cancel()
	devices, err := s.st.Devices(ctx)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"devices": devices})
}

func (s *Server) handleDeleteDevice(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if !deviceRe.MatchString(id) {
		writeErr(w, 422, "device_id 非法")
		return
	}
	ctx, cancel := ctxOf(r)
	defer cancel()
	if err := s.st.DeleteDevice(ctx, id); err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"deleted": id})
}

func (s *Server) handleToday(w http.ResponseWriter, r *http.Request) {
	f, err := s.filter(r)
	if err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	now := time.Now()
	f.Start = time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.Local)
	f.End = now.Add(time.Second)
	ctx, cancel := ctxOf(r)
	defer cancel()
	t, err := s.st.Totals(ctx, f)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, t)
}

func autoBucket(start, end time.Time) string {
	span := end.Sub(start)
	if span <= 48*time.Hour {
		return "hour"
	}
	if span > 31*24*time.Hour {
		return "month"
	}
	return "day"
}

func (s *Server) handleRange(w http.ResponseWriter, r *http.Request) {
	f, err := s.filter(r)
	if err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	bucket := r.URL.Query().Get("bucket")
	switch bucket {
	case "hour", "day", "month":
	case "", "auto":
		bucket = autoBucket(f.Start, f.End)
	default:
		writeErr(w, 400, "bucket must be auto|hour|day|month")
		return
	}
	ctx, cancel := ctxOf(r)
	defer cancel()
	totals, err := s.st.Totals(ctx, f)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	days, err := s.st.Daily(ctx, f, bucket)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{
		"start": f.Start.Format("2006-01-02T15:04:05"), "end": f.End.Format("2006-01-02T15:04:05"),
		"bucket": bucket, "totals": totals, "days": days,
	})
}

func (s *Server) breakdown(w http.ResponseWriter, r *http.Request, group, key string) {
	f, err := s.filter(r)
	if err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	ctx, cancel := ctxOf(r)
	defer cancel()
	rows, err := s.st.Breakdown(ctx, f, group)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{key: rows})
}

func (s *Server) handleAgents(w http.ResponseWriter, r *http.Request) { s.breakdown(w, r, "agent", "agents") }
func (s *Server) handleModels(w http.ResponseWriter, r *http.Request) { s.breakdown(w, r, "model", "models") }
func (s *Server) handleStatsDevices(w http.ResponseWriter, r *http.Request) {
	s.breakdown(w, r, "device_id", "devices")
}

func (s *Server) handleSessions(w http.ResponseWriter, r *http.Request) {
	f, err := s.filter(r)
	if err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	ctx, cancel := ctxOf(r)
	defer cancel()
	rows, err := s.st.Sessions(ctx, f, intParam(r, "limit", 50, 1, 500), intParam(r, "offset", 0, 0, 1<<30))
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"sessions": rows})
}

func (s *Server) handleUsage(w http.ResponseWriter, r *http.Request) {
	f, err := s.filter(r)
	if err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	ctx, cancel := ctxOf(r)
	defer cancel()
	rows, err := s.st.Usage(ctx, f, r.URL.Query().Get("session_id"), intParam(r, "limit", 100, 1, 1000), intParam(r, "offset", 0, 0, 1<<30))
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"usage": rows})
}

func (s *Server) handleSessionDetail(w http.ResponseWriter, r *http.Request) {
	f := store.Filter{
		Start:    time.Date(1970, 1, 1, 0, 0, 0, 0, time.Local),
		End:      time.Now().AddDate(0, 0, 1),
		Agent:    r.PathValue("agent"),
		DeviceID: r.URL.Query().Get("device_id"),
	}
	ctx, cancel := ctxOf(r)
	defer cancel()
	rows, err := s.st.Usage(ctx, f, r.PathValue("sid"), intParam(r, "limit", 200, 1, 1000), 0)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	if len(rows) == 0 {
		writeErr(w, 404, "session not found")
		return
	}
	writeJSON(w, 200, map[string]any{"agent": f.Agent, "session_id": r.PathValue("sid"), "usage": rows})
}

var _ = errors.New
