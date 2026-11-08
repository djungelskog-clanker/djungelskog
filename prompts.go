package main

import (
	"math/rand/v2"

	"github.com/slack-go/slack"
)

// suggestedPromptPool feeds the assistant panel's starter buttons; a random
// few show up each time, so the panel doesn't look the same forever.
var suggestedPromptPool = []slack.AssistantThreadsPrompt{
	{Title: "what's new in my channels", Message: "catch me up on what happened in my channels today"},
	{Title: "make something silly", Message: "make me something silly"},
	{Title: "remind me", Message: "remind me in an hour to drink water"},
	{Title: "say something out loud", Message: "say something nice to me out loud"},
	{Title: "draw me a picture", Message: "draw me a little picture of a bear in a flatpack box"},
	{Title: "what can you do?", Message: "what can you do? show me your tools and reaction tricks"},
	{Title: "how much have i cost alice", Message: "how much have i cost alice this week?"},
	{Title: "run a poll", Message: "make a poll: what should djungelskog learn next?"},
	{Title: "explain something", Message: "explain something interesting i probably don't know, in 5 lines"},
	{Title: "what changed in you lately", Message: "what changed in your code recently?"},
}

func suggestedPrompts(n int) []slack.AssistantThreadsPrompt {
	p := append([]slack.AssistantThreadsPrompt(nil), suggestedPromptPool...)
	rand.Shuffle(len(p), func(i, j int) { p[i], p[j] = p[j], p[i] })
	return p[:min(n, len(p))]
}
