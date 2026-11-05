package handlers

import (
	"context"

	"github.com/Napam/beaverdream/db/generated/sqlc"
	"github.com/Napam/beaverdream/lib/webmaster"
)

// JournalState and JournalRulesUsed expose what the prompt and the schema tool
// get about the journal, without running a model.
func (h *Handlers) JournalState(
	ctx context.Context,
	schema string,
) map[string]webmaster.JournalState {
	return h.journalState(ctx, schema)
}

func (h *Handlers) JournalRulesUsed(ctx context.Context) map[string]int {
	return h.journalRulesUsed(ctx)
}

// ExecForTest runs raw SQL on the handler's database, so a test can make a
// write fail with a trigger.
func (h *Handlers) ExecForTest(ctx context.Context, query string) error {
	_, err := h.db.ExecContext(ctx, query)
	return err
}

// QueuePost and RunPending let tests queue a post and drain the queue without
// racing the background worker.
func (h *Handlers) QueuePost(post sqlc.Post) { h.mutateInBackground(post) }

func (h *Handlers) RunPending() { h.runPending() }

// HoldPost queues a post without waking the worker, so it stays pending.
func (h *Handlers) HoldPost(post sqlc.Post) {
	h.liveMu.Lock()
	h.pending = append(h.pending, post)
	h.liveMu.Unlock()
}

// HoldInstruction shows an admin instruction as waiting for a turn, the way a
// real one does while it waits for the mutation lock.
func (h *Handlers) HoldInstruction(text string) { h.addWaiting(text) }
