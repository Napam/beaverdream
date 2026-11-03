package webmaster

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// liveHarness runs the real live script with a page that has a journal feed,
// then reports whether the new-posts offer appeared instead of a reload. In
// the stale mode nothing changed on the server, and the journal runtime asks
// for a reload instead.
const liveHarness = `
const fs = require('fs')
const src = fs.readFileSync(process.argv[2], 'utf8')
const live = process.argv[3] === '1'
const stale = process.argv[4] === 'stale'
const listeners = {}

function element(id) {
  const props = {}
  return {
    id: id,
    style: {
      setProperty: (key, value) => { props[key] = value },
      getPropertyValue: (key) => props[key] || ''
    },
    textContent: '',
    value: '',
    onclick: null,
    setAttribute: () => {},
    getAttribute: () => null,
    addEventListener: () => {},
    getBoundingClientRect: () => ({ top: 0, bottom: 0 })
  }
}

const nodes = {}
function byID(id) {
  if (!nodes[id]) nodes[id] = element(id)
  return nodes[id]
}

const documentElement = element('html')
documentElement.getAttribute = (name) => {
  if (name === 'data-version') return '1'
  if (name === 'data-post') return '0'
  if (name === 'data-bd-live') return live ? '1' : '0'
  return null
}
const stored = {}
let replaced = ''

global.document = {
  readyState: 'complete',
  hidden: false,
  documentElement: documentElement,
  getElementById: byID,
  querySelector: () => null,
  querySelectorAll: () => [],
  addEventListener: (name, fn) => { listeners[name] = fn },
  dispatchEvent: () => {}
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
global.CustomEvent = function (name) { this.name = name }
global.setInterval = () => 1
global.fetch = async () => ({
  json: async () => ({ version: 1, post: stale ? 0 : 9, pending: 0 })
})

eval(src)
if (stale) listeners['beaverdream:stale']()

setTimeout(() => {
  console.log(JSON.stringify({
    display: byID('bd-new').style.getPropertyValue('display'),
    hasClick: typeof byID('bd-new-btn').onclick === 'function',
    replaced: replaced
  }))
}, 50)
`

type liveResult struct {
	Display  string `json:"display"`
	HasClick bool   `json:"hasClick"`
	Replaced string `json:"replaced"`
}

// runLiveScript runs the live script once. mode "stale" has the journal
// runtime ask for a reload while the server reports nothing new.
func runLiveScript(t *testing.T, live bool, mode ...string) liveResult {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node is not installed")
	}

	start := strings.Index(liveScript, "<script>")
	end := strings.LastIndex(liveScript, "</script>")
	if start < 0 || end < start {
		t.Fatal("liveScript has no script block")
	}
	script := liveScript[start+len("<script>") : end]

	dir := t.TempDir()
	scriptPath := filepath.Join(dir, "live.js")
	harnessPath := filepath.Join(dir, "harness.js")
	if err := os.WriteFile(scriptPath, []byte(script), 0o600); err != nil {
		t.Fatalf("write script: %v", err)
	}
	if err := os.WriteFile(harnessPath, []byte(liveHarness), 0o600); err != nil {
		t.Fatalf("write harness: %v", err)
	}

	flag := "0"
	if live {
		flag = "1"
	}
	args := append([]string{harnessPath, scriptPath, flag}, mode...)
	out, err := exec.Command(node, args...).CombinedOutput()
	if err != nil {
		t.Fatalf("node: %v\n%s", err, out)
	}
	var got liveResult
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatalf("decode: %v\n%s", err, out)
	}
	return got
}

// A page with a live feed must not reload on a new post, but it has to say so.
func TestLiveScriptOffersNewPostsOnALivePage(t *testing.T) {
	got := runLiveScript(t, true)

	if got.Display != "block" {
		t.Fatalf("new-posts display = %q, want block", got.Display)
	}
	if !got.HasClick {
		t.Fatal("the new-posts button has no click handler")
	}
	if got.Replaced != "" {
		t.Fatalf("the page reloaded to %q, it should have offered instead", got.Replaced)
	}
}

// A plain board keeps the old behavior: a new post reloads the page.
func TestLiveScriptReloadsOnAPlainPage(t *testing.T) {
	got := runLiveScript(t, false)

	if got.Replaced == "" {
		t.Fatal("a plain page did not reload on a new post")
	}
	if got.Display != "" {
		t.Fatalf("new-posts display = %q, want hidden on a plain page", got.Display)
	}
}

// The journal runtime found rules this page's code was not written for. The
// live script reloads even though the version and the posts look current.
func TestLiveScriptReloadsWhenTheJournalIsStale(t *testing.T) {
	got := runLiveScript(t, true, "stale")

	if got.Replaced == "" {
		t.Fatal("a stale journal did not reload the page")
	}
	if got.Display != "" {
		t.Fatalf("new-posts display = %q, want hidden: nothing new was posted", got.Display)
	}
}
