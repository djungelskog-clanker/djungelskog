package main

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/slack-go/slack"
	"github.com/slack-go/slack/slackevents"
)

// deleteMine removes one of my own messages. slack only lets a bot delete
// what it posted, so this can't touch anyone else's.
func deleteMine(b *bot, e *slackevents.ReactionAddedEvent, msg slack.Message) error {
	_, _, err := b.api.DeleteMessageContext(context.Background(), e.Item.Channel, msg.Timestamp)
	return err
}

// remindLater schedules a ping for the person who reacted, in the same thread.
func remindLater(b *bot, e *slackevents.ReactionAddedEvent, msg slack.Message) error {
	link, _ := b.api.GetPermalink(&slack.PermalinkParameters{Channel: e.Item.Channel, Ts: msg.Timestamp})
	th := &thread{channel: e.Item.Channel, threadTS: firstNonEmpty(msg.ThreadTimestamp, msg.Timestamp)}
	prompt := fmt.Sprintf("reminder: <@%s> reacted :alarm_clock: an hour ago to this message so you'd remind them about it. "+
		"ping them (<@%s>) with a short reminder and what the message was about.\nlink: %s\nmessage: %s",
		e.User, e.User, link, truncRunes(msg.Text, 1500))
	_, err := b.sched.Add("1h", prompt, "alarm clock reminder", th, e.User, false)
	if err == nil {
		_ = b.api.AddReaction("ok_hand", slack.NewRefToMessage(e.Item.Channel, msg.Timestamp))
	}
	return err
}

func truncRunes(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}

var _ = strings.TrimSpace
var _ = time.Now
