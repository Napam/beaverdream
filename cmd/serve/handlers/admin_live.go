package handlers

import (
	"context"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/Napam/beaverdream/web/pages"
	"github.com/a-h/templ"
	"github.com/starfederation/datastar-go/datastar"
)

// AdminLive is the admin page's one stream. It patches the same fragments the
// page rendered, so the panel follows the board without polling. The token
// rides in the X-Admin-Token request header; a bad one answers 204, which
// tells Datastar to stop retrying a page the browser cannot fix.
func (h *Handlers) AdminLive(w http.ResponseWriter, r *http.Request) {
	h.adminHeaders(w)
	if !h.adminTokenOK(r) {
		w.WriteHeader(http.StatusNoContent)
		return
	}

	// Subscribe before the first sync, so a change that lands during it is
	// pushed after it instead of lost.
	sub, cancel := h.hub.subscribe()
	defer cancel()

	sse := datastar.NewSSE(w, r)
	ctx := sse.Context()
	if err := h.syncAdmin(ctx, sse, topicAll); err != nil {
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
			if err := h.syncAdmin(ctx, sse, sub.take()); err != nil {
				return
			}
		}
	}
}

// syncAdmin patches the panels a topic can change. A failed read is logged and
// skipped: the next change retries it. Only a failed write ends the stream.
func (h *Handlers) syncAdmin(
	ctx context.Context,
	sse *datastar.ServerSentEventGenerator,
	t topic,
) error {
	if t&(topicBusy|topicPosts|topicSite) != 0 {
		if err := h.patchAdmin(
			ctx,
			sse,
			"admin-live",
			pages.AdminLiveCard(h.adminLiveView(ctx)),
		); err != nil {
			return err
		}
	}
	if t&topicSite != 0 {
		if err := h.patchAdminVersion(ctx, sse); err != nil {
			return err
		}
		if err := h.patchAdminMemory(ctx, sse); err != nil {
			return err
		}
		if err := h.patchAdminHistory(ctx, sse); err != nil {
			return err
		}
	}
	if t&(topicSite|topicState|topicJournal) != 0 {
		if err := h.patchAdminFeeds(ctx, sse); err != nil {
			return err
		}
	}
	return nil
}

// patchAdmin replaces one element with a rendered fragment. A render error is
// a bug on this side, so the stream keeps going and the next change tries
// again.
func (h *Handlers) patchAdmin(
	ctx context.Context,
	sse *datastar.ServerSentEventGenerator,
	id string,
	component templ.Component,
) error {
	var b strings.Builder
	if err := component.Render(ctx, &b); err != nil {
		h.logger.Error("Failed to render an admin fragment",
			slog.String("id", id),
			slog.Any("error", err),
		)
		return nil
	}
	return sse.PatchElements(
		b.String(),
		datastar.WithSelectorID(id),
		datastar.WithModeOuter(),
	)
}

// patchAdminVersion brings the current render and the versions table forward.
// One read names the current version, so the table cannot mark an older row
// current when a version lands between two queries.
func (h *Handlers) patchAdminVersion(
	ctx context.Context,
	sse *datastar.ServerSentEventGenerator,
) error {
	versions, err := h.queries.ListSiteVersions(ctx, 12)
	if err != nil {
		h.logger.Error("Live admin stream could not list versions", slog.Any("error", err))
		return nil
	}
	if len(versions) == 0 {
		h.logger.Error("Live admin stream found no version")
		return nil
	}
	current := versions[0]
	if err := h.patchAdmin(
		ctx,
		sse,
		"admin-version",
		pages.AdminVersion(current, h.cfg.AdminToken),
	); err != nil {
		return err
	}
	return h.patchAdmin(
		ctx,
		sse,
		"admin-versions",
		pages.AdminVersions(versions, current.ID, h.cfg.AdminToken),
	)
}

// patchAdminMemory brings the compacted history forward. A summary written
// after the turn landed appears on the next site change.
func (h *Handlers) patchAdminMemory(
	ctx context.Context,
	sse *datastar.ServerSentEventGenerator,
) error {
	summary, through := h.adminSummary(ctx)
	return h.patchAdmin(ctx, sse, "admin-memory", pages.AdminMemory(summary, through))
}

// patchAdminHistory brings the newest turns forward, so a reset cannot leave
// deleted runs on a second admin tab.
func (h *Handlers) patchAdminHistory(
	ctx context.Context,
	sse *datastar.ServerSentEventGenerator,
) error {
	runs, err := h.adminRuns(ctx)
	if err != nil {
		h.logger.Error("Live admin stream could not list turns", slog.Any("error", err))
		return nil
	}
	return h.patchAdmin(ctx, sse, "admin-history", pages.AdminHistory(runs))
}

// patchAdminFeeds brings the journal and shared state forward.
func (h *Handlers) patchAdminFeeds(
	ctx context.Context,
	sse *datastar.ServerSentEventGenerator,
) error {
	site, err := h.queries.CurrentSite(ctx)
	if err != nil {
		h.logger.Error("Live admin stream could not load the site", slog.Any("error", err))
		return nil
	}
	feeds, states := h.adminFeeds(ctx, site)
	return h.patchAdmin(ctx, sse, "admin-feeds", pages.AdminFeeds(feeds, states))
}
