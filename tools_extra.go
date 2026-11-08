package main

import (
	"context"
	"fmt"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"
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
}
