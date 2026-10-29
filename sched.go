package main

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"log/slog"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/robfig/cron/v3"
)

// job is a scheduled prompt that runs the agent again in the thread it came from.
type job struct {
	ID        string    `json:"id"`
	Desc      string    `json:"desc"`
	Prompt    string    `json:"prompt"`
	Channel   string    `json:"channel"`
	ThreadTS  string    `json:"thread_ts"`
	Assistant bool      `json:"assistant"`
	User      string    `json:"user"`
	At        time.Time `json:"at,omitzero"`    // one-shot
	Cron      string    `json:"cron,omitempty"` // recurring
	Created   time.Time `json:"created"`
}

func (j job) describe() string {
	label := firstNonEmpty(j.Desc, j.Prompt)
	if len(label) > 60 {
		label = label[:57] + "..."
	}
	if j.Cron != "" {
		return fmt.Sprintf("%q recurring (%s)", label, j.Cron)
	}
	return fmt.Sprintf("%q at %s", label, j.At.Local().Format("Mon 2006-01-02 15:04 MST"))
}

type scheduler struct {
	b     *bot
	store *jsonStore[[]job]
	cron  *cron.Cron

	mu      sync.Mutex
	timers  map[string]*time.Timer
	entries map[string]cron.EntryID
}

func newScheduler(b *bot) (*scheduler, error) {
	st, err := openStore[[]job](b.cfg.DataDir + "/jobs.json")
	if err != nil {
		return nil, err
	}
	s := &scheduler{b: b, store: st, cron: cron.New(), timers: map[string]*time.Timer{}, entries: map[string]cron.EntryID{}}
	for _, j := range st.Get() {
		if err := s.arm(j); err != nil {
			slog.Warn("sched: dropping bad job", "id", j.ID, "err", err)
		}
	}
	s.cron.Start()
	return s, nil
}

func parseWhen(when string, now time.Time) (at time.Time, spec string, err error) {
	when = strings.TrimSpace(when)
	if strings.HasSuffix(when, "d") {
		if n, e := strconv.Atoi(strings.TrimSuffix(when, "d")); e == nil {
			return now.Add(time.Duration(n) * 24 * time.Hour), "", nil
		}
	}
	if d, e := time.ParseDuration(when); e == nil {
		if d <= 0 {
			return at, "", fmt.Errorf("delay must be positive")
		}
		return now.Add(d), "", nil
	}
	if t, e := time.Parse(time.RFC3339, when); e == nil {
		return t, "", nil
	}
	for _, layout := range []string{"2006-01-02T15:04", "2006-01-02 15:04", "2006-01-02T15:04:05"} {
		if t, e := time.ParseInLocation(layout, when, time.Local); e == nil {
			return t, "", nil
		}
	}
	if _, e := cron.ParseStandard(when); e == nil {
		return at, when, nil
	}
	return at, "", fmt.Errorf("can't parse %q: use a delay (30m, 2h, 1d), a local time (2026-11-02T09:00) or a 5-field cron expression", when)
}

func (s *scheduler) Add(when, prompt, desc string, t *thread, user string) (job, error) {
	at, spec, err := parseWhen(when, time.Now())
	if err != nil {
		return job{}, err
	}
	if spec == "" && at.Before(time.Now()) {
		return job{}, fmt.Errorf("%s is in the past", at.Format(time.RFC3339))
	}
	b := make([]byte, 3)
	_, _ = rand.Read(b)
	j := job{ID: hex.EncodeToString(b), Desc: desc, Prompt: prompt, Channel: t.channel, ThreadTS: t.threadTS,
		Assistant: t.assistant, User: user, At: at, Cron: spec, Created: time.Now()}
	if err := s.arm(j); err != nil {
		return job{}, err
	}
	return j, s.store.Update(func(js []job) []job { return append(js, j) })
}

func (s *scheduler) arm(j job) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if j.Cron != "" {
		id, err := s.cron.AddFunc(j.Cron, func() { s.fire(j.ID) })
		if err != nil {
			return err
		}
		s.entries[j.ID] = id
		return nil
	}
	// one-shots missed while we were down fire right away
	s.timers[j.ID] = time.AfterFunc(max(time.Until(j.At), time.Second), func() { s.fire(j.ID) })
	return nil
}

func (s *scheduler) fire(id string) {
	var j job
	found := false
	for _, x := range s.store.Get() {
		if x.ID == id {
			j, found = x, true
		}
	}
	if !found {
		return
	}
	if j.Cron == "" {
		_ = s.drop(id)
	}
	slog.Info("sched: firing", "id", id, "desc", j.Desc)
	text := fmt.Sprintf("[scheduled task %s (%s), set up by %s (id: %s). this is not a live message; nobody is waiting on the other end unless the task says so]\n%s",
		j.ID, firstNonEmpty(j.Desc, "no label"), s.b.userName(j.User), j.User, j.Prompt)
	s.b.mgr.Submit(incoming{Channel: j.Channel, ThreadTS: j.ThreadTS, User: j.User, Text: text, Assistant: j.Assistant})
}

func (s *scheduler) drop(id string) error {
	s.mu.Lock()
	if t, ok := s.timers[id]; ok {
		t.Stop()
		delete(s.timers, id)
	}
	if e, ok := s.entries[id]; ok {
		s.cron.Remove(e)
		delete(s.entries, id)
	}
	s.mu.Unlock()
	found := false
	err := s.store.Update(func(js []job) []job {
		out := js[:0]
		for _, j := range js {
			if j.ID == id {
				found = true
				continue
			}
			out = append(out, j)
		}
		return out
	})
	if err == nil && !found {
		return fmt.Errorf("no scheduled task %q", id)
	}
	return err
}

func (s *scheduler) Cancel(id string) error { return s.drop(id) }

func (s *scheduler) List() string {
	js := s.store.Get()
	if len(js) == 0 {
		return "nothing scheduled"
	}
	sort.Slice(js, func(a, b int) bool { return js[a].Created.Before(js[b].Created) })
	var sb strings.Builder
	for _, j := range js {
		fmt.Fprintf(&sb, "%s: %s in %s, set by %s\n", j.ID, j.describe(), s.b.channelName(j.Channel), s.b.userName(j.User))
	}
	return sb.String()
}
