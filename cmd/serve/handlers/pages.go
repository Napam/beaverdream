package handlers

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"

	"github.com/Napam/beaverdream/db/generated/sqlc"
	"github.com/Napam/beaverdream/lib/webmaster"
	"github.com/go-chi/chi/v5"
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
	// The board reads newest first. The model still gets the conversation
	// oldest first, which is easier to follow.
	for i, j := 0, len(converted)-1; i < j; i, j = i+1, j-1 {
		converted[i], converted[j] = converted[j], converted[i]
	}

	docSite := h.siteWithState(r.Context(), site)
	docSite.Name = identityFromRequest(r)

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if _, err := io.WriteString(w, webmaster.SiteDoc(docSite, converted)); err != nil {
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
	author := identityFromRequest(r)
	if author == "" {
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}
	if body == "" {
		http.Redirect(w, r, "/", http.StatusSeeOther)
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
	h.touchActivity()

	// The board shows newest first, so the new post is already at the top.
	location := "/"

	capped := h.overDailyCap(r.Context())
	if !rewrite || capped {
		h.logger.Warn("Post stored without a rewrite",
			slog.Bool("throttled", !rewrite),
			slog.Bool("capped", capped),
		)
		http.Redirect(w, r, location, http.StatusSeeOther)
		return
	}

	h.mutateInBackground(createdPost(post))
	http.Redirect(w, r, location, http.StatusSeeOther)
}

// Version is a tiny endpoint for pages that want to notice a change while
// they are open. With ?have=N it also says whether the page can take the new
// stylesheet in place or needs to reload.
func (h *Handlers) Version(w http.ResponseWriter, r *http.Request) {
	version, pending, running, _ := h.liveSnapshot()
	if raw, err := h.queries.CurrentVersionID(r.Context()); err == nil {
		if id, ok := raw.(int64); ok && id > 0 && id != version {
			version = id
			h.setVersion(id)
		}
	}
	if version == 0 {
		var err error
		version, err = h.currentVersion(r.Context())
		if err != nil {
			http.Error(w, "unknown", http.StatusInternalServerError)
			return
		}
	}

	resp := struct {
		Version int64     `json:"version"`
		Swap    string    `json:"swap,omitempty"`
		Pending int       `json:"pending"`
		Running *runState `json:"running,omitempty"`
	}{
		Version: version,
		Pending: pending,
		Running: running,
	}

	have := r.URL.Query().Get("have")
	if have != "" && have != strconv.FormatInt(version, 10) {
		resp.Swap = "reload"
		current, err := h.queries.CurrentSite(r.Context())
		if err == nil {
			resp.Version = current.ID
			h.setVersion(current.ID)
			if id, err := strconv.ParseInt(have, 10, 64); err == nil {
				if previous, err := h.queries.GetSiteVersion(r.Context(), id); err == nil {
					resp.Swap = swapKind(previous, current)
				}
			}
		}
	}

	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	_ = json.NewEncoder(w).Encode(resp)
}

// swapKind reports whether the only change is one that can be applied without
// a navigation. A stylesheet that breaks out of its style tag also forces a
// reload, because in-place text cannot remove the elements it created.
func swapKind(previous, current sqlc.SiteVersion) string {
	if previous.Header != current.Header ||
		previous.Board != current.Board ||
		previous.Footer != current.Footer ||
		previous.StateSchema != current.StateSchema {
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

// VersionCSS returns the stylesheet for one version. The client uses it only
// after Version reports a css-only change.
func (h *Handlers) VersionCSS(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		http.Error(w, "Bad version id", http.StatusBadRequest)
		return
	}
	site, err := h.queries.GetSiteVersion(r.Context(), id)
	if err != nil {
		http.NotFound(w, r)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	_ = json.NewEncoder(w).Encode(map[string]string{
		"css": site.Css,
	})
}
