package main

import (
	"log/slog"

	"github.com/slack-go/slack"
)

// little status reactions on the messages i'm handling, so people can see
// at a glance that i got it, that it's queued, or that something broke.
const (
	ackWorking = "hourglass_flowing_sand"
	ackQueued  = "inbox_tray"
	ackFailed  = "pf"
)

func (b *bot) reactAsync(channel, ts, name string) {
	if ts == "" {
		return
	}
	go func() {
		if err := b.api.AddReaction(name, slack.NewRefToMessage(channel, ts)); err != nil {
			slog.Debug("ack react", "name", name, "err", err)
		}
	}()
}

// reactNow is reactAsync without the goroutine, for when the reaction has to
// land before a later removal (otherwise a fast turn can leave it stuck).
func (b *bot) reactNow(channel, ts, name string) {
	if ts == "" {
		return
	}
	if err := b.api.AddReaction(name, slack.NewRefToMessage(channel, ts)); err != nil {
		slog.Debug("ack react", "name", name, "err", err)
	}
}

func (b *bot) unreactAsync(channel, ts, name string) {
	if ts == "" {
		return
	}
	go func() {
		if err := b.api.RemoveReaction(name, slack.NewRefToMessage(channel, ts)); err != nil {
			slog.Debug("ack unreact", "name", name, "err", err)
		}
	}()
}
