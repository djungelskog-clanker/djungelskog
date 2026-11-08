package main

import (
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/slack-go/slack"
)

// the app home tab: what i am, what i can do, how i'm doing.
func (b *bot) publishHome(user string) {
	md := func(s string) slack.Block {
		return slack.NewSectionBlock(slack.NewTextBlockObject(slack.MarkdownType, s, false, false), nil, nil)
	}
	blocks := []slack.Block{
		slack.NewHeaderBlock(slack.NewTextBlockObject(slack.PlainTextType, "djungelskog", false, false)),
		md("an ikea bear living in a little vm. @ me in a channel or thread and i'll do silly (or useful) stuff. say `stop` or react :octagonal_sign: to stop me."),
		md(fmt.Sprintf("*status*\n%s", strings.TrimSpace(b.publicStatus()))),
		slack.NewDividerBlock(),
		md("*reaction tricks* (react to any message in a channel i'm in)\n" + reactionCheatSheet()),
	}
	if !b.cfg.AllowFrom.Has(user) {
		blocks = append(blocks, md("_you're not on my allowlist, so i won't answer you yet. ask alice._"))
	} else {
		blocks = append(blocks, slack.NewDividerBlock(), md("*just for you*\n"+b.personalHome(user)))
	}
	_, err := b.api.PublishView(user, slack.HomeTabViewRequest{Type: slack.VTHomeTab, Blocks: slack.Blocks{BlockSet: blocks}}, "")
	if err != nil {
		slog.Warn("publish home", "err", err)
	}
}

func reactionCheatSheet() string {
	var sb strings.Builder
	for _, a := range reactionActions {
		var es []string
		for _, e := range a.emojis {
			es = append(es, ":"+e+":")
		}
		fmt.Fprintf(&sb, "%s  %s\n", strings.Join(es, " "), a.label)
	}
	return sb.String()
}

// publicStatus is the part of bot_status that's fine for anyone to see.
func (b *bot) publicStatus() string {
	live, busy, _, _ := b.mgr.snapshot()
	return fmt.Sprintf("running `%s`, up since %s\n%d conversations awake, %d busy right now",
		version(), b.started.In(berlin).Format("Mon 15:04 (berlin)"), live, busy)
}

// personalHome: your spend, screen time and latest bookmarks.
func (b *bot) personalHome(user string) string {
	var sb strings.Builder
	today := time.Now().In(berlin).Format("2006-01-02")
	if d := b.costs.Get()[today]; d != nil {
		fmt.Fprintf(&sb, "you've cost alice $%.2f today (everyone: $%.2f)\n", d.ByUser[user], d.USD)
	} else {
		sb.WriteString("nobody has cost anything today yet\n")
	}
	st := b.screenTime.Get()
	if limit, ok := st.Limits[user]; ok {
		used := 0
		if c, ok := st.Counts[user]; ok && c.Day == today {
			used = c.N
		}
		fmt.Fprintf(&sb, "screen time: %d of %d messages used today\n", used, limit)
	}
	bms := b.bookmarks.Get()[user]
	if len(bms) == 0 {
		sb.WriteString("no bookmarks yet, react :bookmark: to save a message")
	} else {
		sb.WriteString("latest bookmarks:\n")
		for i := len(bms) - 1; i >= 0 && i >= len(bms)-5; i-- {
			fmt.Fprintf(&sb, "• <%s|%s>: %s\n", bms[i].Link, bms[i].Author, truncRunes(bms[i].Snippet, 80))
		}
	}
	return sb.String()
}
