package main

import (
	"log/slog"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/joho/godotenv"
)

// allowlist holds the ALLOW_FROM user ids and re-reads them from the env file
// whenever its mtime changes, so edits apply without a restart.
type allowlist struct {
	path string

	mu    sync.Mutex
	ids   map[string]bool
	mtime time.Time
}

func newAllowlist(path, initial string) *allowlist {
	a := &allowlist{path: path, ids: parseAllowFrom(initial)}
	if fi, err := os.Stat(path); err == nil {
		a.mtime = fi.ModTime()
	}
	return a
}

func parseAllowFrom(s string) map[string]bool {
	ids := map[string]bool{}
	for _, id := range strings.Split(s, ",") {
		if id = strings.TrimSpace(id); id != "" {
			ids[id] = true
		}
	}
	return ids
}

// Has reports whether user is allowed, reloading the file first if it changed.
// On a read error the last known list stays in effect.
func (a *allowlist) Has(user string) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	if fi, err := os.Stat(a.path); err == nil && !fi.ModTime().Equal(a.mtime) {
		if env, err := godotenv.Read(a.path); err != nil {
			slog.Warn("reload allowlist", "err", err)
		} else {
			a.ids = parseAllowFrom(env["ALLOW_FROM"])
			a.mtime = fi.ModTime()
			slog.Info("reloaded allowlist", "count", len(a.ids))
		}
	}
	return a.ids[user]
}
