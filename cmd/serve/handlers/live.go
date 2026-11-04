package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/Napam/beaverdream/db/generated/sqlc"
	"github.com/Napam/beaverdream/lib/webmaster"
	"github.com/starfederation/datastar-go/datastar"
)

const (
	// liveKeepalive keeps an idle stream open behind proxies that close
	// quiet connections. Datastar ignores events it does not know.
	liveKeepalive = 20 * time.Second
	// livePostBatch is how many posts one push may prepend. A page further
	// behind than that gets the board again, the same 200 the page shows.
	livePostBatch = 200
	// liveJournalBatch is how many rows one push may carry. A page further
	// behind reads its feeds again instead.
	liveJournalBatch = 500
)

// liveView is one stream's projection: the cursor the page holds, plus the
// parts the page does not echo back.
type liveView struct {
	cursor webmaster.LiveCursor
	busy   bool
	state  string
}

// Live is the read side of the board: one Datastar stream per open page.
// Commands never answer with page content. They write, then publish a topic;
// every stream reads the database again for that topic and pushes only what
// its page lacks. GET never calls the model.
func (h *Handlers) Live(w http.ResponseWriter, r *http.Request) {
	// 204 is Datastar's signal to stop retrying.
	if identityFromRequest(r) == "" {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	var signals struct {
		BD webmaster.LiveCursor `json:"_bd"`
	}
	if err := datastar.ReadSignals(r, &signals); err != nil {
		h.logger.Debug("Live stream sent unreadable signals", slog.Any("error", err))
	}

	// Subscribe before the first sync, so a change that lands during it is
	// pushed after it instead of lost.
	sub, cancel := h.hub.subscribe(topicAll)
	defer cancel()

	sse := datastar.NewSSE(w, r)
	ctx := sse.Context()
	view := liveView{cursor: signals.BD}
	if err := h.syncLive(ctx, sse, &view, topicAll); err != nil {
		return
	}

	keepalive := time.NewTicker(liveKeepalive)
	defer keepalive.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-h.stop:
			return
		case <-keepalive.C:
			if err := sse.Send("keepalive", nil); err != nil {
				return
			}
		case <-sub.wake:
			if err := h.syncLive(ctx, sse, &view, sub.take()); err != nil {
				return
			}
		}
	}
}

// syncLive brings one page up to date on the topics that changed. A failed
// read is logged and skipped, so the next change to that part retries it; only
// a failed write ends the stream, and the page reconnects with its cursor.
func (h *Handlers) syncLive(
	ctx context.Context,
	sse *datastar.ServerSentEventGenerator,
	view *liveView,
	t topic,
) error {
	before := view.cursor
	// The site goes first: a markup change reloads the page, and nothing
	// pushed after it matters.
	steps := []struct {
		topic topic
		sync  func(context.Context, *datastar.ServerSentEventGenerator, *liveView) error
	}{
		{topicSite, h.syncSite},
		{topicPosts, h.syncPosts},
		{topicState, h.syncState},
		{topicJournal, h.syncJournal},
		{topicBusy, h.syncBusy},
	}
	for _, step := range steps {
		if t&step.topic == 0 {
			continue
		}
		if err := step.sync(ctx, sse, view); err != nil {
			return err
		}
	}
	if view.cursor != before {
		return sse.MarshalAndPatchSignals(map[string]webmaster.LiveCursor{"_bd": view.cursor})
	}
	return nil
}

// syncSite applies a new version. A stylesheet-only change is patched in
// place; anything else asks the live script to reload, which waits for the
// visitor to stop typing.
func (h *Handlers) syncSite(
	ctx context.Context,
	sse *datastar.ServerSentEventGenerator,
	view *liveView,
) error {
	current, err := h.queries.CurrentSite(ctx)
	if err != nil {
		h.logRead("Live stream could not load the site", err)
		return nil
	}
	if current.ID == view.cursor.Version {
		return nil
	}
	// A page that sent no cursor adopts the current version. Reloading it
	// would come back without a cursor and reload forever.
	if view.cursor.Version == 0 {
		view.cursor.Version = current.ID
		return nil
	}

	swap := "reload"
	if previous, err := h.queries.GetSiteVersion(ctx, view.cursor.Version); err == nil {
		swap = swapKind(previous, current)
	}
	if swap == "css" {
		// The page takes the new stylesheet immediately, so it can move its
		// cursor. A reload is deferred while the visitor types and becomes
		// real only on the next navigation; leaving the cursor old makes a
		// reconnect ask for the reload again if this stream dies first.
		view.cursor.Version = current.ID
		if err := sse.PatchElements(
			`<style id="bd-css">` + current.Css + `</style>`,
		); err != nil {
			return err
		}
		return sse.ExecuteScript(fmt.Sprintf("window.bdLive && bdLive.css(%d)", current.ID))
	}
	return sse.ExecuteScript(fmt.Sprintf(
		"window.bdLive ? bdLive.reload(%d) : location.reload()",
		current.ID,
	))
}

// syncPosts puts new posts at the top of the board. A page that is far
// behind, or holds posts a reset deleted, gets the whole board again.
func (h *Handlers) syncPosts(
	ctx context.Context,
	sse *datastar.ServerSentEventGenerator,
	view *liveView,
) error {
	latest, err := h.queries.LatestPostID(ctx)
	if err != nil {
		h.logRead("Live stream could not load the newest post", err)
		return nil
	}
	if latest == view.cursor.Post {
		return nil
	}

	var rows []sqlc.Post
	mode := datastar.WithModeInner()
	if view.cursor.Post > 0 && latest > view.cursor.Post {
		rows, err = h.queries.ListPostsAfter(ctx, sqlc.ListPostsAfterParams{
			ID:    view.cursor.Post,
			Limit: livePostBatch,
		})
		if err != nil {
			h.logRead("Live stream could not load new posts", err)
			return nil
		}
		mode = datastar.WithModePrepend()
	}
	if len(rows) == 0 || len(rows) == livePostBatch {
		rows, err = h.queries.ListRecentPosts(ctx, livePostBatch)
		if err != nil {
			h.logRead("Live stream could not load posts", err)
			return nil
		}
		mode = datastar.WithModeInner()
	}

	// Both queries return newest first, the order the board reads.
	posts := make([]webmaster.Post, len(rows))
	for i, row := range rows {
		posts[i] = postFromRow(row)
	}
	// A post can land between the id read above and the rows read, so the
	// cursor follows what the page was handed, not the older id. Otherwise
	// the next sync would prepend that post a second time.
	newest := latest
	if len(rows) > 0 {
		newest = rows[0].ID
	}
	view.cursor.Post = newest
	if err := sse.PatchElements(
		webmaster.RenderPosts(posts),
		datastar.WithSelectorID("bd-posts"),
		mode,
	); err != nil {
		return err
	}
	return sse.ExecuteScript(fmt.Sprintf("window.bdLive && bdLive.posts(%d)", newest))
}

// syncState pushes the shared values as the $shared signal, which model
// markup binds to, and hands them to page scripts.
func (h *Handlers) syncState(
	ctx context.Context,
	sse *datastar.ServerSentEventGenerator,
	view *liveView,
) error {
	site, err := h.queries.CurrentSite(ctx)
	if err != nil {
		h.logRead("Live stream could not load the site", err)
		return nil
	}
	rows, err := h.queries.ListSiteState(ctx)
	if err != nil {
		h.logRead("Live stream could not load shared state", err)
		return nil
	}
	values, err := json.Marshal(stateValues(parseStateSchema(site.StateSchema), rows))
	if err != nil {
		h.logger.Error("Live stream could not encode shared state", slog.Any("error", err))
		return nil
	}
	if string(values) == view.state {
		return nil
	}
	view.state = string(values)
	if err := sse.PatchSignals(
		[]byte(`{"shared":` + string(values) + `}`),
	); err != nil {
		return err
	}
	return sse.ExecuteScript("window.bdLive && bdLive.state(" + string(values) + ")")
}

// syncJournal pushes the rows written since the page's journal cursor. The
// runtime folds them into the feeds the page started.
func (h *Handlers) syncJournal(
	ctx context.Context,
	sse *datastar.ServerSentEventGenerator,
	view *liveView,
) error {
	newest, err := h.queries.MaxJournalID(ctx)
	if err != nil {
		h.logRead("Live stream could not read the journal", err)
		return nil
	}
	if newest == view.cursor.Journal {
		return nil
	}
	// A reset rewinds the ids. The page's rows are gone, so it reads again.
	if newest < view.cursor.Journal {
		view.cursor.Journal = newest
		return sse.ExecuteScript("window.bd && bd._resync()")
	}

	rows, err := h.queries.ListJournalAfter(ctx, sqlc.ListJournalAfterParams{
		ID:    view.cursor.Journal,
		Limit: liveJournalBatch,
	})
	if err != nil {
		h.logRead("Live stream could not read journal rows", err)
		return nil
	}
	if len(rows) == liveJournalBatch {
		view.cursor.Journal = newest
		return sse.ExecuteScript("window.bd && bd._resync()")
	}
	out := make([]journalRow, len(rows))
	for i, row := range rows {
		out[i] = journalRowFrom(row)
		view.cursor.Journal = row.ID
	}
	// json.Marshal escapes <, > and &, so a payload cannot close the script.
	data, err := json.Marshal(out)
	if err != nil {
		h.logger.Error("Live stream could not encode journal rows", slog.Any("error", err))
		return nil
	}
	return sse.ExecuteScript("window.bd && bd._rows(" + string(data) + ")")
}

// syncBusy shows the spinner while a rewrite is queued or running.
func (h *Handlers) syncBusy(
	_ context.Context,
	sse *datastar.ServerSentEventGenerator,
	view *liveView,
) error {
	_, pending, running, _ := h.liveSnapshot()
	busy := running != nil || pending > 0
	if busy == view.busy {
		return nil
	}
	view.busy = busy
	return sse.PatchElements(webmaster.BusyIndicator(busy))
}
