package webmaster

import (
	"encoding/json"
	"testing"

	"github.com/Napam/beaverdream/lib/webmaster/internal/jsrun"
)

// worldHarness runs the real journal/state/save runtime against one world's
// mount path and storage root. It records every request URL, every error
// beacon and every browser key the runtime touched.
const worldHarness = `
const fs = require('fs')
const src = fs.readFileSync(process.argv[2], 'utf8')
const scenario = process.argv[3]
const world = scenario === 'world'

const config = world
  ? { visitor: 'me', keys: { moves: { match: 1, rules: 1 } }, base: '/w/heroes', store: 'bd:world42:' }
  : { visitor: 'me', keys: { moves: { match: 1, rules: 1 } } }

function node() {
  return {
    textContent: '',
    setAttribute: () => {}, getAttribute: () => null,
    addEventListener: () => {}, appendChild: () => {}
  }
}
const configTag = node()
configTag.textContent = JSON.stringify(config)
const html = node()
html.getAttribute = (name) => (name === 'data-version' ? '7' : null)

const fetches = []
const beacons = []
const handlers = {}
const stored = {}
if (scenario === 'world') {
  stored['bd:save:spot'] = '{"x":9}'
  stored['bd:world42:save:spot'] = '{"x":4}'
} else {
  stored['bd:save:spot'] = '{"x":9}'
}

global.document = {
  hidden: false,
  getElementById: (id) => (id === 'bd-journal' ? configTag : node()),
  documentElement: html,
  addEventListener: () => {},
  dispatchEvent: () => {}
}
global.window = global
global.addEventListener = (name, fn) => { handlers[name] = fn }
Object.defineProperty(global, 'navigator', {
  value: { sendBeacon: (url) => { beacons.push(url); return true } },
  configurable: true
})
global.Blob = function (parts) { this.body = parts[0] }
global.CustomEvent = function () {}
Object.defineProperty(global, 'localStorage', {
  value: {
    getItem: (k) => (k in stored ? stored[k] : null),
    setItem: (k, v) => { stored[k] = String(v) },
    removeItem: (k) => { delete stored[k] }
  },
  configurable: true
})

function reply(status, body) {
  return { ok: status >= 200 && status < 300, status: status, json: async () => body }
}

global.fetch = (url, opts) => {
  const method = (opts && opts.method) || 'GET'
  fetches.push(method + ' ' + url)
  if (url.indexOf('/x/state/') >= 0) return Promise.resolve(reply(200, { ok: true, mine: { coins: 2 } }))
  if (method === 'POST') {
    return Promise.resolve(reply(200, { id: 3, key: 'moves', visitor: 'me', payload: { n: 1 }, match: 1, rules: 1 }))
  }
  return Promise.resolve(reply(200, { match: 1, rules: 1, rows: [] }))
}

eval(src)

const settle = () => new Promise((resolve) => setTimeout(resolve, 20))
const feed = window.bd.journal('moves')

;(async () => {
  feed.start((rows) => rows.map((r) => r.id), () => {})
  await settle()
  await feed.append({ n: 1 })
  await window.bd.state.add('coins', 2)
  await settle()

  // A throwing reducer is the page's bug, so the runtime reports it.
  feed.start(() => { throw new Error('boom') }, () => {})
  window.bd._rows([{ id: 2, key: 'moves', visitor: 'them', payload: {}, match: 1, rules: 1 }])
  await settle()

  const loaded = window.bd.load('spot', null)
  window.bd.save('new', { y: 2 })
  if (handlers.pagehide) handlers.pagehide()

  console.log(JSON.stringify({
    loaded: loaded,
    keys: Object.keys(stored).sort(),
    fetches: fetches,
    beacons: beacons
  }))
})()
`

type worldResult struct {
	Loaded  map[string]float64 `json:"loaded"`
	Keys    []string           `json:"keys"`
	Fetches []string           `json:"fetches"`
	Beacons []string           `json:"beacons"`
}

func runWorld(t *testing.T, scenario string) worldResult {
	t.Helper()
	out := jsrun.Run(t, JournalRuntime, worldHarness, scenario)
	var got worldResult
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatalf("decode: %v\n%s", err, out)
	}
	return got
}

// A world runtime prefixes every board route with the world mount path.
func TestWorldRuntimePrefixesItsRoutes(t *testing.T) {
	got := runWorld(t, "world")
	for _, want := range []string{
		"GET /w/heroes/j/moves?since=0",
		"POST /w/heroes/j/moves",
		"POST /w/heroes/x/state/coins?add=2",
	} {
		if !contains(got.Fetches, want) {
			t.Errorf("fetches = %v, want %q", got.Fetches, want)
		}
	}
	if len(got.Beacons) != 1 || got.Beacons[0] != "/w/heroes/x/err" {
		t.Errorf("beacons = %v, want the world's error route", got.Beacons)
	}
}

// Saves and loads use the world's immutable id. A key left at the root, or
// under another world, is not read.
func TestWorldRuntimeNamespacesSaves(t *testing.T) {
	got := runWorld(t, "world")
	if got.Loaded["x"] != 4 {
		t.Errorf("loaded = %v, want the world's saved value", got.Loaded)
	}
	for _, key := range []string{"bd:world42:save:new", "bd:world42:save:spot"} {
		if !contains(got.Keys, key) {
			t.Errorf("keys = %v, want %q", got.Keys, key)
		}
	}
	if !contains(got.Keys, "bd:save:spot") {
		t.Errorf("keys = %v, want the root key left untouched", got.Keys)
	}
}

// Without a world the runtime keeps the root routes and keys.
func TestRootRuntimeKeepsRootRoutes(t *testing.T) {
	got := runWorld(t, "root")
	for _, want := range []string{
		"GET /j/moves?since=0",
		"POST /j/moves",
		"POST /x/state/coins?add=2",
	} {
		if !contains(got.Fetches, want) {
			t.Errorf("fetches = %v, want %q", got.Fetches, want)
		}
	}
	if len(got.Beacons) != 1 || got.Beacons[0] != "/x/err" {
		t.Errorf("beacons = %v, want the root error route", got.Beacons)
	}
	if got.Loaded["x"] != 9 {
		t.Errorf("loaded = %v, want the root saved value", got.Loaded)
	}
}

func contains(items []string, want string) bool {
	for _, item := range items {
		if item == want {
			return true
		}
	}
	return false
}
