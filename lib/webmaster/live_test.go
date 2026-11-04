package webmaster

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
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

if (scenario === 'draft') {
  stored['bd:draft'] = 'hello'
  stored['bd:focused'] = '1'
  text = textarea
}
if (scenario === 'save') {
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
  stale: () => listeners['beaverdream:stale'](),
  css: () => window.bdLive.css(6),
  posts: () => window.bdLive.posts(9, 'prepend', [9, 8]),
  state: () => window.bdLive.state({ likes: 3 }),
  save: () => window.bdLive.reload(5),
  dedupe: () => window.bdLive.posts(9)
}
steps[scenario]()

setTimeout(() => {
  console.log(JSON.stringify({
    replaced: replaced,
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
	out := runJS(t, liveScript, liveHarness, scenario)
	var got liveResult
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatalf("decode: %v\n%s", err, out)
	}
	return got
}

// runJS runs a harness against the script inside src. It uses node or bun,
// whichever is installed, and fails rather than skipping when neither is, so a
// green suite means the client code ran.
func runJS(t *testing.T, src, harness, scenario string) []byte {
	t.Helper()
	runtime, err := jsRuntime()
	if err != nil {
		t.Skip("neither node nor bun is installed")
	}

	start := strings.Index(src, "<script>")
	end := strings.LastIndex(src, "</script>")
	if start < 0 || end < start {
		t.Fatal("the script has no script block")
	}
	script := src[start+len("<script>") : end]

	dir := t.TempDir()
	scriptPath := filepath.Join(dir, "script.js")
	harnessPath := filepath.Join(dir, "harness.js")
	if err := os.WriteFile(scriptPath, []byte(script), 0o600); err != nil {
		t.Fatalf("write script: %v", err)
	}
	if err := os.WriteFile(harnessPath, []byte(harness), 0o600); err != nil {
		t.Fatalf("write harness: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, runtime, harnessPath, scriptPath, scenario).
		CombinedOutput()
	if err != nil {
		t.Fatalf("%s: %v\n%s", filepath.Base(runtime), err, out)
	}
	return out
}

func jsRuntime() (string, error) {
	var lastErr error
	for _, name := range []string{"node", "bun"} {
		path, err := exec.LookPath(name)
		if err == nil {
			return path, nil
		}
		lastErr = err
	}
	return "", lastErr
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

// A reconnect can prepend a post the board already shows. The id keeps the
// board unique instead of stacking the same post twice.
func TestLiveScriptDropsDuplicatePosts(t *testing.T) {
	got := runLiveScript(t, "dedupe")
	if strings.Join(got.Posts, ",") != "p9,p8,p7" {
		t.Errorf("posts = %v, want p9,p8,p7", got.Posts)
	}
}
