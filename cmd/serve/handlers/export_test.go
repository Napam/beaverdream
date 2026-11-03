package handlers

import "github.com/Napam/beaverdream/db/generated/sqlc"

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
