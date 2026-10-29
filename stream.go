package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/url"
	"path/filepath"
	"strings"
	"unicode/utf8"

	"github.com/slack-go/slack"
)

// slack caps a streamed message's markdown at 12k chars; roll over before that.
const streamRuneCap = 11000

// replyStream renders one agent turn into slack's native streaming message:
// tool calls become task rows, answer text streams below them. If streaming
// fails at any point the leftover text is posted as a plain message at the end,
// so a reply is never lost.
type replyStream struct {
	api           *slack.Client
	channel       string
	threadTS      string
	recipientUser string
	teamID        string

	ts        string // current stream message, "" if none open
	runes     int    // markdown runes sent in the current stream
	broken    bool   // streaming gave up; deliver the rest via postMessage
	text      strings.Builder
	tasks     []slack.TaskUpdateChunk // updates not yet sent
	plan      string                  // plan title not yet sent
	posted    []string                // ts of every message we produced
	anyOutput bool
}

func newReplyStream(api *slack.Client, channel, threadTS, user, teamID string) *replyStream {
	return &replyStream{api: api, channel: channel, threadTS: threadTS, recipientUser: user, teamID: teamID}
}

func (r *replyStream) Text(s string) {
	if s == "" {
		return
	}
	r.text.WriteString(s)
	r.anyOutput = true
}

// Plan sets the title of the task list (slack picks one if we don't).
func (r *replyStream) Plan(title string) { r.plan = title }

// Paragraph separates text blocks that were interrupted by tool calls.
func (r *replyStream) Paragraph() {
	if r.text.Len() > 0 && !strings.HasSuffix(r.text.String(), "\n\n") {
		r.text.WriteString("\n\n")
	}
}

func (r *replyStream) ToolStart(id, title, details string) {
	c := slack.NewTaskUpdateChunk(id, title)
	c.Status = slack.TaskCardStatus("in_progress")
	c.Details = details
	r.tasks = append(r.tasks, c)
	r.anyOutput = true
}

func (r *replyStream) ToolDone(id, title string, isErr bool, output string) {
	c := slack.NewTaskUpdateChunk(id, title)
	c.Status = slack.TaskCardStatus("complete")
	if isErr {
		c.Status = slack.TaskCardStatus("error")
	}
	c.Output = output
	for i := range r.tasks {
		if r.tasks[i].ID == id { // never sent yet, just replace it
			c.Details = r.tasks[i].Details
			r.tasks[i] = c
			return
		}
	}
	r.tasks = append(r.tasks, c)
}

// Flush pushes pending tasks and text to slack.
func (r *replyStream) Flush(ctx context.Context) {
	if r.broken || (len(r.tasks) == 0 && r.text.Len() == 0 && r.plan == "") {
		return
	}
	text := r.text.String()
	if r.ts != "" && r.runes+utf8.RuneCountInString(text) > streamRuneCap {
		r.stop(ctx) // roll over into a fresh message
	}
	chunks := make([]slack.StreamChunk, 0, len(r.tasks)+2)
	if r.plan != "" {
		chunks = append(chunks, slack.NewPlanUpdateChunk(r.plan))
	}
	for _, t := range r.tasks {
		chunks = append(chunks, t)
	}
	if text != "" {
		chunks = append(chunks, slack.NewMarkdownTextChunk(text))
	}

	var err error
	if r.ts == "" {
		opts := []slack.MsgOption{
			slack.MsgOptionChunks(chunks...),
			slack.MsgOptionTaskDisplayMode(slack.TaskDisplayMode("plan")),
		}
		if r.threadTS != "" {
			opts = append(opts, slack.MsgOptionTS(r.threadTS))
		}
		if r.recipientUser != "" {
			opts = append(opts, slack.MsgOptionRecipientUserID(r.recipientUser), slack.MsgOptionRecipientTeamID(r.teamID))
		}
		var ts string
		_, ts, err = r.api.StartStreamContext(ctx, r.channel, opts...)
		if err == nil {
			r.ts, r.runes = ts, 0
			r.posted = append(r.posted, ts)
		}
	} else {
		_, _, err = r.api.AppendStreamContext(ctx, r.channel, r.ts, slack.MsgOptionChunks(chunks...))
	}
	if err != nil {
		slog.Warn("stream: send failed, falling back", "err", err, "open", r.ts != "")
		if r.ts != "" {
			// probably expired; try a fresh stream next flush
			r.ts = ""
			return
		}
		r.broken = true
		return
	}
	r.runes += utf8.RuneCountInString(text)
	r.plan = ""
	r.text.Reset()
	r.tasks = r.tasks[:0]
}

func (r *replyStream) stop(ctx context.Context) {
	if r.ts == "" {
		return
	}
	if _, _, err := r.api.StopStreamContext(ctx, r.channel, r.ts); err != nil {
		slog.Warn("stream: stop failed", "err", err)
	}
	r.ts = ""
}

// Finish flushes, closes the stream and posts anything streaming couldn't deliver.
func (r *replyStream) Finish(ctx context.Context, note string) {
	if note != "" {
		r.Paragraph()
		r.Text(note)
	}
	r.Flush(ctx)
	r.stop(ctx)
	if !r.broken {
		return
	}
	rest := strings.TrimSpace(r.text.String())
	if rest == "" {
		return
	}
	for _, part := range splitRunes(rest, streamRuneCap) {
		if ts, err := postMarkdown(ctx, r.api, r.channel, r.threadTS, part); err != nil {
			slog.Error("stream: fallback post failed", "err", err)
		} else {
			r.posted = append(r.posted, ts)
		}
	}
	r.text.Reset()
}

// postMarkdown posts standard markdown, retrying as plain text if slack
// rejects it, so the words always land somewhere.
func postMarkdown(ctx context.Context, api *slack.Client, channel, threadTS, text string) (string, error) {
	var thread []slack.MsgOption
	if threadTS != "" {
		thread = append(thread, slack.MsgOptionTS(threadTS))
	}
	_, ts, err := api.PostMessageContext(ctx, channel, append(thread, slack.MsgOptionMarkdownText(text))...)
	if err == nil {
		return ts, nil
	}
	slog.Warn("markdown post failed, retrying as plain text", "err", err)
	_, ts, err = api.PostMessageContext(ctx, channel, append(thread, slack.MsgOptionText(text, false))...)
	return ts, err
}

func splitRunes(s string, n int) []string {
	var out []string
	for utf8.RuneCountInString(s) > n {
		rs := []rune(s)
		cut := n
		if i := strings.LastIndex(string(rs[:n]), "\n"); i > n/2 {
			cut = utf8.RuneCountInString(string(rs[:n])[:i])
		}
		out = append(out, string(rs[:cut]))
		s = strings.TrimLeft(string(rs[cut:]), "\n")
	}
	return append(out, s)
}

// toolTitle turns a tool call into a short human label for the task row.
func toolTitle(name string, raw json.RawMessage) (title, details string) {
	var in map[string]any
	_ = json.Unmarshal(raw, &in)
	str := func(k string) string { s, _ := in[k].(string); return s }
	short := func(s string, n int) string {
		s = strings.Join(strings.Fields(s), " ")
		if utf8.RuneCountInString(s) > n {
			return string([]rune(s)[:n-1]) + "…"
		}
		return s
	}
	switch {
	case name == "Bash":
		cmd := str("command")
		if d := str("description"); d != "" {
			return strings.ToLower(short(d, 70)), short(cmd, 300)
		}
		return "running `" + short(cmd, 50) + "`", short(cmd, 300)
	case name == "Read":
		return "reading " + filepath.Base(str("file_path")), str("file_path")
	case name == "Write":
		return "writing " + filepath.Base(str("file_path")), str("file_path")
	case name == "Edit" || name == "MultiEdit":
		return "editing " + filepath.Base(str("file_path")), str("file_path")
	case name == "Grep" || name == "Glob":
		return "searching for `" + short(str("pattern"), 40) + "`", ""
	case name == "WebSearch":
		return "searching the web: " + short(str("query"), 50), ""
	case name == "WebFetch":
		host := str("url")
		if u, err := url.Parse(host); err == nil && u.Host != "" {
			host = u.Host
		}
		return "fetching " + host, str("url")
	case name == "TodoWrite":
		return "updating my todo list", ""
	case name == "Agent" || name == "Task":
		return "subagent: " + short(str("description"), 50), ""
	case strings.HasPrefix(name, "mcp__slack__"):
		return "slack: " + strings.ReplaceAll(strings.TrimPrefix(name, "mcp__slack__"), "_", " "), short(string(raw), 300)
	case strings.HasPrefix(name, "mcp__"):
		parts := strings.SplitN(strings.TrimPrefix(name, "mcp__"), "__", 2)
		if len(parts) == 2 {
			return fmt.Sprintf("%s: %s", parts[0], strings.ReplaceAll(parts[1], "_", " ")), ""
		}
	}
	return strings.ToLower(name), ""
}
