package handlers

import (
	"context"
	"errors"
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
// follows the world without polling. The app session cookie authenticates it;
// an anonymous or non-admin request answers 204, which tells Datastar to stop
// retrying a page the browser cannot fix.
func (h *Handlers) AdminLive(w http.ResponseWriter, r *http.Request) {
	h.adminHeaders(w)
	p := principalFrom(r)
	if p == nil || !p.Admin {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	// The page's forms carry this token, so the fragments the stream patches
	// keep working after the page has loaded.
	csrf := p.CSRFToken
	panel := pages.Panel{Base: h.adminBase(), Page: r.URL.Query().Get("page")}
	switch panel.Page {
	case pages.PageOverview, pages.PageSite:
	case pages.PageJournal:
		panel = h.journalPanel(r.URL.Query())
	case pages.PageChangesets:
		panel.Turn, _ = strconv.ParseInt(r.URL.Query().Get("turn"), 10, 64)
		// The page says whether it rendered the turn in flight, so a turn
		// that lands before this stream connects still replaces it.
		panel.Live = r.URL.Query().Get("live") == "1"
	default:
		w.WriteHeader(http.StatusNoContent)
		return
	}

	// Subscribe before the first sync, so a change that lands during it is
	// pushed after it instead of lost.
	sub, cancel := h.hub.subscribeAs(topicAll|topicDraft, p.IdentityID, p.SessionHash)
	defer cancel()
	select {
	case <-sub.closed:
		w.WriteHeader(http.StatusNoContent)
		return
	default:
	}
	if !h.streamSessionValid(r, p) {
		w.WriteHeader(http.StatusNoContent)
		return
	}

	sse := datastar.NewSSE(w, r)
	ctx := sse.Context()
	// wasLive remembers that the last patch showed the running turn live, so
	// a picked turn gets one more patch when it finishes and is stored.
	wasLive := panel.Live
	if err := h.syncAdmin(ctx, sse, panel, csrf, &wasLive, topicAll); err != nil {
		return
	}

	keepalive := time.NewTicker(liveKeepalive)
	defer keepalive.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-sub.closed:
			return
		case <-h.stop:
			return
		case <-keepalive.C:
			if err := sse.Send("keepalive", nil); err != nil {
				return
			}
		case <-sub.wake:
			if err := h.syncAdmin(ctx, sse, panel, csrf, &wasLive, sub.take()); err != nil {
				return
			}
			// Every append publishes, and the journal page scans every key.
			// Waiting here lets a burst coalesce into one patch a second.
			if panel.Page == pages.PageJournal {
				select {
				case <-ctx.Done():
					return
				case <-h.stop:
					return
				case <-time.After(adminJournalEvery):
				}
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
	wasLive *bool,
	t topic,
) error {
	// The status card names what the round is doing, so a draft moves it.
	if t&(topicBusy|topicPosts|topicSite|topicDraft) != 0 {
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
			if err := h.patchAdminMetrics(ctx, sse, panel); err != nil {
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
	case pages.PageChangesets:
		switch {
		case t&(topicSite|topicTurns|topicBusy) != 0:
			if err := h.patchAdminChangesets(
				ctx,
				sse,
				panel.Turn,
				wasLive,
				csrf,
				panel,
			); err != nil {
				return err
			}
		case t&topicDraft != 0:
			// Only the round being written changed. It has its own element,
			// so a draft does not resend the prompt and every finished round.
			run := h.runProgress()
			if run != nil && liveInspector(panel.Turn, run) {
				if err := h.patchAdmin(ctx, sse, "admin-draft", pages.AdminDraft(run)); err != nil {
					return err
				}
			}
		}
	case pages.PageJournal:
		if t&(topicSite|topicState|topicJournal) != 0 {
			if err := h.patchAdminJournal(ctx, sse, panel, csrf); err != nil {
				return err
			}
		}
	case pages.PageSite:
		if t&topicSite != 0 {
			if err := h.patchAdminVersion(ctx, sse, csrf, panel); err != nil {
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
	panel pages.Panel,
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
		pages.AdminVersion(current, csrf, panel),
	); err != nil {
		return err
	}
	return h.patchAdmin(
		ctx,
		sse,
		"admin-versions",
		pages.AdminVersions(versions, current.ID, csrf, panel),
	)
}

// patchAdminMemory brings the compacted history forward.
func (h *Handlers) patchAdminMemory(
	ctx context.Context,
	sse *datastar.ServerSentEventGenerator,
) error {
	summary, through := h.adminSummary(ctx)
	return h.patchAdmin(
		ctx,
		sse,
		"admin-memory",
		pages.AdminMemory(summary, through, h.adminNotes(ctx)),
	)
}

// patchAdminMetrics brings the charts forward.
func (h *Handlers) patchAdminMetrics(
	ctx context.Context,
	sse *datastar.ServerSentEventGenerator,
	panel pages.Panel,
) error {
	turns, _, err := h.adminTurns(ctx, -1, nil)
	if err != nil {
		h.logger.Error("Live admin stream could not list turns", slog.Any("error", err))
		return nil
	}
	return h.patchAdmin(
		ctx,
		sse,
		"admin-metrics",
		pages.AdminMetrics(pages.NewMetrics(turns), panel),
	)
}

// patchAdminChangesets brings the changeset list forward, and the inspector when it
// follows the newest or holds the running turn, plus once more when that
// turn is stored. One read covers both, so a reset cannot leave deleted runs
// on a second admin tab. Any other picked turn stays as it was loaded.
func (h *Handlers) patchAdminChangesets(
	ctx context.Context,
	sse *datastar.ServerSentEventGenerator,
	picked int64,
	wasLive *bool,
	csrf string,
	panel pages.Panel,
) error {
	run := h.runProgress()
	live := liveInspector(picked, run)
	inspect := live || *wasLive
	*wasLive = live
	pick := int64(-1)
	if inspect {
		pick = picked
	}
	turns, latest, err := h.adminTurns(ctx, pick, run)
	if errors.Is(err, errNoTurn) {
		// A reset can delete the held turn between reads. Show the list
		// without it instead of leaving the tab stale.
		inspect = false
		turns, latest, err = h.adminTurns(ctx, -1, run)
	}
	if err != nil {
		h.logger.Error("Live admin stream could not list turns", slog.Any("error", err))
		return nil
	}
	if err := h.patchAdmin(
		ctx,
		sse,
		"admin-history",
		pages.AdminHistory(turns, picked, h.queueView(ctx), panel),
	); err != nil {
		return err
	}
	if !inspect {
		return nil
	}
	var older, newer int64
	if latest != nil {
		older, newer = pages.TurnNeighbours(turns, latest.Row.Run.ID)
	}
	return h.patchAdmin(
		ctx,
		sse,
		"admin-turn",
		pages.AdminTurn(latest, picked == 0, older, newer, run, csrf, panel),
	)
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

// patchAdminJournal brings the journal page forward. The selection comes from
// the panel, so a patch keeps the picked key, match, visitor and page.
func (h *Handlers) patchAdminJournal(
	ctx context.Context,
	sse *datastar.ServerSentEventGenerator,
	panel pages.Panel,
	csrf string,
) error {
	site, err := h.queries.CurrentSite(ctx)
	if err != nil {
		h.logger.Error("Live admin stream could not load the site", slog.Any("error", err))
		return nil
	}
	keys, browse := h.adminJournal(ctx, site, panel)
	return h.patchAdmin(
		ctx,
		sse,
		"admin-journal",
		pages.AdminJournal(
			h.adminStates(ctx, site),
			h.adminHoldings(ctx, site),
			h.adminStateOps(ctx, panel),
			keys, browse, panel, csrf,
		),
	)
}
