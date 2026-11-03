// Package webmaster asks the model to rewrite the site. It owns the loop, the
// tool surface, and the rules about what the model may write.
package webmaster

import (
	"encoding/json"
	"fmt"
	"html"
	"regexp"
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

// Site is one version of the page: three regions and a stylesheet. StateSchema
// and State belong to the model-declared shared state, not to the version row
// itself.
//
// Note is kept from older versions. It is no longer rendered or edited, but
// seed matching still compares it, so it travels with the version.
type Site struct {
	Version          int64
	CSS              string
	Note             string
	Header           string
	Board            string
	Footer           string
	StateSchema      string
	State            map[string]json.RawMessage
	Name             string
	Plain            bool
	ChangesSinceHeal int
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
  --bg:#f2efe6;
  --panel:#fbf9f2;
  --fg:#1f1f1f;
  --accent:#34345c;
  --name:#117743;
  --muted:#6b6b63;
  --border:#cfc8b8;
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
.post{background:var(--panel);border:1px solid var(--border);border-radius:2px;padding:.65em .75em;margin-bottom:.55em}
.post-head{display:flex;gap:.5em;align-items:baseline;font-size:.82em;flex-wrap:wrap}
.post-author{color:var(--name);font-weight:700}
.post-when{color:var(--muted)}
.post-no{color:var(--muted);margin-left:auto}
.post-body{white-space:pre-wrap;margin-top:.3em;overflow-wrap:anywhere}
.post-body .quote{color:#789922}
.post-body .post-ref{color:var(--accent);text-decoration:underline}
.empty{color:var(--muted);font-style:italic}
.colophon{margin-top:2em;padding-top:.8em;border-top:1px solid var(--border);color:var(--muted);font-size:.8em}
.beaver-id{margin-top:1.25em;font-size:.75em;color:var(--muted);text-align:right}
.beaver-id a{color:var(--muted)}
.beaver-id a:hover{color:var(--accent)}
.beaver-hint{margin:1em 0 0;font-size:.78em;color:var(--muted)}
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

var postRefPattern = regexp.MustCompile(`&gt;&gt;(\d+)`)

// RenderPosts turns stored posts into the markup that replaces
// PostsPlaceholder. Everything is escaped: post bodies are untrusted input.
// The small board conventions survive escaping: lines that start with > are
// greentext, and >>12 links to another post.
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
				`<time class="post-when">%s</time>`+
				`<span class="post-no">No.%d</span></div>`+
				`<div class="post-body">%s</div></article>`,
			p.ID,
			html.EscapeString(author),
			html.EscapeString(p.When),
			p.ID,
			renderPostBody(p.Body),
		)
	}
	return b.String()
}

// renderPostBody escapes a post line by line, then adds greentext and quote
// links. Escaping happens before either feature, so a body cannot smuggle
// markup in.
func renderPostBody(body string) string {
	lines := strings.Split(body, "\n")
	for i, line := range lines {
		escaped := postRefPattern.ReplaceAllString(
			html.EscapeString(line),
			`<a class="post-ref" href="#p$1">&gt;&gt;$1</a>`,
		)
		if strings.HasPrefix(line, ">") && !strings.HasPrefix(line, ">>") {
			lines[i] = `<span class="quote">` + escaped + `</span>`
		} else {
			lines[i] = escaped
		}
	}
	return strings.Join(lines, "\n")
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

// busyCSS animates the code-owned spinner. It is separate from BaseCSS so the
// keyframes survive whatever the model writes over the page variables.
const busyCSS = `<style>
@keyframes bd-busy-spin { to { transform: rotate(360deg) } }
@media (prefers-reduced-motion: reduce) {
  #bd-busy { animation-duration: 2.4s !important }
}
</style>`

// liveScript keeps open pages current. A new post always reloads, because the
// posts are server-rendered markup. A stylesheet-only change is applied in
// place, so colors can shift under the cursor. A markup change reloads. Every
// reload saves unsent text and the reading position first, and it waits while
// someone is typing. The model can break this at runtime; that is part of the game.
const liveScript = `<script>
(function () {
  var fetchPage = window.fetch.bind(window)
  var replacePage = location.replace.bind(location)
  var version = document.documentElement.getAttribute('data-version') || '0'
  var post = document.documentElement.getAttribute('data-post') || '0'
  var typingAt = 0
  var waitingSince = 0
  var reloading = false

  document.addEventListener('input', function () { typingAt = Date.now() }, true)

  function saveState(next) {
    try {
      var area = document.querySelector('form[action="/posts"] textarea[name=body]')
      if (area) {
        sessionStorage.setItem('bd:draft', area.value)
        sessionStorage.setItem('bd:focused', document.activeElement === area ? '1' : '0')
      }
      var posts = document.querySelectorAll('.post')
      for (var i = 0; i < posts.length; i++) {
        var box = posts[i].getBoundingClientRect()
        if (box.bottom > 0) {
          sessionStorage.setItem('bd:scroll', JSON.stringify({
            id: posts[i].id,
            top: Math.round(box.top)
          }))
          break
        }
      }
    } catch (e) {}
  }

  function restoreState() {
    try {
      if (location.search.indexOf('v=') !== -1) {
        if (window.history && history.replaceState) {
          history.replaceState(null, '', location.pathname + location.hash)
        }
      }
      var draft = sessionStorage.getItem('bd:draft')
      var area = document.querySelector('form[action="/posts"] textarea[name=body]')
      if (draft !== null && area) {
        area.value = draft
        if (sessionStorage.getItem('bd:focused') === '1') {
          area.focus()
          area.setSelectionRange(area.value.length, area.value.length)
        }
        sessionStorage.removeItem('bd:draft')
        sessionStorage.removeItem('bd:focused')
      }
      var raw = sessionStorage.getItem('bd:scroll')
      if (raw && !location.hash) {
        var anchor = JSON.parse(raw)
        var el = document.getElementById(anchor.id)
        if (el) {
          var top = el.getBoundingClientRect().top + window.scrollY
          window.scrollTo(0, Math.max(0, top - anchor.top))
        }
      }
      sessionStorage.removeItem('bd:scroll')
    } catch (e) {}
  }

  // The post stamp is in the URL too, so each reload is a real navigation.
  // restoreState strips the query again.
  function reload(next, nextPost) {
    var url = '/?v=' + next
    if (nextPost !== undefined) url += '&p=' + nextPost
    replacePage(url)
  }

  function applyCSS(next) {
    return fetchPage('/version/' + next + '/css', { cache: 'no-store' })
      .then(function (r) { return r.json() })
      .then(function (d) {
        var style = document.getElementById('bd-css')
        if (!style) return false
        style.textContent = d.css || ''
        version = String(next)
        document.documentElement.setAttribute('data-version', version)
        document.dispatchEvent(new CustomEvent('beaverdream:update', { detail: d }))
        return true
      })
  }

  function maybeReload(next, nextPost) {
    if (reloading) return
    var now = Date.now()
    if (!waitingSince) waitingSince = now
    if (now - typingAt < 1500 && now - waitingSince < 15000) {
      setTimeout(function () { maybeReload(next, nextPost) }, 500)
      return
    }
    reloading = true
    saveState(next)
    reload(next, nextPost)
  }

  // setBusy shows the code-owned spinner while a mutation is queued or
  // running. The display value is set important so a model stylesheet cannot
  // hide it.
  function setBusy(state) {
    var el = document.getElementById('bd-busy')
    if (!el) return
    var busy = state === 'running' || state === 'queued'
    el.style.setProperty('display', busy ? 'block' : 'none', 'important')
  }

  function tick() {
    if (document.hidden || reloading || waitingSince) return
    fetchPage('/version?have=' + version, { cache: 'no-store' })
      .then(function (r) { return r.json() })
      .then(function (d) {
        var state = d.running ? 'running' : (d.pending > 0 ? 'queued' : 'idle')
        setBusy(state)
        document.documentElement.setAttribute('data-bd-state', state)
        document.dispatchEvent(new CustomEvent('beaverdream:status', { detail: d }))
        // Reset rewinds post ids, so any difference counts, not only a
        // larger id.
        if (String(d.post) !== post) {
          maybeReload(d.version, d.post)
          return
        }
        if (String(d.version) === version) return
        if (d.swap === 'css') {
          applyCSS(d.version).then(function (ok) {
            if (!ok) maybeReload(d.version)
          }).catch(function () { maybeReload(d.version) })
          return
        }
        maybeReload(d.version)
      })
      .catch(function () {})
  }

  if (document.readyState === 'loading') {
    document.addEventListener('DOMContentLoaded', function () {
      restoreState()
      // The spinner lives in the body, so the first check waits for it.
      tick()
    })
  } else {
    restoreState()
    tick()
  }
  setInterval(tick, 500)
})()
</script>`

// IdentityDoc is the code-owned page shown before a visitor's first post, and
// again when they want to set their display name. It is deliberately plain: the
// model has not taken over yet.
func IdentityDoc(name string) string {
	value := ""
	back := ""
	if name != "" {
		value = ` value="` + html.EscapeString(name) + `"`
		back = `<p class="beaver-id"><a href="/">back to the board</a></p>`
	}
	return `<!doctype html><html lang="en"><head><meta charset="utf-8">` +
		`<meta name="viewport" content="width=device-width,initial-scale=1">` +
		`<link rel="icon" href="data:,"><title>beaverdream</title><style>` +
		BaseCSS +
		`</style></head><body>` +
		`<h1>beaverdream</h1><p class="tagline">a small message board</p>` +
		`<form class="postbox" method="post" action="/identity">` +
		`<p class="postbox-title">your display name</p>` +
		`<input name="name" maxlength="32" autofocus autocomplete="nickname" placeholder="Anonymous" aria-label="display name"` + value + `>` +
		`<div class="postbox-actions"><span>shown next to every post you write</span>` +
		`<button type="submit">save name</button></div>` +
		`</form>` +
		`<p class="beaver-hint">no account, no password. pick any name and change it whenever you like.</p>` +
		back + `</body></html>`
}

// SiteDoc builds the page served to visitors. There is no sandbox: the
// model's regions, CSS and scripts are the page. The only code-owned parts are
// the document shell, the live reload script, the busy spinner, and the small
// name link. The post form is model markup, so it can move it, restyle it, or
// break it.
func SiteDoc(site Site, posts []Post) string {
	name := site.Name
	if name == "" {
		name = "Anonymous"
	}
	escapedName := html.EscapeString(name)
	board := strings.ReplaceAll(site.Board, "{{name}}", escapedName)
	header := strings.ReplaceAll(site.Header, "{{name}}", escapedName)
	footer := strings.ReplaceAll(site.Footer, "{{name}}", escapedName)

	rendered := RenderPosts(posts)
	if strings.Contains(board, PostsPlaceholder) {
		board = strings.ReplaceAll(board, PostsPlaceholder, rendered)
	} else {
		// The model dropped the placeholder. Showing the posts beats hiding
		// them until someone notices.
		board += rendered
	}

	// data-post is the newest post on the page. The live script reloads when
	// the server reports a different one.
	var newest int64
	for _, p := range posts {
		newest = max(newest, p.ID)
	}

	var b strings.Builder
	// data-name is code-owned so the model's JavaScript can tell who is
	// looking without reading the HttpOnly cookie.
	fmt.Fprintf(
		&b,
		`<!doctype html><html lang="en" data-version="%d" data-post="%d" data-name="%s"><head><meta charset="utf-8">`,
		site.Version,
		newest,
		escapedName,
	)
	b.WriteString(`<meta name="viewport" content="width=device-width,initial-scale=1">`)
	b.WriteString(`<link rel="icon" href="data:,">`)
	b.WriteString(`<title>beaverdream</title>`)
	// The live script goes before the model's markup, so an unclosed tag in a
	// region cannot swallow it.
	b.WriteString(liveScript)
	b.WriteString(busyCSS)
	b.WriteString(`<style>`)
	b.WriteString(BaseCSS)
	b.WriteString(`</style><style id="bd-css">`)
	b.WriteString(site.CSS)
	b.WriteString(`</style></head><body>`)
	// The busy spinner is code-owned and inline-styled, so a model stylesheet
	// cannot move or hide it. The live script toggles display while a rewrite
	// is queued or running.
	b.WriteString(`<div id="bd-busy" role="status" aria-label="updating the page" ` +
		`style="display:none!important;position:fixed!important;top:10px!important;` +
		`left:10px!important;width:14px!important;height:14px!important;` +
		`box-sizing:border-box!important;z-index:2147483647!important;` +
		`pointer-events:none!important;border-radius:50%!important;` +
		`border:2px solid rgba(127,127,127,.35)!important;` +
		`border-top-color:rgba(127,127,127,.9)!important;` +
		`animation:bd-busy-spin .7s linear infinite!important"></div>`)
	if site.StateSchema != "" || len(site.State) > 0 {
		state := site.State
		if state == nil {
			state = map[string]json.RawMessage{}
		}
		data, err := json.Marshal(state)
		if err != nil {
			data = []byte("{}")
		}
		b.WriteString(`<script type="application/json" id="bd-state">`)
		b.Write(data)
		b.WriteString(`</script>`)
	}
	b.WriteString(header)
	b.WriteString(board)
	b.WriteString(footer)
	b.WriteString(`<p class="beaver-id"><a href="/identity">set name</a></p>`)
	b.WriteString(`</body></html>`)
	return b.String()
}
