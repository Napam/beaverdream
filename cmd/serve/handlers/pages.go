package handlers

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"strings"

	"github.com/Napam/beaverdream/db/generated/sqlc"
	"github.com/Napam/beaverdream/lib/webmaster"
	"github.com/starfederation/datastar-go/datastar"
)

// Index serves the current version as the page. The posts are folded in, and
// the response is written directly: there is no shell and no iframe, so the
// model's markup is the document.
func (h *Handlers) Index(w http.ResponseWriter, r *http.Request) {
	// The page depends on the visitor's cookie.
	w.Header().Set("Cache-Control", "private, no-store")
	if identityFromRequest(r) == "" {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		if _, err := io.WriteString(w, webmaster.IdentityDoc("")); err != nil {
			h.logger.Error("Failed to write identity page", slog.Any("error", err))
		}
		return
	}

	site, err := h.queries.CurrentSite(r.Context())
	if err != nil {
		h.logger.Error("Failed to load current site", slog.Any("error", err))
		http.Error(w, "The board is not here.", http.StatusInternalServerError)
		return
	}
	posts, err := h.recentPosts(r.Context(), livePostBatch)
	if err != nil {
		h.logger.Error("Failed to load posts", slog.Any("error", err))
		http.Error(w, "The board is not here.", http.StatusInternalServerError)
		return
	}

	converted := make([]webmaster.Post, len(posts))
	for i, p := range posts {
		converted[i] = postFromRow(p)
	}
	// The board reads newest first. The model still gets the conversation
	// oldest first, which is easier to follow.
	for i, j := 0, len(converted)-1; i < j; i, j = i+1, j-1 {
		converted[i], converted[j] = converted[j], converted[i]
	}

	docSite := h.siteWithState(r.Context(), site)
	docSite.Name = identityFromRequest(r)
	docSite.Visitor = publicVisitor(visitorFromRequest(w, r))
	docSite.Datastar = h.cfg.DatastarSrc
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
// A plain form post is redirected back to the board; a Datastar post gets 204.
func (h *Handlers) CreatePost(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "Bad form", http.StatusBadRequest)
		return
	}

	body := strings.TrimSpace(r.FormValue("body"))
	author := identityFromRequest(r)
	if author == "" {
		if isDatastar(r) {
			_ = datastar.NewSSE(w, r).Redirect("/")
			return
		}
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}
	if body == "" {
		h.postDone(w, r)
		return
	}
	rewrite := h.allowRewrite(author)
	body = truncate(body, maxBodyLen)

	post, err := h.queries.CreatePost(r.Context(), sqlc.CreatePostParams{
		Author: author,
		Body:   body,
	})
	if err != nil {
		h.logger.Error("Failed to store post", slog.Any("error", err))
		http.Error(w, "Could not post", http.StatusInternalServerError)
		return
	}
	h.hub.publish(topicPosts)

	capped := h.overDailyCap(r.Context())
	if !rewrite || capped {
		h.logger.Warn("Post stored without a rewrite",
			slog.Bool("throttled", !rewrite),
			slog.Bool("capped", capped),
		)
		h.postDone(w, r)
		return
	}

	h.mutateInBackground(createdPost(post))
	h.postDone(w, r)
}

// postDone answers a post. The board shows newest first, so a plain form
// lands on the new post at the top.
func (h *Handlers) postDone(w http.ResponseWriter, r *http.Request) {
	if isDatastar(r) {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

// isDatastar reports a request sent by a Datastar action. Its reply must not
// be a redirect to the board: Datastar would follow it and patch the whole
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
	if previous.Header != current.Header ||
		previous.Board != current.Board ||
		previous.Footer != current.Footer ||
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
