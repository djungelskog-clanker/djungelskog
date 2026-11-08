package main

import (
	"context"
	"fmt"
	"math/rand/v2"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/slack-go/slack"
)

type (
	bookmarksIn struct {
		User string `json:"user,omitempty" jsonschema:"slack user id; defaults to whoever sent the current message"`
	}
)

// addExtraTools registers the newer tools; tools.go has the original set.
func (b *bot) addExtraTools(s *mcp.Server, t *thread) {
	mcp.AddTool(s, &mcp.Tool{Name: "list_bookmarks", Description: "List the messages someone bookmarked by reacting :bookmark: to them."},
		func(ctx context.Context, _ *mcp.CallToolRequest, in bookmarksIn) (*mcp.CallToolResult, any, error) {
			user := firstNonEmpty(in.User, t.current().User)
			bms := b.bookmarks.Get()[user]
			if len(bms) == 0 {
				return textResult("%s has no bookmarks yet (react :bookmark: to a message to save it)", b.userName(user))
			}
			var sb strings.Builder
			for _, bm := range bms {
				fmt.Fprintf(&sb, "- %s by %s: %q %s\n", bm.At.Format("2006-01-02"), bm.Author, bm.Snippet, bm.Link)
			}
			return textResult("%s", sb.String())
		})
	type editMsgIn struct {
		Channel string `json:"channel,omitempty" jsonschema:"defaults to the current conversation"`
		TS      string `json:"ts" jsonschema:"ts of one of your own messages"`
		Text    string `json:"text" jsonschema:"new markdown text"`
	}
	mcp.AddTool(s, &mcp.Tool{Name: "edit_message", Description: "Replace the text of one of your own earlier messages (e.g. to fix a mistake). Only works on messages you posted."},
		func(ctx context.Context, _ *mcp.CallToolRequest, in editMsgIn) (*mcp.CallToolResult, any, error) {
			ch := firstNonEmpty(in.Channel, t.channel)
			if id, err := b.resolveChannel(ch); err == nil {
				ch = id
			}
			if _, _, _, err := b.api.UpdateMessageContext(ctx, ch, in.TS, slack.MsgOptionMarkdownText(in.Text)); err != nil {
				// same fallback as postMarkdown
				if _, _, _, err2 := b.api.UpdateMessageContext(ctx, ch, in.TS, slack.MsgOptionText(in.Text, false)); err2 != nil {
					return errResult(err)
				}
			}
			return textResult("edited %s", in.TS)
		})

	type deleteMsgIn struct {
		Channel string `json:"channel,omitempty" jsonschema:"defaults to the current conversation"`
		TS      string `json:"ts" jsonschema:"ts of one of your own messages"`
	}
	mcp.AddTool(s, &mcp.Tool{Name: "delete_message", Description: "Delete one of your own messages. Slack refuses for anyone else's."},
		func(ctx context.Context, _ *mcp.CallToolRequest, in deleteMsgIn) (*mcp.CallToolResult, any, error) {
			ch := firstNonEmpty(in.Channel, t.channel)
			if id, err := b.resolveChannel(ch); err == nil {
				ch = id
			}
			if _, _, err := b.api.DeleteMessageContext(ctx, ch, in.TS); err != nil {
				return errResult(err)
			}
			return textResult("deleted %s", in.TS)
		})

	type getReactionsIn struct {
		Channel string `json:"channel,omitempty" jsonschema:"channel id, #name or a message link; defaults to the current conversation"`
		TS      string `json:"ts,omitempty" jsonschema:"message ts (not needed with a link)"`
	}
	mcp.AddTool(s, &mcp.Tool{Name: "get_reactions", Description: "List the emoji reactions on a message and who added each."},
		func(ctx context.Context, _ *mcp.CallToolRequest, in getReactionsIn) (*mcp.CallToolResult, any, error) {
			ch, ts := parseRef(firstNonEmpty(in.Channel, t.channel))
			ts = firstNonEmpty(in.TS, ts)
			id, err := b.resolveChannel(ch)
			if err != nil {
				return errResult(err)
			}
			item, err := b.api.GetReactionsContext(ctx, slack.NewRefToMessage(id, ts), slack.GetReactionsParameters{Full: true})
			if err != nil {
				return errResult(err)
			}
			if len(item.Reactions) == 0 {
				return textResult("no reactions")
			}
			var sb strings.Builder
			for _, r := range item.Reactions {
				var names []string
				for _, u := range r.Users {
					names = append(names, b.userName(u))
				}
				fmt.Fprintf(&sb, ":%s: x%d: %s\n", r.Name, r.Count, strings.Join(names, ", "))
			}
			return textResult("%s", sb.String())
		})

	type sendDMIn struct {
		User string `json:"user" jsonschema:"slack user id (U...)"`
		Text string `json:"text" jsonschema:"markdown text"`
	}
	mcp.AddTool(s, &mcp.Tool{Name: "send_dm", Description: "Send someone a direct message from you. Only when asked to, and never to spam people."},
		func(ctx context.Context, _ *mcp.CallToolRequest, in sendDMIn) (*mcp.CallToolResult, any, error) {
			user := strings.Trim(in.User, "<@> ")
			ch, _, _, err := b.api.OpenConversationContext(ctx, &slack.OpenConversationParameters{Users: []string{user}})
			if err != nil {
				return errResult(err)
			}
			ts, err := postMarkdown(ctx, b.api, ch.ID, "", in.Text)
			if err != nil {
				return errResult(err)
			}
			return textResult("sent to %s (dm %s, ts=%s)", b.userName(user), ch.ID, ts)
		})

	type whisperIn struct {
		User    string `json:"user" jsonschema:"slack user id who should see it"`
		Text    string `json:"text"`
		Channel string `json:"channel,omitempty" jsonschema:"defaults to the current conversation"`
	}
	mcp.AddTool(s, &mcp.Tool{Name: "whisper", Description: "Post an ephemeral message in a channel that only one person can see (it isn't saved in history). Good for secrets in games, private hints, etc."},
		func(ctx context.Context, _ *mcp.CallToolRequest, in whisperIn) (*mcp.CallToolResult, any, error) {
			ch := firstNonEmpty(in.Channel, t.channel)
			if id, err := b.resolveChannel(ch); err == nil {
				ch = id
			}
			opts := []slack.MsgOption{slack.MsgOptionText(in.Text, false)}
			if ch == t.channel && t.threadTS != "" {
				opts = append(opts, slack.MsgOptionTS(t.threadTS))
			}
			if _, err := b.api.PostEphemeralContext(ctx, ch, strings.Trim(in.User, "<@> "), opts...); err != nil {
				return errResult(err)
			}
			return textResult("whispered to %s", b.userName(in.User))
		})

	type topicIn struct {
		Channel string `json:"channel,omitempty" jsonschema:"defaults to the current conversation"`
		Topic   string `json:"topic"`
	}
	mcp.AddTool(s, &mcp.Tool{Name: "set_channel_topic", Description: "Change a channel's topic. Only when asked."},
		func(ctx context.Context, _ *mcp.CallToolRequest, in topicIn) (*mcp.CallToolResult, any, error) {
			id, err := b.resolveChannel(firstNonEmpty(in.Channel, t.channel))
			if err != nil {
				return errResult(err)
			}
			if _, err := b.api.SetTopicOfConversationContext(ctx, id, in.Topic); err != nil {
				return errResult(err)
			}
			return textResult("topic of %s is now %q", b.channelName(id), in.Topic)
		})

	type statsIn struct {
		Channel string `json:"channel,omitempty" jsonschema:"defaults to the current conversation"`
		Limit   int    `json:"limit,omitempty" jsonschema:"how many recent top-level messages to scan (default 500, max 3000)"`
	}
	mcp.AddTool(s, &mcp.Tool{Name: "channel_stats", Description: "Stats over a channel's recent history: top posters, most used emojis (in text and reactions), busiest hours."},
		func(ctx context.Context, _ *mcp.CallToolRequest, in statsIn) (*mcp.CallToolResult, any, error) {
			id, err := b.resolveChannel(firstNonEmpty(in.Channel, t.channel))
			if err != nil {
				return errResult(err)
			}
			want := clamp(in.Limit, 500, 3000)
			var msgs []slack.Message
			cursor := ""
			for len(msgs) < want {
				res, err := b.api.GetConversationHistoryContext(ctx, &slack.GetConversationHistoryParameters{ChannelID: id, Limit: 200, Cursor: cursor})
				if err != nil {
					return errResult(err)
				}
				msgs = append(msgs, res.Messages...)
				if cursor = res.ResponseMetaData.NextCursor; cursor == "" || !res.HasMore {
					break
				}
			}
			return textResult("%s", b.channelStats(id, msgs))
		})

	type randomMemberIn struct {
		Channel string `json:"channel,omitempty" jsonschema:"defaults to the current conversation"`
		Count   int    `json:"count,omitempty" jsonschema:"how many to pick (default 1)"`
	}
	mcp.AddTool(s, &mcp.Tool{Name: "random_member", Description: "Pick random (non-bot) members of a channel, e.g. for 'who has to do X' or giveaways. Uses a real random number generator, not your guess."},
		func(ctx context.Context, _ *mcp.CallToolRequest, in randomMemberIn) (*mcp.CallToolResult, any, error) {
			id, err := b.resolveChannel(firstNonEmpty(in.Channel, t.channel))
			if err != nil {
				return errResult(err)
			}
			var members []string
			cursor := ""
			for {
				ids, next, err := b.api.GetUsersInConversationContext(ctx, &slack.GetUsersInConversationParameters{ChannelID: id, Cursor: cursor, Limit: 1000})
				if err != nil {
					return errResult(err)
				}
				members = append(members, ids...)
				if cursor = next; cursor == "" || len(members) > 5000 {
					break
				}
			}
			var humans []string
			for _, u := range members {
				if u == b.botUserID {
					continue
				}
				if info, err := b.api.GetUserInfoContext(ctx, u); err == nil && (info.IsBot || info.Deleted) {
					continue
				}
				humans = append(humans, u)
				if len(humans) >= 300 {
					break // enough to pick from, and saves api calls
				}
			}
			n := min(max(in.Count, 1), len(humans))
			rand.Shuffle(len(humans), func(i, j int) { humans[i], humans[j] = humans[j], humans[i] })
			var picked []string
			for _, u := range humans[:n] {
				picked = append(picked, fmt.Sprintf("%s (<@%s>)", b.userName(u), u))
			}
			return textResult("picked %d of %d people: %s", n, len(humans), strings.Join(picked, ", "))
		})

	type pollIn struct {
		Question string   `json:"question"`
		Options  []string `json:"options" jsonschema:"2 to 10 options"`
	}
	mcp.AddTool(s, &mcp.Tool{Name: "make_poll", Description: "Post a poll in the current thread: numbered options, people vote with the number reactions. Returns the poll's ts for poll_results."},
		func(ctx context.Context, _ *mcp.CallToolRequest, in pollIn) (*mcp.CallToolResult, any, error) {
			if len(in.Options) < 2 || len(in.Options) > len(pollEmojis) {
				return errResult(fmt.Errorf("need 2 to %d options", len(pollEmojis)))
			}
			var sb strings.Builder
			fmt.Fprintf(&sb, "**poll: %s**\n", in.Question)
			for i, o := range in.Options {
				fmt.Fprintf(&sb, ":%s: %s\n", pollEmojis[i], o)
			}
			sb.WriteString("_vote with the reactions_")
			ts, err := postMarkdown(ctx, b.api, t.channel, t.threadTS, sb.String())
			if err != nil {
				return errResult(err)
			}
			for i := range in.Options {
				_ = b.api.AddReactionContext(ctx, pollEmojis[i], slack.NewRefToMessage(t.channel, ts))
			}
			return textResult("poll posted, ts=%s", ts)
		})

	type pollResultsIn struct {
		Channel string `json:"channel,omitempty" jsonschema:"defaults to the current conversation"`
		TS      string `json:"ts" jsonschema:"ts of the poll message"`
	}
	mcp.AddTool(s, &mcp.Tool{Name: "poll_results", Description: "Count the votes on a poll made with make_poll (your own starter reactions don't count)."},
		func(ctx context.Context, _ *mcp.CallToolRequest, in pollResultsIn) (*mcp.CallToolResult, any, error) {
			ch := firstNonEmpty(in.Channel, t.channel)
			if id, err := b.resolveChannel(ch); err == nil {
				ch = id
			}
			item, err := b.api.GetReactionsContext(ctx, slack.NewRefToMessage(ch, in.TS), slack.GetReactionsParameters{Full: true})
			if err != nil {
				return errResult(err)
			}
			msg, _ := b.fetchMessage(ctx, ch, in.TS)
			return textResult("%s\n%s", msg.Text, b.tallyPoll(item.Reactions))
		})

	type presenceIn struct {
		User string `json:"user" jsonschema:"slack user id"`
	}
	mcp.AddTool(s, &mcp.Tool{Name: "user_presence", Description: "Is someone active on slack right now, and what time is it where they are."},
		func(ctx context.Context, _ *mcp.CallToolRequest, in presenceIn) (*mcp.CallToolResult, any, error) {
			user := strings.Trim(in.User, "<@> ")
			p, err := b.api.GetUserPresenceContext(ctx, user)
			if err != nil {
				return errResult(err)
			}
			local := ""
			if u, err := b.api.GetUserInfoContext(ctx, user); err == nil {
				if loc, err := time.LoadLocation(u.TZ); err == nil {
					local = fmt.Sprintf(", their local time is %s (%s)", time.Now().In(loc).Format("Mon 15:04"), u.TZ)
				}
			}
			return textResult("%s is %s%s", b.userName(user), p.Presence, local)
		})

	type speakIn struct {
		Text    string `json:"text" jsonschema:"what to say (keep it under ~600 characters)"`
		Comment string `json:"comment,omitempty" jsonschema:"optional short message posted with the clip"`
	}
	mcp.AddTool(s, &mcp.Tool{Name: "speak", Description: "Say something out loud: makes a voice clip in your own voice (kokoro, english) and uploads it to the current thread."},
		func(ctx context.Context, _ *mcp.CallToolRequest, in speakIn) (*mcp.CallToolResult, any, error) {
			text := truncRunes(strings.TrimSpace(in.Text), 1200)
			if text == "" {
				return errResult(fmt.Errorf("nothing to say"))
			}
			out := filepath.Join(b.cfg.DataDir, "files", fmt.Sprintf("voice-%d.m4a", time.Now().UnixNano()))
			cctx, cancel := context.WithTimeout(ctx, 3*time.Minute)
			defer cancel()
			script := filepath.Join(os.Getenv("HOME"), "workspace/voice/speak.sh")
			if msg, err := exec.CommandContext(cctx, script, text, out).CombinedOutput(); err != nil {
				return errResult(fmt.Errorf("%v: %s", err, truncRunes(string(msg), 300)))
			}
			defer os.Remove(out)
			st, err := os.Stat(out)
			if err != nil {
				return errResult(err)
			}
			_, err = b.api.UploadFileContext(ctx, slack.UploadFileParameters{
				File: out, FileSize: int(st.Size()), Filename: "djungelskog.m4a", Title: "djungelskog says",
				InitialComment: in.Comment, Channel: t.channel, ThreadTimestamp: t.threadTS,
			})
			if err != nil {
				return errResult(err)
			}
			return textResult("posted a voice clip")
		})

	type usageIn struct {
		Days int `json:"days,omitempty" jsonschema:"how many days back including today (default 7, max 90)"`
	}
	mcp.AddTool(s, &mcp.Tool{Name: "usage_stats", Description: "How much your claude turns cost (api-equivalent usd, tracked since the cost tracker went in): per day, per person, priciest threads, plus thumbs up/down feedback on your messages."},
		func(ctx context.Context, _ *mcp.CallToolRequest, in usageIn) (*mcp.CallToolResult, any, error) {
			return textResult("%s", b.usageReport(clamp(in.Days, 7, 90), time.Now()))
		})

	mcp.AddTool(s, &mcp.Tool{Name: "bot_status", Description: "Your own bot's status: version, uptime, how many threads are live/busy, queued messages, scheduled jobs, today's spend."},
		func(ctx context.Context, _ *mcp.CallToolRequest, _ emptyIn) (*mcp.CallToolResult, any, error) {
			return textResult("%s", b.statusReport())
		})

	type changelogIn struct {
		Count int `json:"count,omitempty" jsonschema:"how many commits (default 20, max 100)"`
	}
	mcp.AddTool(s, &mcp.Tool{Name: "changelog", Description: "Recent commits to your own bot's code, and which one is running right now."},
		func(ctx context.Context, _ *mcp.CallToolRequest, in changelogIn) (*mcp.CallToolResult, any, error) {
			cmd := exec.CommandContext(ctx, "git", "log", "--date=format:%Y-%m-%d %H:%M", "--format=%h %ad %s", "-n", fmt.Sprint(clamp(in.Count, 20, 100)))
			cmd.Dir = botSourceDir()
			out, err := cmd.Output()
			if err != nil {
				return errResult(err)
			}
			return textResult("running: %s\n%s", version(), out)
		})

	type setModelIn struct {
		Model string `json:"model" jsonschema:"opus, sonnet, haiku, fable, a full model id, or default"`
	}
	mcp.AddTool(s, &mcp.Tool{Name: "set_model", Description: "Switch which claude model this thread runs on, starting from the next message (the conversation carries over). Only when someone asks. Cheaper models (haiku, sonnet) are fine for light chat."},
		func(ctx context.Context, _ *mcp.CallToolRequest, in setModelIn) (*mcp.CallToolResult, any, error) {
			model := strings.ToLower(strings.TrimSpace(in.Model))
			switch model {
			case "default", "reset", "":
				model = ""
			case "opus", "sonnet", "haiku", "fable":
			default:
				if !strings.HasPrefix(model, "claude-") {
					return errResult(fmt.Errorf("unknown model %q", in.Model))
				}
			}
			if err := b.mgr.setModel(t, model); err != nil {
				return errResult(err)
			}
			return textResult("this thread switches to %s from the next message", firstNonEmpty(model, "the default model"))
		})

	// more tools get added above this line
}

var textEmojiRe = regexp.MustCompile(`:([a-z0-9_+'-]+):`)

type counted struct {
	key string
	n   int
}

func topN(m map[string]int, n int) []counted {
	var out []counted
	for k, v := range m {
		out = append(out, counted{k, v})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].n != out[j].n {
			return out[i].n > out[j].n
		}
		return out[i].key < out[j].key
	})
	if len(out) > n {
		out = out[:n]
	}
	return out
}

func (b *bot) channelStats(id string, msgs []slack.Message) string {
	posters, emojis, reacts, hours := map[string]int{}, map[string]int{}, map[string]int{}, map[string]int{}
	var oldest, newest string
	for _, m := range msgs {
		if m.User != "" {
			posters[m.User]++
		}
		for _, e := range textEmojiRe.FindAllStringSubmatch(m.Text, -1) {
			if !strings.HasPrefix(e[1], "skin-tone") {
				emojis[e[1]]++
			}
		}
		for _, r := range m.Reactions {
			reacts[strings.SplitN(r.Name, "::", 2)[0]] += r.Count
		}
		hours[tsTimeIn(m.Timestamp, berlin).Format("15")]++
		if oldest == "" || m.Timestamp < oldest {
			oldest = m.Timestamp
		}
		if m.Timestamp > newest {
			newest = m.Timestamp
		}
	}
	var sb strings.Builder
	fmt.Fprintf(&sb, "%s: %d top-level messages from %s to %s (berlin time)\n", b.channelName(id), len(msgs), tsTime(oldest), tsTime(newest))
	sb.WriteString("\ntop posters:\n")
	for _, c := range topN(posters, 10) {
		fmt.Fprintf(&sb, "  %s (%s): %d\n", b.userName(c.key), c.key, c.n)
	}
	sb.WriteString("\nemojis in message text:\n")
	for _, c := range topN(emojis, 25) {
		fmt.Fprintf(&sb, "  :%s: %d\n", c.key, c.n)
	}
	sb.WriteString("\nreactions:\n")
	for _, c := range topN(reacts, 25) {
		fmt.Fprintf(&sb, "  :%s: %d\n", c.key, c.n)
	}
	sb.WriteString("\nmessages per hour:\n")
	for h := 0; h < 24; h++ {
		k := fmt.Sprintf("%02d", h)
		if hours[k] > 0 {
			fmt.Fprintf(&sb, "  %s:00 %s %d\n", k, strings.Repeat("#", min(hours[k], 40)), hours[k])
		}
	}
	return sb.String()
}

func tsTimeIn(ts string, loc *time.Location) time.Time {
	var sec int64
	fmt.Sscanf(ts, "%d", &sec)
	return time.Unix(sec, 0).In(loc)
}

var pollEmojis = []string{"one", "two", "three", "four", "five", "six", "seven", "eight", "nine", "keycap_ten"}

func (b *bot) tallyPoll(rs []slack.ItemReaction) string {
	var sb strings.Builder
	for i, e := range pollEmojis {
		for _, r := range rs {
			if r.Name != e {
				continue
			}
			var voters []string
			for _, u := range r.Users {
				if u != b.botUserID {
					voters = append(voters, b.userName(u))
				}
			}
			fmt.Fprintf(&sb, "option %d: %d votes (%s)\n", i+1, len(voters), strings.Join(voters, ", "))
		}
	}
	if sb.Len() == 0 {
		return "no votes yet"
	}
	return sb.String()
}

func (b *bot) usageReport(days int, now time.Time) string {
	v := b.costs.Get()
	from := now.AddDate(0, 0, -(days - 1))
	usd, turns := spentSince(v, from)
	var sb strings.Builder
	fmt.Fprintf(&sb, "last %d days: $%.2f over %d turns\n", days, usd, turns)
	users, threads := map[string]int{}, map[string]int{}
	userUSD, threadUSD := map[string]float64{}, map[string]float64{}
	for i := 0; i < days; i++ {
		day := now.AddDate(0, 0, -i).In(berlin).Format("2006-01-02")
		d := v[day]
		if d == nil {
			continue
		}
		fmt.Fprintf(&sb, "  %s: $%.2f (%d turns)\n", day, d.USD, d.Turns)
		for u, x := range d.ByUser {
			userUSD[u] += x
			users[u] = int(userUSD[u] * 100)
		}
		for th, x := range d.ByThrd {
			threadUSD[th] += x
			threads[th] = int(threadUSD[th] * 100)
		}
	}
	sb.WriteString("by person:\n")
	for _, c := range topN(users, 10) {
		fmt.Fprintf(&sb, "  %s: $%.2f\n", b.userName(c.key), userUSD[c.key])
	}
	sb.WriteString("priciest threads:\n")
	for _, c := range topN(threads, 5) {
		ch, ts, _ := strings.Cut(c.key, ":")
		fmt.Fprintf(&sb, "  %s thread %s: $%.2f\n", b.channelName(ch), ts, threadUSD[c.key])
	}
	good, bad := 0, 0
	for _, f := range b.feedback.Get() {
		if f.At.After(from) {
			if f.Good {
				good++
			} else {
				bad++
			}
		}
	}
	fmt.Fprintf(&sb, "feedback on my messages: %d good, %d bad\n", good, bad)
	return sb.String()
}

func (b *bot) statusReport() string {
	live, busy, queued, keys := b.mgr.snapshot()
	usd, turns := spentSince(b.costs.Get(), time.Now())
	var sb strings.Builder
	fmt.Fprintf(&sb, "version %s, up %s (since %s)\n", version(), time.Since(b.started).Round(time.Minute), b.started.Format("Mon 15:04"))
	fmt.Fprintf(&sb, "threads: %d with a claude process, %d busy, %d messages queued\n", live, busy, queued)
	for _, k := range keys {
		ch, ts, _ := strings.Cut(k, ":")
		fmt.Fprintf(&sb, "  busy: %s thread %s\n", b.channelName(ch), ts)
	}
	fmt.Fprintf(&sb, "scheduled jobs: %d\n", len(b.sched.store.Get()))
	fmt.Fprintf(&sb, "today: $%.2f over %d turns\n", usd, turns)
	return sb.String()
}

// botSourceDir is where the bot's git checkout lives (the working dir of
// the service).
func botSourceDir() string {
	if wd, err := os.Getwd(); err == nil {
		if _, err := os.Stat(filepath.Join(wd, "go.mod")); err == nil {
			return wd
		}
	}
	return filepath.Join(os.Getenv("HOME"), "workspace/slackbot")
}
