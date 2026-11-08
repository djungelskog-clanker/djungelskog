package main

import (
	"context"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// voice messages and audio clips get transcribed before they reach me, so a
// voice note works like a typed message. uses the faster-whisper setup in
// ~/workspace/transcribe; set TRANSCRIBE=off to disable.
func isAudio(mime, name string) bool {
	if strings.HasPrefix(mime, "audio/") {
		return true
	}
	switch strings.ToLower(filepath.Ext(name)) {
	case ".m4a", ".mp3", ".wav", ".ogg", ".opus", ".flac", ".webm":
		return true
	}
	return strings.HasPrefix(name, "audio_message")
}

func transcribe(path string) (string, error) {
	if os.Getenv("TRANSCRIBE") == "off" {
		return "", nil
	}
	home := os.Getenv("HOME")
	py := filepath.Join(home, "workspace/transcribe/.venv/bin/python")
	script := filepath.Join(home, "workspace/transcribe/transcribe.py")
	if _, err := os.Stat(script); err != nil {
		return "", err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()
	start := time.Now()
	out, err := exec.CommandContext(ctx, py, "-I", script, path).Output()
	slog.Info("transcribed", "file", filepath.Base(path), "took", time.Since(start).Round(time.Second), "err", err)
	return strings.TrimSpace(string(out)), err
}
