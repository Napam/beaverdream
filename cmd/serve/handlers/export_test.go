package handlers

import "github.com/Napam/beaverdream/db/generated/sqlc"

// QueuePost and RunPending let tests queue a post and drain the queue without
// racing the background worker.
func (h *Handlers) QueuePost(post sqlc.Post) { h.mutateInBackground(post) }

func (h *Handlers) RunPending() { h.runPending() }
