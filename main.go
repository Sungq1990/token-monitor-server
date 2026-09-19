package main

import (
	"context"
	"embed"
	"errors"
	"flag"
	"io/fs"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"token-monitor-server/internal/api"
	"token-monitor-server/internal/store"
)

//go:embed static
var staticFS embed.FS

func env(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func main() {
	addr := flag.String("addr", env("LISTEN_ADDR", ":8765"), "监听地址")
	dbPath := flag.String("db", env("DB_PATH", "data/token-monitor.db"), "SQLite 文件路径")
	healthcheck := flag.Bool("healthcheck", false, "仅探测本机服务是否健康后退出（docker healthcheck 用）")
	flag.Parse()

	if *healthcheck {
		port := *addr
		if i := strings.LastIndex(port, ":"); i >= 0 {
			port = port[i:]
		}
		client := &http.Client{Timeout: 4 * time.Second}
		resp, err := client.Get("http://127.0.0.1" + port + "/api/health")
		if err != nil || resp.StatusCode != 200 {
			os.Exit(1)
		}
		os.Exit(0)
	}

	log.SetFlags(log.LstdFlags | log.Lmsgprefix)
	log.SetPrefix("[token-monitor] ")

	st, err := store.Open(*dbPath)
	if err != nil {
		log.Fatalf("open sqlite %s: %v", *dbPath, err)
	}
	defer st.Close()
	log.Printf("sqlite ready: %s", *dbPath)

	static, err := fs.Sub(staticFS, "static")
	if err != nil {
		log.Fatal(err)
	}
	srv := &http.Server{
		Addr:              *addr,
		Handler:           api.New(st, static),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       2 * time.Minute,
		WriteTimeout:      2 * time.Minute,
		IdleTimeout:       90 * time.Second,
	}

	go func() {
		log.Printf("listening on %s (version %s)", *addr, api.Version)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Fatalf("listen: %v", err)
		}
	}()

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)
	<-stop
	log.Print("shutting down")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_ = srv.Shutdown(ctx)
}
