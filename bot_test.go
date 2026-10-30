package main

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

func TestParseWhen(t *testing.T) {
	now := time.Date(2026, 10, 29, 12, 0, 0, 0, time.Local)
	cases := []struct {
		in       string
		wantAt   time.Time
		wantSpec string
		wantErr  bool
	}{
		{in: "30m", wantAt: now.Add(30 * time.Minute)},
		{in: "2d", wantAt: now.Add(48 * time.Hour)},
		{in: "2026-11-02T09:00", wantAt: time.Date(2026, 11, 2, 9, 0, 0, 0, time.Local)},
		{in: "0 9 * * 1", wantSpec: "0 9 * * 1"},
		{in: "-5m", wantErr: true},
		{in: "whenever", wantErr: true},
	}
	for _, c := range cases {
		at, spec, err := parseWhen(c.in, now)
		if (err != nil) != c.wantErr {
			t.Fatalf("%q: err = %v, wantErr %v", c.in, err, c.wantErr)
		}
		if !at.Equal(c.wantAt) || spec != c.wantSpec {
			t.Errorf("%q: got (%v, %q), want (%v, %q)", c.in, at, spec, c.wantAt, c.wantSpec)
		}
	}
}

func TestSplitRunes(t *testing.T) {
	s := strings.Repeat("héllo wörld\n", 3000)
	parts := splitRunes(s, streamRuneCap)
	if len(parts) < 3 {
		t.Fatalf("expected several parts, got %d", len(parts))
	}
	total := 0
	for _, p := range parts {
		if n := utf8.RuneCountInString(p); n > streamRuneCap {
			t.Errorf("part too long: %d", n)
		}
		total += strings.Count(p, "héllo")
	}
	if total != 3000 {
		t.Errorf("lost text: %d/3000 lines survived", total)
	}
}

func TestParseRef(t *testing.T) {
	ch, ts := parseRef("https://hackclub.slack.com/archives/C0A3XPGG2BG/p1761740000123456")
	if ch != "C0A3XPGG2BG" || ts != "1761740000.123456" {
		t.Errorf("got %s %s", ch, ts)
	}
	if ch, ts := parseRef("#general"); ch != "general" || ts != "" {
		t.Errorf("got %s %s", ch, ts)
	}
}

func TestToolTitle(t *testing.T) {
	in, _ := json.Marshal(map[string]any{"command": "ls -la", "description": "List Files"})
	if got, _ := toolTitle("Bash", in); got != "list files" {
		t.Errorf("bash: %q", got)
	}
	if got, _ := toolTitle("mcp__slack__read_channel", json.RawMessage(`{}`)); got != "slack: read channel" {
		t.Errorf("mcp: %q", got)
	}
}

func TestHTMLToText(t *testing.T) {
	got := htmlToText("<h1>title</h1><p>a &amp; b</p><ul><li>one</li><li>two</li></ul>")
	if !strings.Contains(got, "title") || !strings.Contains(got, "a & b") || strings.Contains(got, "<") {
		t.Errorf("got %q", got)
	}
}

func TestIsStop(t *testing.T) {
	for in, want := range map[string]bool{
		"stop": true, "STOP!": true, "stop\n*Sent using* <@U1|Claude>": true,
		"stop the music": false, "don't stop": false,
	} {
		if got := isStop(in); got != want {
			t.Errorf("isStop(%q) = %v", in, got)
		}
	}
}

func TestSentUsingFooter(t *testing.T) {
	got := sentUsingRe.ReplaceAllString("stop *Sent using* <@U09L2HK59AS>", "")
	if !isStop(got) {
		t.Errorf("footer not stripped: %q", got)
	}
}

func TestParseBody(t *testing.T) {
	raw := "From: a@b.c\r\nSubject: hi\r\nMIME-Version: 1.0\r\nContent-Type: multipart/mixed; boundary=XX\r\n\r\n" +
		"--XX\r\nContent-Type: text/html; charset=utf-8\r\n\r\n<p>hello <b>there</b></p>\r\n" +
		"--XX\r\nContent-Type: application/pdf\r\nContent-Disposition: attachment; filename=\"a.pdf\"\r\n\r\nPDF\r\n--XX--\r\n"
	text, atts := parseBody(strings.NewReader(raw))
	if !strings.Contains(text, "hello there") || len(atts) != 1 || atts[0] != "a.pdf" {
		t.Errorf("got %q %v", text, atts)
	}
}

func TestAllowlistReload(t *testing.T) {
	path := t.TempDir() + "/.env"
	if err := os.WriteFile(path, []byte("ALLOW_FROM=U1, U2\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	a := newAllowlist(path, "U1,U2")
	if !a.Has("U1") || !a.Has("U2") || a.Has("U3") {
		t.Fatalf("initial list wrong: %v", a.ids)
	}
	if err := os.WriteFile(path, []byte("ALLOW_FROM=U2,U3\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	later := time.Now().Add(time.Second)
	if err := os.Chtimes(path, later, later); err != nil {
		t.Fatal(err)
	}
	if a.Has("U1") || !a.Has("U2") || !a.Has("U3") {
		t.Fatalf("reloaded list wrong: %v", a.ids)
	}
	os.Remove(path)
	if !a.Has("U3") {
		t.Fatal("missing file should keep the last list")
	}
}
