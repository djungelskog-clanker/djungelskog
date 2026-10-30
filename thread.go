package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"sync"
	"time"
)

// incoming is one message that should reach the agent.
type incoming struct {
	Channel     string
	ThreadTS    string // where replies go; "" = top level (plain dms)
	TS          string // the message itself
	User        string
	Text        string // already tagged with sender info
	ActionToken string
	Assistant   bool // came from the assistant side panel
}

// thread is one slack conversation backed by one claude session.
type thread struct {
	key       string
	channel   string
	threadTS  string
	assistant bool
	token     string // identifies this thread to the mcp tool server

	mu          sync.Mutex
	proc        *claudeProc
	queue       []incoming
	busy        bool
	trigger     incoming // message that started the current turn
	lastActive  time.Time
	dirty       bool // had turns since the last memory review
	interrupted bool
}

func (t *thread) current() incoming {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.trigger
}

type manager struct {
	bot      *bot
	mu       sync.Mutex
	threads  map[string]*thread
	byToken  map[string]*thread
	byMsg    map[string]string // message ts -> thread key, for reaction stops
	sessions *jsonStore[map[string]string]
	seen     *jsonStore[map[string]time.Time] // thread key -> last finished turn
}

// Sessions of threads older than sessionMaxAge that have been quiet for
// sessionIdle are dropped, so sessions.json doesn't grow forever.
const (
	sessionMaxAge = 30 * 24 * time.Hour
	sessionIdle   = 7 * 24 * time.Hour
)

func newManager(b *bot) (*manager, error) {
	s, err := openStore[map[string]string](b.cfg.DataDir + "/sessions.json")
	if err != nil {
		return nil, err
	}
	seen, err := openStore[map[string]time.Time](b.cfg.DataDir + "/seen.json")
	if err != nil {
		return nil, err
	}
	m := &manager{bot: b, threads: map[string]*thread{}, byToken: map[string]*thread{}, byMsg: map[string]string{}, sessions: s, seen: seen}
	go m.reaper()
	return m, nil
}

func threadKey(channel, threadTS string) string { return channel + ":" + threadTS }

func (m *manager) get(channel, threadTS string, assistant bool) *thread {
	key := threadKey(channel, threadTS)
	m.mu.Lock()
	defer m.mu.Unlock()
	if t, ok := m.threads[key]; ok {
		// touch under m.mu so the reaper can't forget it between lookup and use
		t.mu.Lock()
		t.lastActive = time.Now()
		t.mu.Unlock()
		return t
	}
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	t := &thread{key: key, channel: channel, threadTS: threadTS, assistant: assistant, token: hex.EncodeToString(b), lastActive: time.Now()}
	m.threads[key] = t
	m.byToken[t.token] = t
	return t
}

func (m *manager) threadByToken(tok string) *thread {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.byToken[tok]
}

func (m *manager) noteMessage(ts, key string) {
	m.mu.Lock()
	m.byMsg[ts] = key
	m.mu.Unlock()
}

// Submit queues a message and sends it right away if claude is idle.
func (m *manager) Submit(in incoming) {
	t := m.get(in.Channel, in.ThreadTS, in.Assistant)
	if in.TS != "" {
		m.noteMessage(in.TS, t.key)
	}
	t.mu.Lock()
	t.queue = append(t.queue, in)
	t.lastActive = time.Now()
	t.mu.Unlock()
	m.kick(t)
}

// Stop interrupts the running turn in the thread containing message ts.
// It reports whether something was actually running.
func (m *manager) Stop(channel, ts string) bool {
	m.mu.Lock()
	key, ok := m.byMsg[ts]
	if !ok {
		key = threadKey(channel, ts)
	}
	t := m.threads[key]
	m.mu.Unlock()
	return t != nil && t.interrupt()
}

func (m *manager) StopThread(channel, threadTS string) bool {
	m.mu.Lock()
	t := m.threads[threadKey(channel, threadTS)]
	m.mu.Unlock()
	return t != nil && t.interrupt()
}

func (t *thread) interrupt() bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	if !t.busy || t.proc == nil {
		return false
	}
	t.queue = nil
	t.interrupted = true
	if err := t.proc.Interrupt(); err != nil {
		slog.Warn("interrupt failed", "err", err)
		return false
	}
	return true
}

func (m *manager) ensureProc(t *thread) (*claudeProc, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.proc != nil {
		return t.proc, nil
	}
	resume := m.sessions.Get()[t.key]
	p, err := startClaude(context.Background(), procOptions{
		WorkDir:      m.bot.cfg.WorkDir,
		Model:        m.bot.cfg.Model,
		Resume:       resume,
		MCPURL:       m.bot.mcpURL(t.token),
		SystemAppend: m.bot.systemPrompt(),
	})
	if err != nil {
		return nil, err
	}
	t.proc = p
	go m.pump(t, p)
	return p, nil
}

// kick sends the queued messages to claude if no turn is running.
func (m *manager) kick(t *thread) {
	t.mu.Lock()
	if t.busy || len(t.queue) == 0 {
		t.mu.Unlock()
		return
	}
	batch := t.queue
	t.queue = nil
	t.trigger = batch[len(batch)-1]
	t.busy = true
	t.interrupted = false
	t.mu.Unlock()

	fail := func(msg string) {
		t.mu.Lock()
		t.busy = false
		t.mu.Unlock()
		m.bot.postPlain(t.channel, t.threadTS, msg)
		if t.assistant {
			m.bot.setStatus(t.channel, t.threadTS, "active")
		}
	}
	p, err := m.ensureProc(t)
	if err != nil {
		slog.Error("start claude", "err", err)
		fail("couldn't start claude :pf: `" + err.Error() + "`")
		return
	}
	if t.assistant {
		m.bot.setStatus(t.channel, t.threadTS, "processing")
	}
	var parts []string
	for _, in := range batch {
		parts = append(parts, in.Text)
	}
	if err := p.Send(strings.Join(parts, "\n\n")); err != nil {
		slog.Error("send to claude", "err", err)
		m.dropProc(t, p)
		fail("lost my brain mid-sentence :pf: try again?")
	}
}

// pump renders everything a claude process says, for as long as it lives.
// Turns we started and turns claude starts on its own (say, when a
// background task finishes) are handled the same way.
func (m *manager) pump(t *thread, p *claudeProc) {
	ctx := context.Background()
	var rs *replyStream
	titles := map[string]string{}
	textBlock := false
	tick := time.NewTicker(700 * time.Millisecond)
	defer tick.Stop()

	open := func() *replyStream {
		if rs == nil {
			t.mu.Lock()
			t.busy = true // also true for turns claude started itself
			user := t.trigger.User
			t.mu.Unlock()
			rs = newReplyStream(m.bot.api, t.channel, t.threadTS, user, m.bot.teamID)
			titles = map[string]string{}
			textBlock = false
		}
		return rs
	}
	endTurn := func(note, plan string) {
		if rs != nil || note != "" {
			open()
			if plan != "" {
				rs.Plan(plan)
			}
			rs.Finish(ctx, note)
			m.track(t, rs)
		}
		if rs != nil || t.assistant {
			// stopStream should do this, but not if the stream already broke
			m.bot.setStatus(t.channel, t.threadTS, "active")
		}
		rs = nil
		t.mu.Lock()
		t.busy = false
		t.dirty = true
		t.lastActive = time.Now()
		t.mu.Unlock()
		m.kick(t)
	}

	for {
		select {
		case <-tick.C:
			if rs != nil {
				rs.Flush(ctx)
			}
		case ev, ok := <-p.Events:
			if !ok {
				t.mu.Lock()
				current := t.proc == p
				wasBusy := t.busy
				t.mu.Unlock()
				if !current {
					// reaped or replaced on purpose; the thread state now belongs
					// to whoever took over, so don't end their turn or touch lastActive
					return
				}
				m.dropProc(t, p)
				note := ""
				if wasBusy {
					note = "_(my claude process died mid-turn. send that again?)_"
				}
				endTurn(note, "")
				return
			}
			if ev.ParentID != nil && *ev.ParentID != "" {
				continue // subagent internals
			}
			switch ev.Type {
			case "stream_event":
				if ev.Event == nil {
					continue
				}
				switch ev.Event.Type {
				case "content_block_start":
					if ev.Event.ContentBlock.Type == "text" {
						if textBlock {
							open().Paragraph()
						}
						textBlock = true
					}
				case "content_block_delta":
					if ev.Event.Delta.Type == "text_delta" {
						open().Text(ev.Event.Delta.Text)
					}
				}
			case "assistant":
				var msg apiMessage
				_ = json.Unmarshal(ev.Message, &msg)
				for _, b := range msg.Content {
					if b.Type == "tool_use" {
						title, details := toolTitle(b.Name, b.Input)
						open().ToolStart(b.ID, title, details) // open first: it resets titles
						titles[b.ID] = title
					}
				}
			case "user":
				if rs == nil {
					continue
				}
				var msg apiMessage
				_ = json.Unmarshal(ev.Message, &msg)
				for _, b := range msg.Content {
					if b.Type == "tool_result" {
						rs.ToolDone(b.ToolUseID, titles[b.ToolUseID], b.IsError, "")
					}
				}
			case "result":
				if ev.Subtype == "compact" || ev.Subtype == "compaction" {
					continue
				}
				if id := p.SessionID(); id != "" {
					_ = m.sessions.Update(func(s map[string]string) map[string]string {
						if s == nil {
							s = map[string]string{}
						}
						s[t.key] = id
						return s
					})
					_ = m.seen.Update(func(s map[string]time.Time) map[string]time.Time {
						if s == nil {
							s = map[string]time.Time{}
						}
						s[t.key] = time.Now()
						return s
					})
				}
				t.mu.Lock()
				stopped := t.interrupted
				t.interrupted = false
				t.mu.Unlock()
				note, plan := "", ""
				switch {
				case stopped:
					note, plan = "_(stopped)_", "stopped"
				case ev.IsError:
					note = fmt.Sprintf("_(that turn ended with an error: %s)_", firstNonEmpty(ev.Result, ev.Subtype))
				case (rs == nil || !rs.anyOutput) && strings.TrimSpace(ev.Result) != "":
					open().Text(ev.Result)
				}
				endTurn(note, plan)
				slog.Info("turn done", "thread", t.key, "session_cost_usd", ev.CostUSD, "subtype", ev.Subtype)
			}
		}
	}
}
func (m *manager) track(t *thread, rs *replyStream) {
	for _, ts := range rs.posted {
		m.noteMessage(ts, t.key)
	}
}

func (m *manager) dropProc(t *thread, p *claudeProc) {
	p.Close()
	t.mu.Lock()
	if t.proc == p {
		t.proc = nil
	}
	t.mu.Unlock()
}

// reaper shuts down claude processes for quiet threads and, when enabled,
// runs a cheap memory review over what was said. Threads that were already
// shut down get forgotten; the session id in sessions.json is enough to
// resume them later.
func (m *manager) reaper() {
	for range time.Tick(time.Minute) {
		m.reap()
	}
}

func (m *manager) reap() {
	m.mu.Lock()
	var idle []*thread
	forgot := false
	for key, t := range m.threads {
		t.mu.Lock()
		quiet := !t.busy && len(t.queue) == 0 && time.Since(t.lastActive) > m.bot.cfg.IdleTimeout
		switch {
		case quiet && t.proc != nil:
			idle = append(idle, t)
		case quiet:
			delete(m.threads, key)
			delete(m.byToken, t.token)
			forgot = true
		}
		t.mu.Unlock()
	}
	if forgot {
		for ts, key := range m.byMsg {
			if m.threads[key] == nil {
				delete(m.byMsg, ts)
			}
		}
	}
	m.pruneSessions(time.Now())
	m.mu.Unlock()

	for _, t := range idle {
		t.mu.Lock()
		if t.busy || t.proc == nil || len(t.queue) > 0 { // woke up meanwhile
			t.mu.Unlock()
			continue
		}
		p := t.proc
		t.proc = nil
		review := t.dirty
		t.dirty = false
		t.mu.Unlock()
		p.Close()
		slog.Info("reaped idle thread", "thread", t.key)
		if review && m.bot.cfg.MemoryModel != "" {
			if id := m.sessions.Get()[t.key]; id != "" {
				go m.bot.reviewMemory(id)
			}
		}
	}
}

// pruneSessions forgets the session ids of old, quiet threads. Callers hold
// m.mu, so a thread can't come back to life halfway through.
func (m *manager) pruneSessions(now time.Time) {
	seen := m.seen.Get()
	var drop []string
	for key := range m.sessions.Get() {
		last, ok := seen[key]
		if !ok {
			// from before seen.json existed: start counting from now
			_ = m.seen.Update(func(s map[string]time.Time) map[string]time.Time {
				if s == nil {
					s = map[string]time.Time{}
				}
				s[key] = now
				return s
			})
			continue
		}
		start, ok := threadStart(key)
		if ok && m.threads[key] == nil && now.Sub(start) > sessionMaxAge && now.Sub(last) > sessionIdle {
			drop = append(drop, key)
		}
	}
	if len(drop) == 0 {
		return
	}
	_ = m.sessions.Update(func(s map[string]string) map[string]string {
		for _, k := range drop {
			delete(s, k)
		}
		return s
	})
	_ = m.seen.Update(func(s map[string]time.Time) map[string]time.Time {
		for _, k := range drop {
			delete(s, k)
		}
		return s
	})
	slog.Info("pruned old sessions", "count", len(drop))
}

// threadStart reads the thread's age from its root ts. Top level dms have
// no thread ts; they're one long conversation, so they never count as old.
func threadStart(key string) (time.Time, bool) {
	_, ts, _ := strings.Cut(key, ":")
	sec, err := strconv.ParseFloat(ts, 64)
	if err != nil {
		return time.Time{}, false
	}
	return time.Unix(int64(sec), 0), true
}

func firstNonEmpty(s ...string) string {
	for _, v := range s {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}
