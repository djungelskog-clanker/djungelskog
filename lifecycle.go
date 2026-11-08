package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// restartNote is left behind by restart_self so the next boot can say it's
// back in the thread that asked for the restart.
type restartNote struct {
	Channel  string    `json:"channel"`
	ThreadTS string    `json:"thread_ts"`
	User     string    `json:"user"`
	From     string    `json:"from_version"`
	At       time.Time `json:"at"`
}

// selfRestart tests and builds the bot from source, swaps the binary in
// (keeping the old one as djungelskog.prev) and restarts the service in 20
// seconds. A watchdog 90 seconds later rolls back to the old binary if the
// new one never connected to slack.
func (b *bot) selfRestart(ctx context.Context, t *thread, user string) (string, error) {
	dir := botSourceDir()
	run := func(timeout time.Duration, name string, args ...string) (string, error) {
		cctx, cancel := context.WithTimeout(ctx, timeout)
		defer cancel()
		cmd := exec.CommandContext(cctx, name, args...)
		cmd.Dir = dir
		out, err := cmd.CombinedOutput()
		return strings.TrimSpace(string(out)), err
	}
	if out, err := run(5*time.Minute, "go", "test", "./..."); err != nil {
		return "", fmt.Errorf("tests failed, not restarting:\n%s", truncRunes(out, 2000))
	}
	if out, err := run(5*time.Minute, "go", "build", "-o", "djungelskog.new", "."); err != nil {
		return "", fmt.Errorf("build failed, not restarting:\n%s", truncRunes(out, 2000))
	}
	bin := filepath.Join(dir, "djungelskog")
	if err := copyFile(bin, bin+".prev"); err != nil {
		return "", fmt.Errorf("backing up the old binary: %w", err)
	}
	if err := os.Rename(filepath.Join(dir, "djungelskog.new"), bin); err != nil {
		return "", err
	}
	note := restartNote{Channel: t.channel, ThreadTS: t.threadTS, User: user, From: version(), At: time.Now()}
	if err := writeJSON(filepath.Join(b.cfg.DataDir, "restart.json"), note); err != nil {
		return "", err
	}
	marker := filepath.Join(b.cfg.DataDir, "restart.marker")
	if err := os.WriteFile(marker, []byte(time.Now().Format(time.RFC3339)), 0o600); err != nil {
		return "", err
	}
	// date the marker past the restart, so a socket reconnect of this old
	// process in the next 20s doesn't look like the new one coming up
	future := time.Now().Add(25 * time.Second)
	if err := os.Chtimes(marker, future, future); err != nil {
		return "", err
	}
	stamp := fmt.Sprint(time.Now().Unix())
	if out, err := run(time.Minute, "systemd-run", "--user", "--on-active=20", "--unit=djungelskog-restart-"+stamp,
		"systemctl", "--user", "restart", "djungelskog.service"); err != nil {
		return "", fmt.Errorf("scheduling the restart: %v %s", err, out)
	}
	connected := filepath.Join(b.cfg.DataDir, "last_connected")
	watch := fmt.Sprintf(`if [ ! %q -nt %q ]; then cp %q %q && systemctl --user restart djungelskog.service; fi`,
		connected, marker, bin+".prev", bin)
	if out, err := run(time.Minute, "systemd-run", "--user", "--on-active=110", "--unit=djungelskog-watchdog-"+stamp,
		"sh", "-c", watch); err != nil {
		slog.Warn("scheduling the rollback watchdog failed", "err", err, "out", out)
	}
	return "tests pass, new binary is in place. restarting in 20s; if it hasn't reconnected to slack ~90s after that, a watchdog puts the old binary back.", nil
}

func copyFile(src, dst string) error {
	data, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	return os.WriteFile(dst, data, 0o755)
}

// announceBack posts in the thread that asked for a restart, once the new
// process is connected.
func (b *bot) announceBack() {
	path := filepath.Join(b.cfg.DataDir, "restart.json")
	var note restartNote
	if err := readJSON(path, &note); err != nil {
		return
	}
	_ = os.Remove(path)
	if time.Since(note.At) > 30*time.Minute {
		return // stale
	}
	text := fmt.Sprintf("_back online, now running `%s` (was `%s`)_", version(), note.From)
	if note.From == version() {
		text = fmt.Sprintf("_back online, still on `%s`_", version())
	}
	b.postPlain(note.Channel, note.ThreadTS, text)
}

// onBoot runs once, after the first successful connection.
func (b *bot) onBoot() {
	b.announceBack()
	b.resumeCutOff()
}

// cutOff is a turn that was still running when the bot shut down.
type cutOff struct {
	Channel   string `json:"channel"`
	ThreadTS  string `json:"thread_ts"`
	User      string `json:"user"`
	Assistant bool   `json:"assistant"`
}

// shutdown runs on SIGTERM: threads that are mid-turn get a short note and
// are written down so the next boot can pick them back up.
func (b *bot) shutdown() {
	var cut []cutOff
	b.mgr.mu.Lock()
	for _, t := range b.mgr.threads {
		t.mu.Lock()
		if t.busy || len(t.queue) > 0 {
			cut = append(cut, cutOff{Channel: t.channel, ThreadTS: t.threadTS, User: t.trigger.User, Assistant: t.assistant})
		}
		t.mu.Unlock()
	}
	b.mgr.mu.Unlock()
	if len(cut) == 0 {
		return
	}
	if err := writeJSON(filepath.Join(b.cfg.DataDir, "cutoff.json"), cut); err != nil {
		slog.Error("saving cut-off turns", "err", err)
	}
	for _, c := range cut {
		b.postPlain(c.Channel, c.ThreadTS, "_(restarting mid-turn, i'll pick this back up in a sec)_")
	}
	slog.Info("shutdown: saved cut-off turns", "count", len(cut))
}

// resumeCutOff nudges the threads that shutdown() wrote down.
func (b *bot) resumeCutOff() {
	path := filepath.Join(b.cfg.DataDir, "cutoff.json")
	var cut []cutOff
	if err := readJSON(path, &cut); err != nil {
		return
	}
	_ = os.Remove(path)
	for _, c := range cut {
		b.mgr.Submit(incoming{Channel: c.Channel, ThreadTS: c.ThreadTS, User: c.User, Assistant: c.Assistant,
			Text: "[system, not a slack message: the bot restarted while you were in the middle of a turn in this thread. " +
				"look at where you were and finish what you were doing (re-check anything half done). don't call restart_self again because of this note.]"})
	}
}
