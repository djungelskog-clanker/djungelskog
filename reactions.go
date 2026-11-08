package main

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/slack-go/slack"
	"github.com/slack-go/slack/slackevents"
)

// reactionAction is something an allowlisted person can trigger by reacting
// to a message with one of the emojis. Either prompt (an agent turn in that
// message's thread) or direct (plain go, no agent) is set.
type reactionAction struct {
	emojis  []string
	label   string // shown in the home tab cheat sheet
	ownOnly bool   // only on my own messages
	prompt  string
	direct  func(b *bot, e *slackevents.ReactionAddedEvent, msg slack.Message) error
}

var reactionActions = []reactionAction{
	{emojis: []string{"loud_sound", "speaker", "sound"}, label: "read it out loud",
		prompt: "read that message out loud: make a voice clip of it with the speak tool (or ~/workspace/voice/speak.sh) and send_file it here. if it's long, read a short summary instead. reply with one short line at most."},
	{emojis: []string{"globe_with_meridians", "earth_africa", "earth_americas", "earth_asia"}, label: "translate it",
		prompt: "translate that message into english. if it's already english, translate it into french. just the translation plus a one-line note on anything that doesn't translate cleanly."},
	{emojis: []string{"memo", "tldr"}, label: "tl;dr of the thread",
		prompt: "give a short tl;dr of the thread this message is in (read_thread it first): who wanted what, what got decided, what's still open. 3-5 lines."},
	{emojis: []string{"robot_face"}, label: "treat it like an @mention",
		prompt: "they want you to look at that message and respond to it as if it had @mentioned you. help with whatever it asks or is about."},
	{emojis: []string{"bug", "beetle"}, label: "debug it",
		prompt: "that message has code, an error or a bug report in it. find what's wrong, explain it in a line or two, and give the fix. if you can reproduce it here, do."},
	{emojis: []string{"mag", "mag_right"}, label: "fact-check it",
		prompt: "fact-check the claims in that message. search the web where needed, say which bits are right, wrong or unclear, and link sources. keep it short and don't be smug about it."},
	{emojis: []string{"art", "frame_with_picture"}, label: "draw it",
		prompt: "make a little picture inspired by that message (python + pillow, an svg rendered to png, ascii art if nothing else works) and send_file it here with a one-line caption."},
	// more actions get added above this line
}

func findReaction(emoji string) *reactionAction {
	emoji = strings.SplitN(emoji, "::", 2)[0] // drop skin tones
	for i := range reactionActions {
		for _, e := range reactionActions[i].emojis {
			if e == emoji {
				return &reactionActions[i]
			}
		}
	}
	return nil
}

// fetchMessage gets one message by ts, whether it's top level or a reply.
func (b *bot) fetchMessage(ctx context.Context, channel, ts string) (slack.Message, error) {
	msgs, _, _, err := b.api.GetConversationRepliesContext(ctx, &slack.GetConversationRepliesParameters{
		ChannelID: channel, Timestamp: ts, Limit: 1, Inclusive: true,
	})
	if err == nil {
		for _, m := range msgs {
			if m.Timestamp == ts {
				return m, nil
			}
		}
	}
	res, err2 := b.api.GetConversationHistoryContext(ctx, &slack.GetConversationHistoryParameters{
		ChannelID: channel, Latest: ts, Inclusive: true, Limit: 1,
	})
	if err2 == nil && len(res.Messages) > 0 && res.Messages[0].Timestamp == ts {
		return res.Messages[0], nil
	}
	if err == nil {
		err = err2
	}
	if err == nil {
		err = fmt.Errorf("message %s not found", ts)
	}
	return slack.Message{}, err
}

func (b *bot) isMine(m slack.Message) bool {
	return m.User == b.botUserID || (m.BotID != "" && m.BotID == b.botID)
}

// handleReaction runs the action for an emoji, if there is one.
func (b *bot) handleReaction(e *slackevents.ReactionAddedEvent) {
	a := findReaction(e.Reaction)
	if a == nil || e.Item.Type != "message" {
		return
	}
	ctx := context.Background()
	msg, err := b.fetchMessage(ctx, e.Item.Channel, e.Item.Timestamp)
	if err != nil {
		slog.Warn("reaction: fetch message", "err", err)
		return
	}
	if a.ownOnly && !b.isMine(msg) {
		return
	}
	slog.Info("reaction action", "emoji", e.Reaction, "user", e.User, "label", a.label)
	if a.direct != nil {
		if err := a.direct(b, e, msg); err != nil {
			slog.Warn("reaction action failed", "emoji", e.Reaction, "err", err)
		}
		return
	}
	b.submitReactionTurn(e, msg, a.prompt)
}

func (b *bot) submitReactionTurn(e *slackevents.ReactionAddedEvent, msg slack.Message, prompt string) {
	threadTS := firstNonEmpty(msg.ThreadTimestamp, msg.Timestamp)
	author := msg.Username
	if msg.User != "" {
		author = b.userName(msg.User) + " (id: " + msg.User + ")"
	}
	if b.isMine(msg) {
		author = "you"
	}
	text := fmt.Sprintf("[slack reaction from %s (id: %s) in %s (%s), thread %s: they reacted :%s: to a message by %s, ts %s]\n%s\n\nthe message:\n%s",
		b.userName(e.User), e.User, b.channelName(e.Item.Channel), e.Item.Channel, threadTS, e.Reaction, author, msg.Timestamp, prompt, msg.Text)
	for _, f := range msg.Files {
		if p, err := b.download(msg.Timestamp, f); err == nil {
			text += fmt.Sprintf("\n[attached file: %s (%s)]", p, f.Mimetype)
		}
	}
	if note := countScreenTime(b.screenTime, e.User, time.Now()); note != "" {
		text += "\n" + note
	}
	b.mgr.Submit(incoming{Channel: e.Item.Channel, ThreadTS: threadTS, TS: msg.Timestamp, User: e.User, Text: text})
}
