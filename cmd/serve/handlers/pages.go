package handlers

import (
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"io"
	"log/slog"
	"net/http"
	"strings"

	"github.com/Napam/beaverdream/db/world/generated/sqlc"
	"github.com/Napam/beaverdream/lib/registry"
	"github.com/Napam/beaverdream/lib/webmaster"
	"github.com/starfederation/datastar-go/datastar"
)

// Index serves the current version as the page. The posts are folded in, and
// the response is written directly: there is no shell and no iframe, so the
// model's markup is the document.
func (h *Handlers) Index(w http.ResponseWriter, r *http.Request) {
	// The page depends on the play session's identity.
	w.Header().Set("Cache-Control", "private, no-store")
	p := principalFrom(r)
	if p == nil {
		http.Error(w, "enter a world with a play session", http.StatusUnauthorized)
		return
	}

	site, err := h.queries.CurrentSite(r.Context())
	if err != nil {
		h.logger.Error("Failed to load current site", slog.Any("error", err))
		http.Error(w, "The world is not here.", http.StatusInternalServerError)
		return
	}
	posts, err := h.recentPosts(r.Context(), livePostBatch)
	if err != nil {
		h.logger.Error("Failed to load posts", slog.Any("error", err))
		http.Error(w, "The world is not here.", http.StatusInternalServerError)
		return
	}

	converted := make([]webmaster.Post, len(posts))
	for i, post := range posts {
		converted[i] = h.postFromRow(r.Context(), post)
	}
	// The page reads newest first. The model still gets the conversation
	// oldest first, which is easier to follow.
	for i, j := 0, len(converted)-1; i < j; i, j = i+1, j-1 {
		converted[i], converted[j] = converted[j], converted[i]
	}

	docSite := h.siteWithShared(r.Context(), site)
	docSite.Name = p.DisplayName
	docSite.WorldID = h.world.ID
	docSite.WorldSlug = h.world.Slug
	docSite.BasePath = h.basePath()
	docSite.AccountURL = h.accountURL(r)
	visitor := p.IdentityID
	docSite.Visitor = publicVisitor(visitor)
	// $mine and $tops are on the page from the start, so markup bound to
	// them renders before the stream connects.
	schema := parseStateSchema(site.StateSchema)
	if mine, err := visitorHoldings(r.Context(), h.queries, schema, visitor); err != nil {
		h.logRead("Could not load visitor holdings", err)
	} else {
		docSite.Mine = mine
	}
	if tops, err := h.topHoldings(r.Context(), schema); err != nil {
		h.logRead("Could not load the top holders", err)
	} else if data, err := json.Marshal(tops); err == nil {
		docSite.Tops = data
	}
	docSite.Datastar = h.cfg.DatastarSrc
	docSite.Three = h.cfg.ThreeSrc
	docSite.ThreeAddons = h.cfg.ThreeAddonsSrc
	// The stream pushes rows after this id. A failed read starts the page at
	// zero, which only means the first push carries rows it already has.
	if cursor, err := h.queries.MaxJournalID(r.Context()); err == nil {
		docSite.JournalCursor = cursor
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if _, err := io.WriteString(w, webmaster.SiteDoc(docSite, converted)); err != nil {
		h.logger.Error("Failed to write index page", slog.Any("error", err))
	}
}

// CreatePost appends the post, then asks the model to redecorate in the
// background. The response does not wait for the model and carries no page:
// every open page, the poster's included, gets the post from its live stream.
// A plain form post is redirected back to the world; a Datastar post gets 204.
func (h *Handlers) CreatePost(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "Bad form", http.StatusBadRequest)
		return
	}

	body := strings.TrimSpace(r.FormValue("body"))
	p := principalFrom(r)
	if p == nil {
		if isDatastar(r) {
			_ = datastar.NewSSE(w, r).Redirect(h.worldPath())
			return
		}
		http.Redirect(w, r, h.worldPath(), http.StatusSeeOther)
		return
	}
	if body == "" {
		h.postDone(w, r)
		return
	}
	body = truncate(body, webmaster.MaxBodyLen)

	// The budget is checked before the insert, so nobody sees a post that can
	// never get an answer. A running turn may overshoot by one, by design.
	if !p.Admin && h.reg != nil {
		if err := h.reg.CheckBudget(
			r.Context(),
			p.IdentityID,
			h.budget,
		); errors.Is(
			err,
			registry.ErrBudget,
		) {
			h.budgetRefused(w, r, body)
			return
		} else if err != nil {
			h.logger.Error("Failed to check the budget", slog.Any("error", err))
			http.Error(w, "Could not post", http.StatusInternalServerError)
			return
		}
	}

	post, err := h.queries.CreatePost(r.Context(), sqlc.CreatePostParams{
		Author: p.IdentityID,
		Body:   body,
	})
	if err != nil {
		h.logger.Error("Failed to store post", slog.Any("error", err))
		http.Error(w, "Could not post", http.StatusInternalServerError)
		return
	}
	h.hub.publish(topicPosts)

	if !h.allowRewrite(p.IdentityID) {
		h.logger.Warn("Post stored without a rewrite", slog.Bool("throttled", true))
		h.postDone(w, r)
		return
	}

	h.queuePost(createdPost(post))
	h.postDone(w, r)
}

// budgetRefused answers an over-budget post. Datastar and JSON callers get a
// 429 with reason budget; a plain form gets the typed text back in a small
// app-owned page, without a world render, so a reload does not lose it.
func (h *Handlers) budgetRefused(w http.ResponseWriter, r *http.Request, body string) {
	if isDatastar(r) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusTooManyRequests)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"ok": false, "reason": "budget", "error": "no tokens left today",
		})
		return
	}
	if wantsHTML(r) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		w.WriteHeader(http.StatusTooManyRequests)
		action := h.basePath() + "/posts"
		_, _ = fmt.Fprintf(w, `<!doctype html><meta charset="utf-8"><title>out of tokens</title>
<main style="max-width:48rem;margin:2rem auto;font:16px system-ui;padding:0 1rem">
<p>No tokens are left for today. Your message was not posted.</p>
<form method="post" action="%s"><textarea name="body" rows="4" style="width:100%%">%s</textarea>
<p><button type="submit">try again</button></p></form>
<p><a href="%s/">back to the world</a></p></main>`,
			html.EscapeString(action), html.EscapeString(body),
			html.EscapeString(h.basePath()))
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusTooManyRequests)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"ok": false, "reason": "budget", "error": "no tokens left today",
	})
}

// postDone answers a post. The page shows newest first, so a plain form
// lands on the new post at the top.
func (h *Handlers) postDone(w http.ResponseWriter, r *http.Request) {
	if isDatastar(r) {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	http.Redirect(w, r, h.worldPath(), http.StatusSeeOther)
}

// isDatastar reports a request sent by a Datastar action. Its reply must not
// be a redirect to the world: Datastar would follow it and patch the whole
// page into the document.
func isDatastar(r *http.Request) bool {
	return r.Header.Get("Datastar-Request") == "true"
}

// Version reports the current version, the newest post id and whether a
// rewrite is queued or running. Pages do not poll it; their live stream
// pushes changes. It is a plain read for tools and tests.
func (h *Handlers) Version(w http.ResponseWriter, r *http.Request) {
	raw, err := h.queries.CurrentVersionID(r.Context())
	version, ok := raw.(int64)
	if err != nil || !ok {
		http.Error(w, "unknown", http.StatusInternalServerError)
		return
	}
	post, err := h.queries.LatestPostID(r.Context())
	if err != nil {
		h.logger.Error("Failed to load latest post id", slog.Any("error", err))
		http.Error(w, "unknown", http.StatusInternalServerError)
		return
	}
	_, pending, running, _ := h.liveSnapshot()

	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	_ = json.NewEncoder(w).Encode(struct {
		Version int64     `json:"version"`
		Post    int64     `json:"post"`
		Pending int       `json:"pending"`
		Running *runState `json:"running,omitempty"`
	}{
		Version: version,
		Post:    post,
		Pending: pending,
		Running: running,
	})
}

// swapKind reports whether the only change is one an open page can apply
// without a navigation. A stylesheet that breaks out of its style tag also forces a
// reload, because in-place text cannot remove the elements it created.
func swapKind(previous, current sqlc.SiteVersion) string {
	if previous.Html != current.Html ||
		previous.StateSchema != current.StateSchema ||
		normalizeSchema(previous.JournalSchema) != normalizeSchema(current.JournalSchema) {
		return "reload"
	}
	if breaksOutOfStyle(previous.Css) || breaksOutOfStyle(current.Css) {
		return "reload"
	}
	return "css"
}

func breaksOutOfStyle(css string) bool {
	lower := strings.ToLower(css)
	return strings.Contains(lower, "</style") || strings.Contains(lower, "<script")
}
