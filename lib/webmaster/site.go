// Package webmaster asks the model to rewrite the site. It owns the loop, the
// tool surface, and the rules about what the model may write.
package webmaster

import (
	"fmt"
	"html"
	"strings"
	"time"
)

// Region names the model may replace.
const (
	RegionHeader = "header"
	RegionBoard  = "board"
	RegionFooter = "footer"
)

// PostsPlaceholder marks where the code-owned post list goes inside the board
// region. The list stays out of the model's hands so a post can never vanish
// from the board just because the model rewrote the region without it.
const PostsPlaceholder = "{{posts}}"

// Site is one version of the page: three regions, a stylesheet, and the note
// shown above the sandbox.
type Site struct {
	Version int64
	CSS     string
	Note    string
	Header  string
	Board   string
	Footer  string
}

// Post is one message on the board.
type Post struct {
	ID     int64
	Author string
	Body   string
	When   string
}

// BaseCSS is prepended to every render. It defines the variables the model is
// told to override and styles the post list, which the model only has to
// place.
const BaseCSS = `
:root{
  --bg:#101014;
  --fg:#d9d7d1;
  --accent:#7fd6a4;
  --muted:#8b8a86;
  --font:ui-monospace,SFMono-Regular,Menlo,Consolas,monospace;
  --pad:2rem;
}
*{box-sizing:border-box}
html,body{margin:0}
body{background:var(--bg);color:var(--fg);font-family:var(--font);line-height:1.55;padding:var(--pad);font-size:15px}
a{color:var(--accent)}
h1,h2,h3{line-height:1.2;margin:0 0 .5em}
h1{font-size:1.6em;letter-spacing:-.02em}
.tagline{color:var(--muted);margin:-.25em 0 1.5em;font-size:.9em}
main{margin-top:1.5em}
.board-title{font-size:.75em;letter-spacing:.14em;text-transform:uppercase;color:var(--muted);margin:1.5em 0 .25em}
.post{border-top:1px solid color-mix(in srgb,var(--fg) 18%,transparent);padding:.9em 0}
.post:first-child{border-top:0}
.post-head{display:flex;gap:.6em;align-items:baseline;font-size:.8em}
.post-author{color:var(--accent);font-weight:700}
.post-when{color:var(--muted)}
.post-body{white-space:pre-wrap;margin-top:.35em;overflow-wrap:anywhere}
.empty{color:var(--muted);font-style:italic}
.colophon{margin-top:2.5em;padding-top:1em;border-top:1px solid color-mix(in srgb,var(--fg) 18%,transparent);color:var(--muted);font-size:.75em}
.beaver-note{border-left:2px solid var(--accent);padding-left:.75em;color:var(--accent);margin:0 0 1.5em}
.beaver-controls{margin-top:2.5em;border-top:1px solid color-mix(in srgb,var(--fg) 18%,transparent);padding-top:1.25em;display:flex;flex-direction:column;gap:.75em}
.beaver-controls input,.beaver-controls textarea{width:100%;background:color-mix(in srgb,var(--bg) 85%,var(--fg) 8%);color:var(--fg);border:1px solid color-mix(in srgb,var(--fg) 25%,transparent);border-radius:4px;padding:.6em .7em;font:inherit;font-size:.9em}
.beaver-controls textarea{resize:vertical}
.beaver-controls input:focus,.beaver-controls textarea:focus{outline:2px solid var(--accent);outline-offset:1px}
.beaver-actions{display:flex;justify-content:space-between;align-items:center;gap:1em;color:var(--muted);font-size:.8em}
.beaver-controls button{background:var(--accent);color:var(--bg);border:0;border-radius:4px;padding:.5em 1.1em;font:inherit;font-weight:700;cursor:pointer}
.beaver-links{display:flex;justify-content:space-between;color:var(--muted);font-size:.75em;margin-top:.25em}
`

// RenderPosts turns stored posts into the markup that replaces
// PostsPlaceholder. Everything is escaped: post bodies are untrusted input.
func RenderPosts(posts []Post) string {
	if len(posts) == 0 {
		return `<p class="empty">no posts yet</p>`
	}
	var b strings.Builder
	for _, p := range posts {
		author := p.Author
		if author == "" {
			author = "anon"
		}
		fmt.Fprintf(
			&b,
			`<article class="post" id="p%d"><div class="post-head">`+
				`<span class="post-author">%s</span>`+
				`<time class="post-when">%s</time></div>`+
				`<div class="post-body">%s</div></article>`,
			p.ID,
			html.EscapeString(author),
			html.EscapeString(p.When),
			html.EscapeString(p.Body),
		)
	}
	return b.String()
}

// FormatWhen renders a stored timestamp for a post header, falling back to the
// raw value if it ever changes shape.
func FormatWhen(s string) string {
	t, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		return s
	}
	return t.Format("Jan 2 15:04")
}

// controls is the only fixed markup in the page. It sits at the end of the
// body, after the model's footer. The model can style it away or delete it
// with script; /plain and /admin stay reachable by URL.
const controls = `<section class="beaver-controls">` +
	`<form method="post" action="/posts">` +
	`<input name="author" maxlength="32" placeholder="name (optional)" autocomplete="off">` +
	`<textarea name="body" required maxlength="2000" rows="3" placeholder="say something and the board will change"></textarea>` +
	`<div class="beaver-actions"><span>posting rewrites the site</span>` +
	`<button type="submit">post</button></div></form>` +
	`<nav class="beaver-links"><a href="/plain">plain view</a>` +
	`<a href="/admin">admin</a></nav></section>`

// liveReload reloads the page when someone else's post lands. The model can
// break it, which is part of the game; it is convenience, not a guarantee.
const liveReload = `<script>
(function () {
  var current = document.documentElement.getAttribute('data-version')
  setInterval(function () {
    fetch('/version', { cache: 'no-store' })
      .then(function (r) { return r.json() })
      .then(function (d) {
        if (String(d.version) !== current) location.reload()
      })
      .catch(function () {})
  }, 5000)
})()
</script>`

// SiteDoc builds the page served to visitors. There is no sandbox: the
// model's regions, CSS and scripts are the page. The only code-owned parts
// are the note, the post form and the two links, and even those can be
// styled away by the model. That is the point.
func SiteDoc(site Site, posts []Post) string {
	board := site.Board
	rendered := RenderPosts(posts)
	if strings.Contains(board, PostsPlaceholder) {
		board = strings.ReplaceAll(board, PostsPlaceholder, rendered)
	} else {
		// The model dropped the placeholder. Showing the posts beats hiding
		// them until someone notices.
		board += rendered
	}

	var b strings.Builder
	fmt.Fprintf(
		&b,
		`<!doctype html><html lang="en" data-version="%d"><head><meta charset="utf-8">`,
		site.Version,
	)
	b.WriteString(`<meta name="viewport" content="width=device-width,initial-scale=1">`)
	b.WriteString(`<link rel="icon" href="data:,">`)
	b.WriteString(`<title>beaverdream</title><style>`)
	b.WriteString(BaseCSS)
	b.WriteString(site.CSS)
	b.WriteString(`</style></head><body>`)
	if site.Note != "" {
		b.WriteString(`<p class="beaver-note">`)
		b.WriteString(html.EscapeString(site.Note))
		b.WriteString(`</p>`)
	}
	b.WriteString(site.Header)
	b.WriteString(board)
	b.WriteString(site.Footer)
	b.WriteString(controls)
	b.WriteString(liveReload)
	b.WriteString(`</body></html>`)
	return b.String()
}
