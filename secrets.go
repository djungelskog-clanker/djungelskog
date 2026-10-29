package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/slack-go/slack"
	"github.com/slack-go/slack/socketmode"
)

// Secrets are typed into a slack modal and written straight to disk by the
// bot. They never pass through a chat message or the model's context, so
// they don't end up in slack history or session transcripts.

const (
	secretButtonAction = "secret_open"
	secretModalID      = "secret_submit"
)

var secretNameRe = regexp.MustCompile(`^[a-z0-9_]{1,64}$`)

type secretMeta struct {
	Name     string `json:"name"`
	Channel  string `json:"channel"`
	ThreadTS string `json:"thread_ts"`
	MsgTS    string `json:"msg_ts"`
}

func (b *bot) secretPath(name string) string { return filepath.Join(b.cfg.SecretsDir, name) }

func (b *bot) readSecret(name string) (string, error) {
	v, err := os.ReadFile(b.secretPath(name))
	if err != nil {
		return "", fmt.Errorf("secret %q isn't set (ask someone to fill it in with request_secret)", name)
	}
	return strings.TrimSpace(string(v)), nil
}

func (b *bot) listSecrets() []string {
	ents, _ := os.ReadDir(b.cfg.SecretsDir)
	var names []string
	for _, e := range ents {
		if !e.IsDir() {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)
	return names
}

// requestSecret posts a button in the thread; clicking it opens the modal.
func (b *bot) requestSecret(ctx context.Context, t *thread, name, why string) error {
	if !secretNameRe.MatchString(name) {
		return fmt.Errorf("secret names are lowercase letters, digits and underscores")
	}
	btn := slack.NewButtonBlockElement(secretButtonAction, name, slack.NewTextBlockObject("plain_text", "enter "+name, false, false))
	btn.Style = slack.StylePrimary
	text := fmt.Sprintf("i need a secret: `%s`. %s\nclick the button to type it in privately. it goes straight to disk and never shows up in chat or in my context.", name, why)
	_, _, err := b.api.PostMessageContext(ctx, t.channel,
		slack.MsgOptionTS(t.threadTS),
		slack.MsgOptionText(text, false),
		slack.MsgOptionBlocks(
			slack.NewSectionBlock(slack.NewTextBlockObject("mrkdwn", text, false, false), nil, nil),
			slack.NewActionBlock("secret_actions", btn),
		))
	return err
}

func (b *bot) handleInteraction(evt socketmode.Event) {
	cb, ok := evt.Data.(slack.InteractionCallback)
	if !ok {
		b.sm.Ack(*evt.Request)
		return
	}
	switch cb.Type {
	case slack.InteractionTypeBlockActions:
		b.sm.Ack(*evt.Request)
		for _, a := range cb.ActionCallback.BlockActions {
			if a.ActionID == secretButtonAction {
				b.openSecretModal(cb, a.Value)
			}
		}
	case slack.InteractionTypeViewSubmission:
		if cb.View.CallbackID != secretModalID {
			b.sm.Ack(*evt.Request)
			return
		}
		b.sm.Ack(*evt.Request) // closes the modal
		b.saveSecret(cb)
	default:
		b.sm.Ack(*evt.Request)
	}
}

func (b *bot) openSecretModal(cb slack.InteractionCallback, name string) {
	if !b.cfg.AllowFrom[cb.User.ID] {
		b.ephemeral(cb.Channel.ID, cb.User.ID, "only people on my allowlist can set secrets")
		return
	}
	meta, _ := json.Marshal(secretMeta{Name: name, Channel: cb.Channel.ID, ThreadTS: firstNonEmpty(cb.Message.ThreadTimestamp, cb.Message.Timestamp), MsgTS: cb.Message.Timestamp})
	input := slack.NewPlainTextInputBlockElement(slack.NewTextBlockObject("plain_text", "paste it here", false, false), "value")
	view := slack.ModalViewRequest{
		Type:            slack.VTModal,
		CallbackID:      secretModalID,
		PrivateMetadata: string(meta),
		Title:           slack.NewTextBlockObject("plain_text", "secret for djungelskog", false, false),
		Submit:          slack.NewTextBlockObject("plain_text", "save", false, false),
		Close:           slack.NewTextBlockObject("plain_text", "cancel", false, false),
		Blocks: slack.Blocks{BlockSet: []slack.Block{
			slack.NewInputBlock("secret", slack.NewTextBlockObject("plain_text", name, false, false), nil, input),
		}},
	}
	if _, err := b.api.OpenView(cb.TriggerID, view); err != nil {
		slog.Error("open secret modal", "err", err)
	}
}

func (b *bot) saveSecret(cb slack.InteractionCallback) {
	var meta secretMeta
	if err := json.Unmarshal([]byte(cb.View.PrivateMetadata), &meta); err != nil || !secretNameRe.MatchString(meta.Name) {
		slog.Error("secret modal: bad metadata", "err", err)
		return
	}
	if !b.cfg.AllowFrom[cb.User.ID] {
		return
	}
	value := strings.TrimSpace(cb.View.State.Values["secret"]["value"].Value)
	if value == "" {
		return
	}
	if err := os.WriteFile(b.secretPath(meta.Name), []byte(value+"\n"), 0o600); err != nil {
		slog.Error("save secret", "name", meta.Name, "err", err)
		b.postPlain(meta.Channel, meta.ThreadTS, "couldn't save `"+meta.Name+"` :pf:")
		return
	}
	slog.Info("secret saved", "name", meta.Name, "by", cb.User.ID)
	done := fmt.Sprintf("`%s` saved by %s", meta.Name, b.userName(cb.User.ID))
	if _, _, _, err := b.api.UpdateMessage(meta.Channel, meta.MsgTS, slack.MsgOptionText(done, false), slack.MsgOptionBlocks()); err != nil {
		slog.Warn("update secret prompt", "err", err)
	}
	b.mgr.Submit(incoming{Channel: meta.Channel, ThreadTS: meta.ThreadTS, User: cb.User.ID,
		Text: fmt.Sprintf("[system: %s (id: %s) saved the secret %q to %s. you never see its value; use it from that file and never print it]",
			b.userName(cb.User.ID), cb.User.ID, meta.Name, b.secretPath(meta.Name))})
}

func (b *bot) ephemeral(channel, user, text string) {
	if _, err := b.api.PostEphemeral(channel, user, slack.MsgOptionText(text, false)); err != nil {
		slog.Warn("ephemeral", "err", err)
	}
}
