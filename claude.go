package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"strings"
	"sync"
	"sync/atomic"
)

// event is one line of claude's stream-json output, decoded loosely.
type event struct {
	Type      string          `json:"type"`
	Subtype   string          `json:"subtype"`
	SessionID string          `json:"session_id"`
	Event     *streamEvent    `json:"event"`
	Message   json.RawMessage `json:"message"`
	Result    string          `json:"result"`
	IsError   bool            `json:"is_error"`
	CostUSD   float64         `json:"total_cost_usd"`
	ParentID  *string         `json:"parent_tool_use_id"`
}

type streamEvent struct {
	Type         string `json:"type"`
	Index        int    `json:"index"`
	ContentBlock struct {
		Type string `json:"type"`
		ID   string `json:"id"`
		Name string `json:"name"`
	} `json:"content_block"`
	Delta struct {
		Type string `json:"type"`
		Text string `json:"text"`
	} `json:"delta"`
}

type contentBlock struct {
	Type      string          `json:"type"`
	ID        string          `json:"id"`
	Name      string          `json:"name"`
	Input     json.RawMessage `json:"input"`
	ToolUseID string          `json:"tool_use_id"`
	IsError   bool            `json:"is_error"`
	Content   json.RawMessage `json:"content"`
	Text      string          `json:"text"`
}

type apiMessage struct {
	Content []contentBlock `json:"content"`
}

// claudeProc is one long-lived `claude` process speaking stream-json.
// It serves one slack thread; turns are written to stdin one at a time
// and events are delivered on Events until the process exits.
type claudeProc struct {
	cmd    *exec.Cmd
	stdin  io.WriteCloser
	mu     sync.Mutex // guards stdin writes
	Events chan event // closed when the process exits
	reqID  atomic.Int64

	sessionID atomic.Value // string
}

type procOptions struct {
	WorkDir      string
	Model        string
	Resume       string
	MCPURL       string
	SystemAppend string
}

func startClaude(ctx context.Context, o procOptions) (*claudeProc, error) {
	mcpCfg, _ := json.Marshal(map[string]any{
		"mcpServers": map[string]any{
			"slack": map[string]any{"type": "http", "url": o.MCPURL},
		},
	})
	args := []string{
		"-p",
		"--input-format", "stream-json",
		"--output-format", "stream-json",
		"--verbose",
		"--include-partial-messages",
		"--permission-mode", "bypassPermissions",
		"--mcp-config", string(mcpCfg),
		// only our slack tools: the org's claude.ai connectors and plugin
		// servers add ~20% to every fresh session for things we never use
		"--strict-mcp-config",
		"--append-system-prompt", o.SystemAppend,
	}
	if o.Model != "" {
		args = append(args, "--model", o.Model)
	}
	if o.Resume != "" {
		args = append(args, "--resume", o.Resume)
	}

	cmd := exec.CommandContext(ctx, "claude", args...)
	cmd.Dir = o.WorkDir
	cmd.Env = childEnv()
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, fmt.Errorf("claude: stdin pipe: %w", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("claude: stdout pipe: %w", err)
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return nil, fmt.Errorf("claude: stderr pipe: %w", err)
	}
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("claude: start: %w", err)
	}

	p := &claudeProc{cmd: cmd, stdin: stdin, Events: make(chan event, 256)}
	p.sessionID.Store(o.Resume)

	go func() {
		sc := bufio.NewScanner(stderr)
		for sc.Scan() {
			slog.Debug("claude stderr", "line", sc.Text())
		}
	}()
	go func() {
		defer close(p.Events)
		sc := bufio.NewScanner(stdout)
		sc.Buffer(make([]byte, 1<<20), 64<<20)
		for sc.Scan() {
			var ev event
			if err := json.Unmarshal(sc.Bytes(), &ev); err != nil {
				slog.Warn("claude: bad json line", "err", err)
				continue
			}
			if ev.SessionID != "" {
				p.sessionID.Store(ev.SessionID)
			}
			p.Events <- ev
		}
		err := cmd.Wait()
		slog.Info("claude exited", "err", err)
	}()
	return p, nil
}

func (p *claudeProc) SessionID() string {
	s, _ := p.sessionID.Load().(string)
	return s
}

func (p *claudeProc) writeJSON(v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	_, err = p.stdin.Write(append(b, '\n'))
	return err
}

// Send queues a user message. content is either a string or a slice of blocks.
func (p *claudeProc) Send(content any) error {
	return p.writeJSON(map[string]any{
		"type":    "user",
		"message": map[string]any{"role": "user", "content": content},
	})
}

// Interrupt aborts the running turn; the turn ends with an error result.
func (p *claudeProc) Interrupt() error {
	return p.writeJSON(map[string]any{
		"type":       "control_request",
		"request_id": fmt.Sprintf("int-%d", p.reqID.Add(1)),
		"request":    map[string]any{"subtype": "interrupt"},
	})
}

func (p *claudeProc) Close() {
	p.mu.Lock()
	p.stdin.Close()
	p.mu.Unlock()
	if p.cmd.Process != nil {
		_ = p.cmd.Process.Signal(os.Interrupt)
	}
}

// childEnv strips variables that would make claude think it's nested
// inside another claude session (matters when started from one).
func childEnv() []string {
	var out []string
	for _, kv := range os.Environ() {
		k, _, _ := strings.Cut(kv, "=")
		if k == "CLAUDECODE" || k == "CLAUDE_PID" || k == "CLAUDE_EFFORT" || strings.HasPrefix(k, "CLAUDE_CODE_") {
			continue
		}
		out = append(out, kv)
	}
	return out
}
