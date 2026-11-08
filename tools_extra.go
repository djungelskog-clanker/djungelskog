package main

import (
	"context"
	"fmt"
	"strings"

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

	// more tools get added above this line
}
