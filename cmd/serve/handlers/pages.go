package handlers

import (
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"

	"github.com/Napam/beaverdream/db/generated/sqlc"
	"github.com/Napam/beaverdream/lib/webmaster"
)

// Index serves the current version as the page. The posts are folded in, and
// the response is written directly: there is no shell and no iframe, so the
// model's markup is the document.
func (h *Handlers) Index(w http.ResponseWriter, r *http.Request) {
	site, err := h.queries.CurrentSite(r.Context())
	if err != nil {
		h.logger.Error("Failed to load current site", slog.Any("error", err))
		http.Error(w, "The board is not here.", http.StatusInternalServerError)
		return
	}
	posts, err := h.recentPosts(r.Context(), 200)
	if err != nil {
		h.logger.Error("Failed to load posts", slog.Any("error", err))
		http.Error(w, "The board is not here.", http.StatusInternalServerError)
		return
	}

	converted := make([]webmaster.Post, len(posts))
	for i, p := range posts {
		converted[i] = postFromRow(p)
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if _, err := io.WriteString(w, webmaster.SiteDoc(siteFromRow(site), converted)); err != nil {
		h.logger.Error("Failed to write index page", slog.Any("error", err))
	}
}

// CreatePost appends the post, then asks the model to redecorate in the
// background. The response does not wait for the model: the post shows up on
// the next load, and open pages pick up the new version when it lands.
func (h *Handlers) CreatePost(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "Bad form", http.StatusBadRequest)
		return
	}

	body := strings.TrimSpace(r.FormValue("body"))
	author := truncate(strings.TrimSpace(r.FormValue("author")), maxAuthorLen)
	if body == "" {
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}
	body = truncate(body, maxBodyLen)
	if author == "" {
		author = "anon"
	}

	post, err := h.queries.CreatePost(r.Context(), sqlc.CreatePostParams{
		Author: author,
		Body:   body,
	})
	if err != nil {
		h.logger.Error("Failed to store post", slog.Any("error", err))
		http.Error(w, "Could not post", http.StatusInternalServerError)
		return
	}

	// Land on the new post instead of the top of the page. The post is stored
	// already, so the anchor exists even before the rewrite lands.
	location := fmt.Sprintf("/#p%d", post.ID)

	if h.overDailyCap(r.Context()) {
		h.logger.Warn("Daily mutation cap reached, post stored without a rewrite")
		http.Redirect(w, r, location, http.StatusSeeOther)
		return
	}

	h.mutateInBackground(post)
	http.Redirect(w, r, location, http.StatusSeeOther)
}

// Version is a tiny endpoint for pages that want to notice a change while
// they are open.
func (h *Handlers) Version(w http.ResponseWriter, r *http.Request) {
	site, err := h.queries.CurrentSite(r.Context())
	if err != nil {
		http.Error(w, "unknown", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]int64{"version": site.ID})
}
