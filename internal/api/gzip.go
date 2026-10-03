package api

import (
	"compress/gzip"
	"net/http"
	"strings"
	"sync"
)

// gzipWriter 包装 ResponseWriter，写出时以 gzip 压缩。
type gzipWriter struct {
	http.ResponseWriter
	gz *gzip.Writer
}

// WriteHeader 压缩后长度改变，递交前隐去原始 Content-Length（静态文件服务会预设它）
func (w *gzipWriter) WriteHeader(code int) {
	w.Header().Del("Content-Length")
	w.ResponseWriter.WriteHeader(code)
}

func (w *gzipWriter) Write(b []byte) (int, error) {
	if w.Header().Get("Content-Type") == "" {
		w.Header().Set("Content-Type", http.DetectContentType(b))
	}
	w.Header().Del("Content-Length")
	return w.gz.Write(b)
}

// gzipHandler 对可压缩响应做 gzip：面板静态资源与 JSON API 的体积在家宽上行上
// 直接决定首屏速度（如 usage 45KB JSON 压缩后约 5KB）。nginx 侧未开 gzip，
// 会透传后端的 Content-Encoding。
func gzipHandler(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodHead || !acceptsGzip(r) || isImage(r.URL.Path) {
			next.ServeHTTP(w, r)
			return
		}
		w.Header().Set("Content-Encoding", "gzip")
		w.Header().Add("Vary", "Accept-Encoding")
		// 压缩后长度不同，隐去 Content-Length 交给 http 层分块
		w.Header().Del("Content-Length")
		gz := gzipPool.Get().(*gzip.Writer)
		defer gzipPool.Put(gz)
		gz.Reset(w)
		defer gz.Close()
		next.ServeHTTP(&gzipWriter{ResponseWriter: w, gz: gz}, r)
	})
}

// acceptsGzip 解析 Accept-Encoding，处理 "gzip;q=0" 的显式拒绝。
func acceptsGzip(r *http.Request) bool {
	enc := strings.TrimSpace(r.Header.Get("Accept-Encoding"))
	if enc == "" {
		return false
	}
	for _, part := range strings.Split(enc, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		name, params := part, ""
		if j := strings.Index(part, ";"); j >= 0 {
			name, params = strings.TrimSpace(part[:j]), part[j+1:]
		}
		if name == "gzip" && !strings.Contains(params, "q=0") {
			return true
		}
	}
	return false
}

// isImage 已压缩的图片/归档不再 gzip，白费 CPU
func isImage(path string) bool {
	for _, ext := range []string{".png", ".jpg", ".jpeg", ".gif", ".webp", ".ico", ".gz", ".zip", ".tar"} {
		if strings.HasSuffix(strings.ToLower(path), ext) {
			return true
		}
	}
	return false
}

var gzipPool = sync.Pool{New: func() any { return gzip.NewWriter(nil) }}
