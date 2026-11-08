package main

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"os"
	"time"
)

// serveHealth answers GET /health on localhost (HEALTH_ADDR, default
// 127.0.0.1:8788) with a small json status, for scripts and future-me.
func (b *bot) serveHealth() {
	addr := envOr("HEALTH_ADDR", "127.0.0.1:8788")
	if addr == "off" {
		return
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		live, busy, queued, _ := b.mgr.snapshot()
		usd, turns := spentSince(b.costs.Get(), time.Now())
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"ok": true, "version": version(), "pid": os.Getpid(),
			"started": b.started, "uptime_s": int(time.Since(b.started).Seconds()),
			"threads_live": live, "threads_busy": busy, "queued": queued,
			"jobs": len(b.sched.store.Get()), "today_usd": usd, "today_turns": turns,
		})
	})
	go func() {
		if err := http.ListenAndServe(addr, mux); err != nil {
			slog.Warn("health endpoint", "addr", addr, "err", err)
		}
	}()
}
