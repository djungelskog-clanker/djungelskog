package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"strconv"
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

func TestMessageLink(t *testing.T) {
	for _, s := range []string{
		"<https://hackclub.slack.com/archives/C0BEPN3JH2R/p1787032943387839?thread_ts=1787024149.893349&cid=C0BEPN3JH2R>",
		"look https://hackclub.enterprise.slack.com/archives/C06RQ9TTEG3/p1719612237677359",
	} {
		if !messageLinkRe.MatchString(s) {
			t.Errorf("missed %q", s)
		}
	}
	if messageLinkRe.MatchString("https://hackclub.slack.com/files/U05MKEZUY67/F0BQ53HJ70R/image.png") {
		t.Error("matched a file link")
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

func TestReapForgetsIdleThreads(t *testing.T) {
	m := newTestManager(t)
	old := m.get("C1", "1.0", false)
	old.lastActive = time.Now().Add(-time.Hour)
	m.noteMessage("1.1", old.key)
	fresh := m.get("C1", "2.0", false)
	m.noteMessage("2.1", fresh.key)

	m.reap()

	if m.threads[old.key] != nil || m.byToken[old.token] != nil || m.byMsg["1.1"] != "" {
		t.Error("idle thread not forgotten")
	}
	if m.threads[fresh.key] == nil || m.byToken[fresh.token] == nil || m.byMsg["2.1"] == "" {
		t.Error("active thread forgotten")
	}
}

type nopCloser struct{}

func (nopCloser) Write(b []byte) (int, error) { return len(b), nil }
func (nopCloser) Close() error                { return nil }

// A reaped process ends its pump without touching the thread, so the thread
// is forgotten on the next pass instead of a full idle timeout later.
func TestReapProcThenForget(t *testing.T) {
	m := newTestManager(t)
	th := m.get("C1", "1.0", false)
	p := &claudeProc{cmd: &exec.Cmd{}, stdin: nopCloser{}, Events: make(chan event)}
	th.proc = p
	stale := time.Now().Add(-time.Hour)
	th.lastActive = stale
	done := make(chan struct{})
	go func() { m.pump(th, p); close(done) }()

	m.reap()
	if th.proc != nil || m.threads[th.key] == nil {
		t.Fatal("first pass should close the process and keep the thread")
	}
	close(p.Events) // the process exits
	<-done
	if !th.lastActive.Equal(stale) || th.dirty {
		t.Fatal("old pump touched the thread after being reaped")
	}

	m.reap()
	if m.threads[th.key] != nil {
		t.Fatal("second pass should forget the thread")
	}
}

func newTestManager(t *testing.T) *manager {
	t.Helper()
	m, err := newManager(&bot{cfg: config{IdleTimeout: time.Minute, DataDir: t.TempDir()}})
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func TestPruneSessions(t *testing.T) {
	m := newTestManager(t)
	now := time.Now()
	ts := func(d time.Duration) string { return strconv.FormatInt(now.Add(-d).Unix(), 10) + ".000100" }
	day := 24 * time.Hour
	keys := map[string]bool{ // key -> should survive
		"C1:" + ts(40*day):           false, // old and quiet
		"C1:" + ts(40*day+time.Hour): true,  // old but active this week
		"C1:" + ts(10*day):           true,  // quiet but young
		"D1:":                        true,  // top level dm, no age
		"C1:" + ts(50*day):           true,  // old, quiet, but no seen record yet
	}
	sess, seen := map[string]string{}, map[string]time.Time{}
	for k := range keys {
		sess[k] = "id-" + k
		seen[k] = now.Add(-10 * day)
	}
	seen["C1:"+ts(40*day+time.Hour)] = now.Add(-day)
	delete(seen, "C1:"+ts(50*day))
	_ = m.sessions.Update(func(map[string]string) map[string]string { return sess })
	_ = m.seen.Update(func(map[string]time.Time) map[string]time.Time { return seen })

	m.mu.Lock()
	m.pruneSessions(now)
	m.mu.Unlock()

	got := m.sessions.Get()
	for k, keep := range keys {
		if (got[k] != "") != keep {
			t.Errorf("%s: kept=%v, want %v", k, got[k] != "", keep)
		}
	}
	if _, ok := m.seen.Get()["C1:"+ts(50*day)]; !ok {
		t.Error("missing seen record not backfilled")
	}
}
