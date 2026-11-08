package webmaster

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/Napam/beaverdream/lib/webmaster/internal/jsrun"
)

// liveHarness runs the real live script, then plays one call the live stream
// would make, and reports what the page did.
const liveHarness = `
const fs = require('fs')
const src = fs.readFileSync(process.argv[2], 'utf8')
const scenario = process.argv[3]
const listeners = {}
const attrs = { 'data-version': '1', 'data-post': '0' }
const events = []
const stateTag = { textContent: '{}' }
const journalTag = { textContent: '{}' }
const world = scenario.indexOf('world') === 0
if (world) {
  journalTag.textContent = JSON.stringify({ base: '/worlds/heroes', store: 'bd:abc:' })
}
const stored = {}
let replaced = ''
let text = null

const textarea = {
  value: '',
  events: [],
  focus: function () {},
  setSelectionRange: function () {},
  dispatchEvent: function (e) { this.events.push(e.type) }
}
const box = {
  children: [],
  removeChild: function (el) {
    const i = this.children.indexOf(el)
    if (i >= 0) this.children.splice(i, 1)
  }
}
const posts = []

global.document = {
  readyState: 'complete',
  hidden: false,
  activeElement: null,
  documentElement: {
    getAttribute: (name) => (name in attrs ? attrs[name] : null),
    setAttribute: (name, value) => { attrs[name] = value }
  },
  getElementById: (id) => {
    if (id === 'bd-state') return stateTag
    if (id === 'bd-journal') return journalTag
    if (id === 'bd-posts') return box
    return null
  },
  querySelector: (sel) => (sel.indexOf('textarea') >= 0 ? text : null),
  querySelectorAll: (sel) => (sel === '.post' ? posts : []),
  addEventListener: (name, fn) => { listeners[name] = fn },
  dispatchEvent: (e) => { events.push({ name: e.name, detail: e.detail }) }
}
global.window = global
global.sessionStorage = {
  getItem: (key) => (key in stored ? stored[key] : null),
  setItem: (key, value) => { stored[key] = value },
  removeItem: (key) => { delete stored[key] }
}
global.history = { replaceState: () => {} }
global.location = {
  search: '', hash: '', pathname: '/',
  replace: (url) => { replaced = url }
}
global.CustomEvent = function (name, init) { this.name = name; this.detail = init && init.detail }
global.Event = function (type) { this.type = type }
global.setInterval = () => { throw new Error('the live script must not poll') }

// The cap scenario runs the script's retry timers by hand on a fake clock, so
// the 15 s cap is reached at once. The real setTimeout comes back after it.
const realSetTimeout = setTimeout
let clock = 1000000
let timers = []
let waited = 0
if (scenario === 'cap') {
  Date.now = () => clock
  global.setTimeout = (fn, ms) => { timers.push({ at: clock + ms, fn: fn }) }
}

if (scenario === 'draft') {
  stored['bd:draft'] = 'hello'
  stored['bd:focused'] = '1'
  text = textarea
}
if (scenario === 'worlddraft') {
  stored['bd:abc:draft'] = 'hello'
  stored['bd:abc:focused'] = '1'
  stored['bd:draft'] = 'another world'
  text = textarea
}
if (scenario === 'save') {
  text = textarea
  textarea.value = 'draft text'
  posts.push({ id: 'p7', getBoundingClientRect: () => ({ top: 12, bottom: 40 }) })
}
if (scenario === 'worldsave') {
  text = textarea
  textarea.value = 'draft text'
  posts.push({ id: 'p7', getBoundingClientRect: () => ({ top: 12, bottom: 40 }) })
}
if (scenario === 'dedupe') {
  box.children = [{ id: 'p9' }, { id: 'p8' }, { id: 'p9' }, { id: 'p7' }]
}

eval(src)

const steps = {
  draft: () => {},
  reload: () => window.bdLive.reload(5),
  typing: () => { listeners.input(); window.bdLive.reload(5) },
  keydown: () => { listeners.keydown(); window.bdLive.reload(5) },
  cap: () => {
    // The visitor presses a key on every tick, so only the cap can release it.
    const start = clock
    listeners.keydown()
    window.bdLive.reload(5)
    for (let tick = 0; tick < 60 && !replaced; tick++) {
      clock += 500
      listeners.keydown()
      const due = timers.filter((t) => t.at <= clock)
      timers = timers.filter((t) => t.at > clock)
      due.forEach((t) => t.fn())
    }
    waited = clock - start
    setTimeout = realSetTimeout
  },
  stale: () => listeners['beaverdream:stale'](),
  css: () => window.bdLive.css(6),
  posts: () => window.bdLive.posts(9, 'prepend', [9, 8]),
  state: () => window.bdLive.state({ likes: 3 }),
  save: () => window.bdLive.reload(5),
  dedupe: () => window.bdLive.posts(9),
  worldreload: () => window.bdLive.reload(5),
  worlddraft: () => {},
  worldsave: () => window.bdLive.reload(5)
}
steps[scenario]()

setTimeout(() => {
  console.log(JSON.stringify({
    replaced: replaced,
    waited: waited,
    attrs: attrs,
    events: events,
    stateTag: stateTag.textContent,
    text: textarea.value,
    textInputs: textarea.events.filter((type) => type === 'input').length,
    posts: box.children.map((el) => el.id),
    stored: stored
  }))
}, 50)
`

type liveEvent struct {
	Name   string          `json:"name"`
	Detail json.RawMessage `json:"detail"`
}

type liveResult struct {
	Replaced   string            `json:"replaced"`
	Waited     int               `json:"waited"`
	Attrs      map[string]string `json:"attrs"`
	Events     []liveEvent       `json:"events"`
	StateTag   string            `json:"stateTag"`
	Text       string            `json:"text"`
	TextInputs int               `json:"textInputs"`
	Posts      []string          `json:"posts"`
	Stored     map[string]string `json:"stored"`
}

func runLiveScript(t *testing.T, scenario string) liveResult {
	t.Helper()
	out := jsrun.Run(t, liveScript, liveHarness, scenario)
	var got liveResult
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatalf("decode: %v\n%s", err, out)
	}
	return got
}

func eventNamed(events []liveEvent, name string) (liveEvent, bool) {
	for _, e := range events {
		if e.Name == name {
			return e, true
		}
	}
	return liveEvent{}, false
}

// A markup change reloads to the new version.
func TestLiveScriptReloadsOnAMarkupChange(t *testing.T) {
	got := runLiveScript(t, "reload")
	if got.Replaced != "/?v=5" {
		t.Fatalf("replaced = %q, want /?v=5", got.Replaced)
	}
}

// A reload waits while the visitor is typing.
func TestLiveScriptWaitsForTyping(t *testing.T) {
	got := runLiveScript(t, "typing")
	if got.Replaced != "" {
		t.Fatalf("the page reloaded to %q under the visitor's fingers", got.Replaced)
	}
}

// A key press alone holds a reload, the same as input does.
func TestLiveScriptWaitsForKeydown(t *testing.T) {
	got := runLiveScript(t, "keydown")
	if got.Replaced != "" {
		t.Fatalf("the page reloaded to %q under the visitor's keys", got.Replaced)
	}
}

// A visitor who never stops typing is still reloaded once the 15 s cap has
// passed. The release comes on the first retry after the cap.
func TestLiveScriptReleasesATypingPageAtTheCap(t *testing.T) {
	got := runLiveScript(t, "cap")
	if got.Replaced != "/?v=5" {
		t.Fatalf("replaced = %q, want /?v=5 once the cap passed", got.Replaced)
	}
	if got.Waited < 15000 || got.Waited >= 15500 {
		t.Errorf("released after %d ms, want the first retry past 15000 ms", got.Waited)
	}
}

// The journal runtime found rules this page's code was not written for. The
// live script reloads even though nothing else changed.
func TestLiveScriptReloadsWhenTheJournalIsStale(t *testing.T) {
	got := runLiveScript(t, "stale")
	if got.Replaced != "/?v=1" {
		t.Fatalf("replaced = %q, want a reload of the current version", got.Replaced)
	}
}

// New posts are patched in place. The page records the newest one and tells
// page scripts, and never reloads.
func TestLiveScriptAnnouncesNewPosts(t *testing.T) {
	got := runLiveScript(t, "posts")
	if got.Replaced != "" {
		t.Fatalf("a new post reloaded the page to %q", got.Replaced)
	}
	if got.Attrs["data-post"] != "9" {
		t.Errorf("data-post = %q, want 9", got.Attrs["data-post"])
	}
	event, ok := eventNamed(got.Events, "beaverdream:post")
	if !ok {
		t.Fatalf("events = %+v, want beaverdream:post", got.Events)
	}
	if string(event.Detail) != `{"post":9,"mode":"prepend","ids":[9,8]}` {
		t.Errorf("detail = %s, want the mode and the new ids", event.Detail)
	}
}

// A stylesheet patched in place moves the page's version, which the error
// beacon reports.
func TestLiveScriptTracksAnInPlaceStylesheet(t *testing.T) {
	got := runLiveScript(t, "css")
	if got.Attrs["data-version"] != "6" {
		t.Errorf("data-version = %q, want 6", got.Attrs["data-version"])
	}
	if _, ok := eventNamed(got.Events, "beaverdream:update"); !ok {
		t.Errorf("events = %+v, want beaverdream:update", got.Events)
	}
}

// Pushed shared values reach page scripts and the bd-state tag.
func TestLiveScriptHandsSharedStateToScripts(t *testing.T) {
	got := runLiveScript(t, "state")
	if got.StateTag != `{"likes":3}` {
		t.Errorf("bd-state = %q, want the new values", got.StateTag)
	}
	e, ok := eventNamed(got.Events, "beaverdream:state")
	if !ok || !strings.Contains(string(e.Detail), `"likes":3`) {
		t.Errorf("events = %+v, want beaverdream:state with the values", got.Events)
	}
}

// A restored draft is put back and announced as input, so a Datastar binding
// sees it and a submit disabled on !$draft turns back on.
func TestLiveScriptRestoresADraft(t *testing.T) {
	got := runLiveScript(t, "draft")
	if got.Text != "hello" {
		t.Errorf("textarea = %q, want the saved draft", got.Text)
	}
	if got.TextInputs != 1 {
		t.Errorf("input events = %d, want 1 so data-bind updates", got.TextInputs)
	}
	if _, ok := got.Stored["bd:draft"]; ok {
		t.Error("the restored draft was left in storage")
	}
}

// A reload saves the draft and the reading position before it navigates.
func TestLiveScriptSavesTheDraftBeforeReloading(t *testing.T) {
	got := runLiveScript(t, "save")
	if got.Stored["bd:draft"] != "draft text" {
		t.Errorf("saved draft = %q, want the textarea value", got.Stored["bd:draft"])
	}
	if !strings.Contains(got.Stored["bd:scroll"], `"id":"p7"`) {
		t.Errorf("saved scroll = %q, want the visible post", got.Stored["bd:scroll"])
	}
	if got.Replaced != "/?v=5" {
		t.Errorf("replaced = %q, want /?v=5", got.Replaced)
	}
}

// A reconnect can prepend a post the page already shows. The id keeps the
// page unique instead of stacking the same post twice.
func TestLiveScriptDropsDuplicatePosts(t *testing.T) {
	got := runLiveScript(t, "dedupe")
	if strings.Join(got.Posts, ",") != "p9,p8,p7" {
		t.Errorf("posts = %v, want p9,p8,p7", got.Posts)
	}
}

// A world page reloads into its own mount path, not the server root.
func TestLiveScriptUsesTheWorldBaseForReloads(t *testing.T) {
	got := runLiveScript(t, "worldreload")
	if got.Replaced != "/worlds/heroes/?v=5" {
		t.Fatalf("replaced = %q, want the world's reload URL", got.Replaced)
	}
}

// A draft is read and cleared under the world's storage root, never the root
// page's keys.
func TestLiveScriptUsesTheWorldStorageForDrafts(t *testing.T) {
	got := runLiveScript(t, "worlddraft")
	if got.Text != "hello" {
		t.Errorf("textarea = %q, want the world's saved draft", got.Text)
	}
	if _, ok := got.Stored["bd:abc:draft"]; ok {
		t.Error("the restored world draft was left in storage")
	}
	if got.Stored["bd:draft"] != "another world" {
		t.Errorf("root draft = %q, want it untouched", got.Stored["bd:draft"])
	}
}

// A reload saves the draft and reading position under the world's storage
// root.
func TestLiveScriptSavesUnderTheWorldStorage(t *testing.T) {
	got := runLiveScript(t, "worldsave")
	if got.Stored["bd:abc:draft"] != "draft text" {
		t.Errorf("world draft = %q, want the textarea value", got.Stored["bd:abc:draft"])
	}
	if !strings.Contains(got.Stored["bd:abc:scroll"], `"id":"p7"`) {
		t.Errorf("world scroll = %q, want the visible post", got.Stored["bd:abc:scroll"])
	}
	if _, ok := got.Stored["bd:draft"]; ok {
		t.Error("a world draft leaked into the root storage key")
	}
	if got.Replaced != "/worlds/heroes/?v=5" {
		t.Errorf("replaced = %q, want the world's reload URL", got.Replaced)
	}
}
