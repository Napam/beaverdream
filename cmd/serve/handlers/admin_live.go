package handlers

import (
	"context"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/Napam/beaverdream/web/pages"
	"github.com/a-h/templ"
	"github.com/starfederation/datastar-go/datastar"
)

// AdminLive is an admin page's one stream. The page names itself in the query,
// and the stream patches the fragments that page rendered, so the panel
// follows the board without polling. The token rides in the X-Admin-Token
// remembered login cookie or the token header; a request without either answers
// 204, which tells Datastar to stop retrying a page the browser cannot fix.
func (h *Handlers) AdminLive(w http.ResponseWriter, r *http.Request) {
	h.adminHeaders(w)
	if !h.adminAuthed(r) {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	// The page's forms carry this token, so the fragments the stream patches
	// keep working after the page has loaded.
	csrf := h.csrfFor(r)
	panel := pages.Panel{Page: r.URL.Query().Get("page")}
	switch panel.Page {
	case pages.PageOverview, pages.PageSite:
	case pages.PageTurns:
		panel.Turn, _ = strconv.ParseInt(r.URL.Query().Get("turn"), 10, 64)
	default:
		w.WriteHeader(http.StatusNoContent)
		return
	}

	// Subscribe before the first sync, so a change that lands during it is
	// pushed after it instead of lost.
	sub, cancel := h.hub.subscribe()
	defer cancel()

	sse := datastar.NewSSE(w, r)
	ctx := sse.Context()
	if err := h.syncAdmin(ctx, sse, panel, csrf, topicAll); err != nil {
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
			if err := h.syncAdmin(ctx, sse, panel, csrf, sub.take()); err != nil {
				return
			}
		}
	}
}

// syncAdmin patches the panels of one page that a topic can change. Every
// page carries the status card. A failed read is logged and skipped: the next
// change retries it. Only a failed write ends the stream.
func (h *Handlers) syncAdmin(
	ctx context.Context,
	sse *datastar.ServerSentEventGenerator,
	panel pages.Panel,
	csrf string,
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
	switch panel.Page {
	case pages.PageOverview:
		if t&(topicSite|topicTurns) != 0 {
			if err := h.patchAdminMetrics(ctx, sse); err != nil {
				return err
			}
			if err := h.patchAdminMemory(ctx, sse); err != nil {
				return err
			}
		}
		// The budget counts the log and the page, and the page carries the
		// shared state and the newest journal rows.
		if t&(topicPosts|topicSite|topicTurns|topicState|topicJournal) != 0 {
			if err := h.patchAdminBudget(ctx, sse); err != nil {
				return err
			}
		}
	case pages.PageTurns:
		if t&(topicSite|topicTurns) != 0 {
			if err := h.patchAdminTurns(ctx, sse, panel.Turn); err != nil {
				return err
			}
		}
	case pages.PageSite:
		if t&topicSite != 0 {
			if err := h.patchAdminVersion(ctx, sse, csrf); err != nil {
				return err
			}
		}
		if t&(topicSite|topicState|topicJournal) != 0 {
			if err := h.patchAdminFeeds(ctx, sse); err != nil {
				return err
			}
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
	csrf string,
) error {
	versions, err := h.queries.ListSiteVersions(ctx, adminVersions)
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
		pages.AdminVersion(current, csrf),
	); err != nil {
		return err
	}
	return h.patchAdmin(
		ctx,
		sse,
		"admin-versions",
		pages.AdminVersions(versions, current.ID, csrf),
	)
}

// patchAdminMemory brings the compacted history forward.
func (h *Handlers) patchAdminMemory(
	ctx context.Context,
	sse *datastar.ServerSentEventGenerator,
) error {
	summary, through := h.adminSummary(ctx)
	return h.patchAdmin(ctx, sse, "admin-memory", pages.AdminMemory(summary, through))
}

// patchAdminMetrics brings the charts forward.
func (h *Handlers) patchAdminMetrics(
	ctx context.Context,
	sse *datastar.ServerSentEventGenerator,
) error {
	turns, _, err := h.adminTurns(ctx, -1)
	if err != nil {
		h.logger.Error("Live admin stream could not list turns", slog.Any("error", err))
		return nil
	}
	return h.patchAdmin(ctx, sse, "admin-metrics", pages.AdminMetrics(pages.NewMetrics(turns)))
}

// patchAdminTurns brings the turn list forward, and the inspector when it
// follows the newest. One read covers both, so a reset cannot leave deleted
// runs on a second admin tab. A picked turn stays as it was loaded.
func (h *Handlers) patchAdminTurns(
	ctx context.Context,
	sse *datastar.ServerSentEventGenerator,
	picked int64,
) error {
	pick := int64(0)
	if picked > 0 {
		pick = -1
	}
	turns, latest, err := h.adminTurns(ctx, pick)
	if err != nil {
		h.logger.Error("Live admin stream could not list turns", slog.Any("error", err))
		return nil
	}
	if err := h.patchAdmin(
		ctx,
		sse,
		"admin-history",
		pages.AdminHistory(turns, picked),
	); err != nil {
		return err
	}
	if picked > 0 {
		return nil
	}
	// The newest turn's only neighbour is the one before it.
	var older int64
	if len(turns) > 1 {
		older = turns[1].Run.ID
	}
	return h.patchAdmin(ctx, sse, "admin-turn", pages.AdminTurn(latest, true, older, 0))
}

// patchAdminBudget brings the compaction budget forward.
func (h *Handlers) patchAdminBudget(
	ctx context.Context,
	sse *datastar.ServerSentEventGenerator,
) error {
	site, err := h.queries.CurrentSite(ctx)
	if err != nil {
		h.logger.Error("Live admin stream could not load the site", slog.Any("error", err))
		return nil
	}
	return h.patchAdmin(ctx, sse, "admin-budget", pages.AdminBudget(h.adminBudget(ctx, site)))
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
