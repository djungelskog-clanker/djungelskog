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

	// more tools get added above this line
}
