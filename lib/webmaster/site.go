// Package webmaster asks the model to rewrite the site. It owns the loop, the
// tool surface, and the rules about what the model may write.
package webmaster

import (
	"encoding/json"
	"fmt"
	"html"
	"math/rand/v2"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// PostsPlaceholder marks where the code-owned post list goes inside the page.
// The list stays out of the model's hands so a post can never vanish just
// because the model rewrote the page without it.
const PostsPlaceholder = "{{posts}}"

// Site is one version of the page: the body markup and a stylesheet.
// StateSchema and State belong to the model-declared shared state, not to the
// version row itself.
type Site struct {
	Version       int64
	CSS           string
	HTML          string
	StateSchema   string
	State         map[string]json.RawMessage
	JournalSchema string
	Journal       map[string]JournalState
	// JournalRulesUsed is the highest rules number stored under each key
	// with rows, declared or not. Only the schema tool reads it.
	JournalRulesUsed map[string]int
	// Holdings totals every key visitors hold, declared now or not. Only the
	// prompt reads it, to show the visitor counters and the hidden ones.
	Holdings []HoldingTotal
	// Mine and Tops seed the $mine and $tops signals on the first render.
	// Mine is this visitor's own holdings; Tops holds raw JSON so the page
	// package does not need the handler's row type.
	Mine map[string]int64
	Tops json.RawMessage
	// JournalCursor is the newest journal row id when the page was rendered.
	// The live stream pushes every row after it.
	JournalCursor    int64
	Visitor          string
	Name             string
	Plain            bool
	ChangesSinceHeal int
	// Datastar is the script path the page loads. Empty uses DatastarPath.
	Datastar string
	// Three is the path the page's import map gives the bare name "three".
	// Empty uses ThreePath.
	Three string
	// ThreeAddons is the path the import map gives "three/addons". Empty
	// uses ThreeAddonsPath.
	ThreeAddons string
}

// HoldingTotal is how much of one visitor counter everyone holds together,
// and by how many visitors.
type HoldingTotal struct {
	Key     string
	Total   int64
	Holders int64
}

// Post is one message on the board.
type Post struct {
	ID     int64
	Author string
	Body   string
	When   string
	// Kind is TurnPost for a visitor post and TurnReply for the webmaster's
	// answer. Only Kind marks a reply: a visitor can call themselves
	// webmaster.
	Kind string
}

// BaseCSS is prepended to every render. It holds only what the code-owned
// markup needs: the color and font variables, and the styles for the posts
// and the name link. The page layout belongs to the model's stylesheet, so a
// rewrite starts from a blank body, not from a column it has to undo.
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
}
*{box-sizing:border-box}
body{background:var(--bg);color:var(--fg);font-family:var(--font);line-height:1.6;font-size:16px}
.post{background:var(--panel);border:1px solid var(--border);border-radius:2px;padding:.65em .75em;margin-bottom:.55em}
.post-head{display:flex;gap:.5em;align-items:baseline;font-size:.82em;flex-wrap:wrap}
.post-author{color:var(--name);font-weight:700}
.post-when{color:var(--muted)}
.post-no{color:var(--muted);margin-left:auto}
.post-body{white-space:pre-wrap;margin-top:.3em;overflow-wrap:anywhere}
.post-body .quote{color:#789922}
.post-body .post-ref{color:var(--accent);text-decoration:underline}
.post-webmaster{border-left:3px solid var(--accent)}
.post-webmaster .post-author{color:var(--accent)}
.empty{color:var(--muted);font-style:italic}
.beaver-id{margin:1.25em;font-size:.75em;color:var(--muted);text-align:right}
.beaver-id a{color:var(--muted)}
.beaver-id a:hover{color:var(--accent)}
`

// identityCSS lays out the code-owned name page. The model never sees it.
const identityCSS = `
body{padding:1.5rem;max-width:48rem;margin:0 auto}
h1{font-size:1.7em;line-height:1.25;letter-spacing:-.02em;margin:0 0 .4em}
a{color:var(--accent);text-decoration:none}
.tagline{color:var(--muted);margin:0 0 1.25em;font-size:.95em}
.beaver-hint{margin:1em 0 0;font-size:.78em;color:var(--muted)}
.postbox{background:var(--panel);border:1px solid var(--border);border-radius:4px;padding:.9em;display:flex;flex-direction:column;gap:.55em;margin:0 0 1.5em}
.postbox-title{margin:0;font-weight:700;font-size:.95em}
.postbox input{width:100%;background:color-mix(in srgb,var(--bg) 90%,#fff 10%);color:var(--fg);border:1px solid var(--border);border-radius:3px;padding:.5em .6em;font:inherit;font-size:.95em}
.postbox input:focus{outline:2px solid var(--accent);outline-offset:1px;border-color:transparent}
.postbox-actions{display:flex;align-items:center;justify-content:space-between;gap:1em;color:var(--muted);font-size:.8em}
.postbox button{background:var(--accent);color:#fff;border:0;border-radius:3px;padding:.5em 1em;font:inherit;font-weight:600;cursor:pointer}
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
		class, kind := "post", TurnPost
		if p.Kind == TurnReply {
			class, kind = "post post-webmaster", TurnReply
		}
		fmt.Fprintf(
			&b,
			`<article class="%s" id="p%d" data-id="%d" data-kind="%s" data-author="%s" data-when="%s">`+
				`<div class="post-head">`+
				`<span class="post-author">%s</span>`+
				`<time class="post-when">%s</time>`+
				`<span class="post-no">No.%d</span></div>`+
				`<div class="post-body">%s</div></article>`,
			class,
			p.ID,
			p.ID,
			kind,
			html.EscapeString(author),
			html.EscapeString(p.When),
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
// keyframes survive whatever the model writes over the page variables. The
// turn is a custom property so the reduced-motion rule can stop it: an inline
// important animation would beat a stylesheet important one.
const busyCSS = `<style>
@keyframes bd-busy-spin { to { transform: rotate(var(--bd-busy-turn, 1turn)) } }
@media (prefers-reduced-motion: reduce) {
  #bd-busy-ring { --bd-busy-turn: 0turn }
}
</style>`

// BusyIndicator is the code-owned spinner. The live stream patches it whole
// when a rewrite is queued, starts or ends. Every rule on the container is
// inline and important, so a model stylesheet cannot move or hide it. The ring
// spins inside the container: the container's transform lock is important, and
// an important declaration beats an animation on the same element.
func BusyIndicator(busy bool) string {
	display := "none"
	if busy {
		display = "block"
	}
	return `<div id="bd-busy" role="status" aria-label="updating the page" ` +
		`style="display:` + display + `!important;visibility:visible!important;` +
		`opacity:1!important;transform:none!important;clip-path:none!important;` +
		`filter:none!important;clip:auto!important;position:fixed!important;top:10px!important;` +
		`left:10px!important;width:14px!important;height:14px!important;` +
		`box-sizing:border-box!important;z-index:2147483647!important;` +
		`pointer-events:none!important"><span id="bd-busy-ring" ` +
		`style="display:block!important;width:100%!important;height:100%!important;` +
		`box-sizing:border-box!important;border-radius:50%!important;` +
		`border:2px solid rgba(127,127,127,.35)!important;` +
		`border-top-color:rgba(127,127,127,.9)!important;` +
		`animation:bd-busy-spin .7s linear infinite!important"></span></div>`
}

// PostsContainer wraps the rendered posts in the element the live stream
// patches. display:contents keeps it out of the model's layout, so the posts
// lay out as children of whatever holds {{posts}}.
func PostsContainer(rendered string) string {
	return `<div id="bd-posts" style="display:contents">` + rendered + `</div>`
}

// DatastarPath is where the page loads Datastar from when the server does not
// pass a content-hashed path.
const DatastarPath = "/static/datastar.js"

// ThreePath is where the import map points "three" when the server does not
// pass a content-hashed path. The file is three.js 0.187.0, vendored in
// web/vendor, so model pages need no CDN.
const ThreePath = "/static/three.module.js"

// ThreeAddonsPath is where the import map points "three/addons": a chosen
// set of three.js examples bundled into one file that imports "three".
const ThreeAddonsPath = "/static/three-addons.js"

// FaviconPath is the beaver icon the code-owned pages put in their tab. The
// path is literal, not content-hashed: the document builders do not know the
// static root, and a 885-byte icon is not worth threading one through.
const FaviconPath = "/static/assets/beaver.svg"

// LiveCursor is what an open page has already shown. The page holds it as the
// _bd signal and sends it whenever its stream opens, so a reconnect resumes
// from the page, not from the start.
type LiveCursor struct {
	Version int64 `json:"version"`
	Post    int64 `json:"post"`
	Journal int64 `json:"journal"`
}

// visitorSignals is the first $mine and $tops, never nil, so markup bound to
// them has something to read before the stream connects.
func visitorSignals(site Site) (map[string]int64, json.RawMessage) {
	mine := site.Mine
	if mine == nil {
		mine = map[string]int64{}
	}
	tops := site.Tops
	if len(tops) == 0 || !json.Valid(tops) {
		tops = json.RawMessage(`{}`)
	}
	return mine, tops
}

// liveMount is the hidden element that opens the page's one live stream. Its
// signals carry the cursor, the shared state, which model markup reads as
// $shared.KEY, and the visitor's holdings and the top holders as $mine and
// $tops. Only _bd goes to /live; the leading underscore keeps it out of every
// @post the model writes.
func liveMount(site Site, newestPost int64) string {
	state := site.State
	if state == nil {
		state = map[string]json.RawMessage{}
	}
	mine, tops := visitorSignals(site)
	signals, err := json.Marshal(struct {
		BD     LiveCursor                 `json:"_bd"`
		Shared map[string]json.RawMessage `json:"shared"`
		Mine   map[string]int64           `json:"mine"`
		Tops   json.RawMessage            `json:"tops"`
	}{
		BD: LiveCursor{
			Version: site.Version,
			Post:    newestPost,
			Journal: site.JournalCursor,
		},
		Shared: state,
		Mine:   mine,
		Tops:   tops,
	})
	if err != nil {
		signals = []byte(`{"_bd":{},"shared":{},"mine":{},"tops":{}}`)
	}
	// retry always: a clean close (a deploy) reconnects too. The jitter keeps
	// every tab from reconnecting in the same instant.
	retry := 1000 + rand.IntN(2000)
	init := fmt.Sprintf(
		`@get('/live', {retry: 'always', retryMaxCount: Infinity, retryInterval: %d, `+
			`filterSignals: {include: /^_bd\./, exclude: /^$/}})`,
		retry,
	)
	return `<div id="bd-live" hidden data-signals="` + html.EscapeString(string(signals)) +
		`" data-init="` + html.EscapeString(init) + `"></div>`
}

// liveScript is the code-owned half of the live page that Datastar does not
// cover. The stream calls it: reload for a markup change, css after a
// stylesheet was patched in place, posts after new posts landed, state after
// shared values moved. Every reload saves unsent text and the reading
// position first, and it waits while someone is typing. The model can break
// this at runtime; that is part of the game.
const liveScript = `<script>
(function () {
  var replacePage = location.replace.bind(location)
  var html = document.documentElement
  var typingAt = 0
  var waitingSince = 0
  var reloading = false
  var restored = false

  // Keys and input both count as typing. Capture sees them even when a page
  // script stops propagation.
  function markTyping() { typingAt = Date.now() }
  document.addEventListener('input', markTyping, true)
  document.addEventListener('keydown', markTyping, true)
  // The journal runtime sends this when the server has journal rules this
  // page's code was not written for. Only a fresh page has the right code.
  document.addEventListener('beaverdream:stale', function () {
    maybeReload(html.getAttribute('data-version') || '0')
  })

  function emit(name, detail) {
    document.dispatchEvent(new CustomEvent(name, { detail: detail }))
  }

  function saveState() {
    try {
      // Before the draft has been restored, the box is still empty. Saving
      // now would overwrite the draft this page is about to bring back.
      if (!restored) return
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
        // Datastar's data-bind listens for input, not for a scripted value.
        area.dispatchEvent(new Event('input', { bubbles: true }))
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
    restored = true
  }

  // The version is in the URL, so each reload is a real navigation.
  // restoreState strips the query again.
  function maybeReload(next) {
    if (reloading) return
    var now = Date.now()
    if (!waitingSince) waitingSince = now
    if (now - typingAt < 1500 && now - waitingSince < 15000) {
      setTimeout(function () { maybeReload(next) }, 500)
      return
    }
    reloading = true
    saveState()
    replacePage('/?v=' + next)
  }

  window.bdLive = {
    reload: maybeReload,
    css: function (version) {
      html.setAttribute('data-version', String(version))
      emit('beaverdream:update', { version: version })
    },
    // mode is prepend when ids are new posts on top of the list, or replace
    // when ids are the whole list and anything placed before is gone.
    posts: function (newest, mode, ids) {
      // A stream can die after the post patch and before the cursor patch, so
      // a reconnect may prepend the same rows again. The ids are the truth.
      var box = document.getElementById('bd-posts')
      if (box) {
        var seen = {}
        for (var i = 0; i < box.children.length;) {
          var el = box.children[i]
          if (el.id && seen[el.id]) {
            box.removeChild(el)
          } else {
            if (el.id) seen[el.id] = true
            i++
          }
        }
      }
      html.setAttribute('data-post', String(newest))
      emit('beaverdream:post', { post: newest, mode: mode, ids: ids || [] })
    },
    state: function (values, mine, tops) {
      var tag = document.getElementById('bd-state')
      if (tag) tag.textContent = JSON.stringify(values)
      var own = window.bd && window.bd.state
      if (own && mine) own.mine = mine
      if (own && tops) own.tops = tops
      emit('beaverdream:state', {
        state: values,
        mine: own ? own.mine : mine || {},
        tops: own ? own.tops : tops || {}
      })
    }
  }

  if (document.readyState === 'loading') {
    document.addEventListener('DOMContentLoaded', restoreState)
  } else {
    restoreState()
  }
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
		`<link rel="icon" type="image/svg+xml" href="` + FaviconPath + `"><title>beaverdream</title><style>` +
		BaseCSS + identityCSS +
		`</style></head><body>` +
		`<h1>beaverdream</h1><p class="tagline">a small message board</p>` +
		`<form class="postbox" method="post" action="/identity">` +
		`<p class="postbox-title">your display name</p>` +
		`<input name="name" maxlength="` + strconv.Itoa(MaxIdentityLen) + `" autofocus autocomplete="nickname" placeholder="Anonymous" aria-label="display name"` + value + `>` +
		`<div class="postbox-actions"><span>shown next to every post you write</span>` +
		`<button type="submit">save name</button></div>` +
		`</form>` +
		`<p class="beaver-hint">no account, no password. pick any name and change it whenever you like.</p>` +
		back + `</body></html>`
}

// journalKeyConfig is one key as the client runtime gets it. Rules is the
// number this page's code was written for; the runtime holds it for the life
// of the tab. Max is the same row window the server keeps.
type journalKeyConfig struct {
	Match int `json:"match"`
	Rules int `json:"rules"`
	Max   int `json:"max,omitempty"`
}

// journalConfig is the data the client runtime needs: who is looking, and the
// declared journal keys with their current match, rules number and row limit.
// The row count and recent rows the prompt uses stay out of the page.
func journalConfig(site Site) []byte {
	keys := make(map[string]journalKeyConfig, len(site.Journal))
	for key, state := range site.Journal {
		keys[key] = journalKeyConfig{Match: state.Match, Rules: state.Rules, Max: state.Max}
	}
	mine, tops := visitorSignals(site)
	data, err := json.Marshal(struct {
		Visitor string                      `json:"visitor"`
		Keys    map[string]journalKeyConfig `json:"keys"`
		Mine    map[string]int64            `json:"mine"`
		Tops    json.RawMessage             `json:"tops"`
	}{Visitor: site.Visitor, Keys: keys, Mine: mine, Tops: tops})
	if err != nil {
		return []byte(`{"visitor":"","keys":{},"mine":{},"tops":{}}`)
	}
	return data
}

// SiteDoc builds the page served to visitors. There is no sandbox: the
// model's markup, CSS and scripts are the page. The only code-owned parts are
// the document shell, Datastar and the live stream mount, the live script, the
// post container, the busy spinner, and the small name link. The post form is
// model markup, so it can move it, restyle it, or break it.
func SiteDoc(site Site, posts []Post) string {
	name := site.Name
	if name == "" {
		name = "Anonymous"
	}
	escapedName := html.EscapeString(name)
	// Split on the posts placeholder before replacing {{name}}: a visitor
	// named "{{posts}}" must not become a second post container.
	parts := strings.Split(site.HTML, PostsPlaceholder)
	for i := range parts {
		parts[i] = strings.ReplaceAll(parts[i], "{{name}}", escapedName)
	}
	rendered := PostsContainer(RenderPosts(posts))
	page := strings.Join(parts, rendered)
	if len(parts) == 1 {
		// The model dropped the placeholder. Showing the posts beats hiding
		// them until someone notices.
		page += rendered
	}

	// data-post is the newest post on the page. The stream starts from it.
	var newest int64
	for _, p := range posts {
		newest = max(newest, p.ID)
	}

	datastar := site.Datastar
	if datastar == "" {
		datastar = DatastarPath
	}
	imports := map[string]string{"three": site.Three, "three/addons": site.ThreeAddons}
	if imports["three"] == "" {
		imports["three"] = ThreePath
	}
	if imports["three/addons"] == "" {
		imports["three/addons"] = ThreeAddonsPath
	}
	// A map of strings always marshals, and Marshal escapes < and >, so a
	// path cannot close the script tag.
	threeMap, _ := json.Marshal(map[string]map[string]string{"imports": imports})

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
	b.WriteString(`<link rel="icon" type="image/svg+xml" href="` + FaviconPath + `">`)
	b.WriteString(`<title>beaverdream</title>`)
	// The import map must come before every module script, so model scripts
	// can import "three" and "three/addons" without a URL.
	b.WriteString(`<script type="importmap">`)
	b.Write(threeMap)
	b.WriteString(`</script>`)
	// A module script is deferred, so Datastar starts after the whole page,
	// model scripts included, has parsed.
	b.WriteString(`<script type="module" src="` + html.EscapeString(datastar) + `"></script>`)
	// The journal config and runtime are code-owned and come before any model
	// markup, so a page script can call bd.journal() while the page parses.
	b.WriteString(`<script type="application/json" id="bd-journal">`)
	b.Write(journalConfig(site))
	b.WriteString(`</script>`)
	b.WriteString(JournalRuntime)
	// The live script goes before the model's markup, so an unclosed tag in
	// the page cannot swallow it.
	b.WriteString(liveScript)
	b.WriteString(busyCSS)
	b.WriteString(`<style>`)
	b.WriteString(BaseCSS)
	b.WriteString(`</style><style id="bd-css">`)
	b.WriteString(site.CSS)
	b.WriteString(`</style></head><body>`)
	// The stream mount and the spinner come before the model's markup for
	// the same reason as the live script.
	b.WriteString(liveMount(site, newest))
	b.WriteString(BusyIndicator(false))
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
	b.WriteString(page)
	b.WriteString(`<p class="beaver-id"><a href="/identity">set name</a></p>`)
	b.WriteString(`</body></html>`)
	return b.String()
}
