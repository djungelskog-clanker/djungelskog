package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
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
}

func newManager(b *bot) (*manager, error) {
	s, err := openStore[map[string]string](b.cfg.DataDir + "/sessions.json")
	if err != nil {
		return nil, err
	}
	m := &manager{bot: b, threads: map[string]*thread{}, byToken: map[string]*thread{}, byMsg: map[string]string{}, sessions: s}
	go m.reaper()
	return m, nil
}

func threadKey(channel, threadTS string) string { return channel + ":" + threadTS }

func (m *manager) get(channel, threadTS string, assistant bool) *thread {
	key := threadKey(channel, threadTS)
	m.mu.Lock()
	defer m.mu.Unlock()
	if t, ok := m.threads[key]; ok {
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
	}
	p, err := m.ensureProc(t)
	if err != nil {
		slog.Error("start claude", "err", err)
		fail("couldn't start claude :pf: `" + err.Error() + "`")
		return
	}
	if t.assistant {
		m.bot.setStatus(t.channel, t.threadTS, "is thinking…")
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
				m.dropProc(t, p)
				t.mu.Lock()
				wasBusy := t.busy
				t.mu.Unlock()
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
						titles[b.ID] = title
						open().ToolStart(b.ID, title, details)
						if t.assistant {
							m.bot.setStatus(t.channel, t.threadTS, title+"…")
						}
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
// runs a cheap memory review over what was said.
func (m *manager) reaper() {
	for range time.Tick(time.Minute) {
		m.mu.Lock()
		var idle []*thread
		for _, t := range m.threads {
			t.mu.Lock()
			if !t.busy && t.proc != nil && time.Since(t.lastActive) > m.bot.cfg.IdleTimeout {
				idle = append(idle, t)
			}
			t.mu.Unlock()
		}
		m.mu.Unlock()

		for _, t := range idle {
			t.mu.Lock()
			if t.busy || t.proc == nil { // woke up meanwhile
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
}

func firstNonEmpty(s ...string) string {
	for _, v := range s {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}
