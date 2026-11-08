package main

import (
	"strings"

	"github.com/slack-go/slack/slackevents"
)

// handleEdit re-asks when someone edits a message that already reached me,
// so fixing a typo in the question gets a fresh answer.
func (b *bot) handleEdit(e *slackevents.MessageEvent) {
	n, old := e.Message, e.PreviousMessage
	if n == nil || old == nil || n.User == "" || n.User == b.botUserID || n.BotID != "" {
		return
	}
	if strings.TrimSpace(n.Text) == strings.TrimSpace(old.Text) {
		return // unfurls and attachments also send message_changed
	}
	if !b.mgr.knowsMessage(n.Timestamp) {
		return
	}
	isDM := strings.HasPrefix(e.Channel, "D")
	if !isDM && !strings.Contains(n.Text, "<@"+b.botUserID) {
		return
	}
	threadTS := firstNonEmpty(n.ThreadTimestamp, n.Timestamp)
	text := "[they edited a message they already sent you (ts " + n.Timestamp + "). the new version is below; answer that instead if it changes anything]\n" + n.Text
	b.dispatch(e.Channel, threadTS, n.Timestamp, n.User, text, nil, "", false)
}
