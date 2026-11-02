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
  --bg:#f7f5ef;
  --panel:#fffdf7;
  --fg:#1f1f1f;
  --accent:#1f6b45;
  --muted:#6b6b63;
  --border:#dcd6c8;
  --font:-apple-system,BlinkMacSystemFont,"Segoe UI",Roboto,Helvetica,Arial,sans-serif;
  --pad:1.5rem;
}
*{box-sizing:border-box}
html,body{margin:0}
body{background:var(--bg);color:var(--fg);font-family:var(--font);line-height:1.6;padding:var(--pad);font-size:16px;max-width:48rem;margin:0 auto}
a{color:var(--accent);text-decoration:none}
a:hover{text-decoration:underline}
h1,h2,h3{line-height:1.25;margin:0 0 .4em}
h1{font-size:1.7em;letter-spacing:-.02em}
.tagline{color:var(--muted);margin:0 0 1.25em;font-size:.95em}
main{margin-top:1.25em}
.board-title{font-size:.8em;letter-spacing:.08em;text-transform:uppercase;color:var(--muted);margin:1.5em 0 .5em}
.post{background:var(--panel);border:1px solid var(--border);border-radius:4px;padding:.8em .9em;margin-bottom:.7em}
.post-head{display:flex;gap:.6em;align-items:baseline;font-size:.85em}
.post-author{color:var(--accent);font-weight:700}
.post-when{color:var(--muted)}
.post-body{white-space:pre-wrap;margin-top:.35em;overflow-wrap:anywhere}
.empty{color:var(--muted);font-style:italic}
.colophon{margin-top:2em;padding-top:.8em;border-top:1px solid var(--border);color:var(--muted);font-size:.8em}
.beaver-note{background:var(--panel);border:1px solid var(--border);border-left:3px solid var(--accent);padding:.6em .8em;color:var(--muted);margin:0 0 1.25em;font-size:.9em}
.postbox{background:var(--panel);border:1px solid var(--border);border-radius:4px;padding:.9em;display:flex;flex-direction:column;gap:.55em;margin:0 0 1.5em}
.postbox-title{margin:0;font-weight:700;font-size:.95em}
.postbox input,.postbox textarea{width:100%;background:color-mix(in srgb,var(--bg) 90%,#fff 10%);color:var(--fg);border:1px solid var(--border);border-radius:3px;padding:.5em .6em;font:inherit;font-size:.95em}
.postbox textarea{min-height:5.5em;resize:vertical}
.postbox input::placeholder,.postbox textarea::placeholder{color:#9a9a90}
.postbox input:focus,.postbox textarea:focus{outline:2px solid var(--accent);outline-offset:1px;border-color:transparent}
.postbox-actions{display:flex;align-items:center;justify-content:space-between;gap:1em;color:var(--muted);font-size:.8em}
.postbox button{background:var(--accent);color:#fff;border:0;border-radius:3px;padding:.5em 1em;font:inherit;font-weight:600;cursor:pointer}
.postbox button:hover{filter:brightness(1.08)}
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

// IdentityDoc is the code-owned page shown before a visitor's first post. It
// asks for a name and stores it in a cookie. It is deliberately plain: the
// model has not taken over yet.
func IdentityDoc() string {
	return `<!doctype html><html lang="en"><head><meta charset="utf-8">` +
		`<meta name="viewport" content="width=device-width,initial-scale=1">` +
		`<link rel="icon" href="data:,"><title>beaverdream</title><style>` +
		BaseCSS +
		`</style></head><body>` +
		`<h1>beaverdream</h1><p class="tagline">a small message board</p>` +
		`<form class="postbox" method="post" action="/identity">` +
		`<p class="postbox-title">pick a name</p>` +
		`<input name="name" maxlength="32" required autofocus autocomplete="nickname" placeholder="your name" aria-label="your name">` +
		`<div class="postbox-actions"><span>no account needed</span>` +
		`<button type="submit">join</button></div>` +
		`</form></body></html>`
}

// SiteDoc builds the page served to visitors. There is no sandbox: the
// model's regions, CSS and scripts are the page. The only code-owned parts are
// the note, the document shell, and the live reload script. The post form is
// model markup, so it can move it, restyle it, or break it.
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
	b.WriteString(liveReload)
	b.WriteString(`</body></html>`)
	return b.String()
}
