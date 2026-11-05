package webmaster

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/openai/openai-go/v3"
)

// The summarizer is the only extra model call the board makes. It runs once
// per chunk of old turns, never per mutation, and it writes prose only. The
// page stays the artifact; this is the webmaster's memory of it.
const summarySystemText = `You are the memory of the webmaster of beaverdream, a
small anonymous message board. The webmaster has no text channel: it answers
by rewriting the page. Summarize a stretch of the room so it can continue the
conversation after those turns leave the prompt window.

Write third person, past tense, plain prose. Keep what still matters: what
visitors asked for, what the webmaster granted or refused, positions it took,
promises, grudges, running jokes, what the page looked like and why it
changed, and any server events. Drop small talk and duplicate detail. No
markup, no quotes, no lists, no headings. Under 5000 characters.`

// Summarize folds a chunk of old turns, plus the previous summary, into one
// compact memory. A failed summary is not fatal: the room log still works, and
// the same turns are retried on the next mutation.
func (m *Mutator) Summarize(
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
	b.WriteString("Summarize this stretch of the room so the webmaster does not forget it.\n\n")

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
