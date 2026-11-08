package webmaster

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/openai/openai-go/v3"
)

// The summarizer is the only extra model call the server makes. It runs once
// per chunk of old turns, never per changeset, and it writes prose only. The
// page stays the artifact; this is Naps's memory of it.
var summarySystemText = fmt.Sprintf(`You are the memory of Naps, the webmaster of beaverdream, a
world builder that starts as a small plain page with a post form. Naps answers
in two ways: by rewriting the page, and by replies it posts in the room.
Merge the previous summary and the new turns into one replacement memory.
Keep still-relevant facts from the previous summary; it will be discarded.
In the room log, "you" and "you replied" mean Naps.

Write third person, past tense, plain prose. Keep what still matters: what
visitors asked for, the questions they asked and the gist of what Naps
answered, what it granted or refused, positions it took,
promises, grudges, running jokes, what changed on the page and why,
and any server events. Drop small talk and duplicate detail. No
markup, no quotes, no lists, no headings. Under %d characters.`, summaryMaxLen)

// Summarize folds a chunk of old turns, plus the previous summary, into one
// compact memory. A failed summary is not fatal: the room log still works, and
// the same turns are retried on the next changeset.
func (m *ChangesetRunner) Summarize(
	ctx context.Context,
	previous string,
	turns []Turn,
) (string, error) {
	resp, err := m.client.Chat.Completions.New(ctx, openai.ChatCompletionNewParams{
		Model: openai.ChatModel(m.model),
		Messages: []openai.ChatCompletionMessageParamUnion{
			openai.SystemMessage(summarySystemText),
			openai.UserMessage(summaryPrompt(previous, turns)),
		},
		MaxTokens:   openai.Int(summaryMaxTokens),
		Temperature: openai.Float(0.3),
	})
	if err != nil {
		return "", fmt.Errorf("summary completion: %w", err)
	}
	if len(resp.Choices) == 0 {
		return "", errors.New("summary returned no choices")
	}
	text := strings.TrimSpace(resp.Choices[0].Message.Content)
	if text == "" {
		return "", errors.New("summary was empty")
	}
	return truncateRunes(text, summaryMaxLen), nil
}

func summaryPrompt(previous string, turns []Turn) string {
	var b strings.Builder
	b.WriteString("PREVIOUS SUMMARY:\n")
	if notes := strings.TrimSpace(previous); notes != "" {
		b.WriteString(notes)
		b.WriteString("\n")
	} else {
		b.WriteString("(none yet)\n")
	}

	b.WriteString("\nNEW TURNS:\n")
	if log := RenderLog(turns, nil); log != "" {
		b.WriteString(log)
	} else {
		b.WriteString("(nothing new)\n")
	}
	return b.String()
}
