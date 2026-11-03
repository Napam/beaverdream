package webmaster

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// runtimeHarness runs the real client runtime against a scripted server. Each
// scenario names a server and the steps a page takes; the harness reports the
// feed, what the server was asked, the beacons sent, the events dispatched
// and what each append resolved to.
const runtimeHarness = `
const fs = require('fs')
const src = fs.readFileSync(process.argv[2], 'utf8')
const scenario = process.argv[3]

const config = JSON.stringify({
  visitor: 'me',
  keys: { moves: { match: 1, rules: 1 } }
})

function node() {
  return {
    textContent: '', style: { setProperty: () => {} },
    dataset: {}, setAttribute: () => {}, getAttribute: () => null,
    addEventListener: () => {}, appendChild: () => {}
  }
}

const page = node()
page.textContent = config
const html = node()
html.getAttribute = (name) => (name === 'data-version' ? '7' : null)

const events = []
const reports = []
const fetches = []
const results = []
const sent = []
let unhandled = 0

global.document = {
  hidden: false,
  getElementById: (id) => (id === 'bd-journal' ? page : node()),
  documentElement: html,
  addEventListener: () => {},
  dispatchEvent: (e) => { events.push(e.name) }
}
global.window = global
global.addEventListener = () => {}
// Node has a read-only navigator of its own, so plain assignment is ignored.
Object.defineProperty(global, 'navigator', {
  value: { sendBeacon: (url, blob) => { reports.push(blob.body); return true } },
  configurable: true
})
global.Blob = function (parts) { this.body = JSON.parse(parts[0]) }
global.CustomEvent = function (name, init) { this.name = name; this.detail = init && init.detail }
global.setInterval = (fn) => { global.__tick = fn; return 1 }
global.clearInterval = () => {}
process.on('unhandledRejection', () => { unhandled++ })

function reply(status, body) {
  return { ok: status >= 200 && status < 300, status: status, json: async () => body }
}

const row = (id, match, rules) => ({ id: id, visitor: 'them', payload: { n: id }, match: match || 1, rules: rules || 1 })

// Each server answers one request. state lets a scenario change its answers
// between steps.
// hold parks the request after skip more requests, until a step releases it.
const state = { read: 'ok', post: 200, postError: '', hold: null, skip: 0 }
const servers = {
  // An append returns a row newer than one still in flight. The cursor must
  // not jump past it.
  append: (url, method) => {
    if (method === 'POST') return reply(200, { id: 3, visitor: 'me', payload: { n: 1 }, match: 1, rules: 1 })
    if (url.indexOf('since=0') >= 0) return reply(200, { match: 1, rules: 1, rows: [row(1)] })
    return reply(200, { match: 1, rules: 1, rows: [row(2), { id: 3, visitor: 'me', payload: { n: 1 }, match: 1, rules: 1 }] })
  },
  scripted: (url, method) => {
    if (method === 'POST') {
      if (state.post === 200) return reply(200, { id: 9, visitor: 'me', payload: {}, match: 1, rules: 1 })
      return reply(state.post, { error: state.postError })
    }
    if (state.read === 'fail') return reply(500, { error: 'could not read that' })
    if (state.read === 'rules') return reply(200, { match: 1, rules: 2, rows: [row(5, 1, 2)] })
    if (state.read === 'match') return reply(200, { match: 2, rules: 1, rows: [row(6, 2)] })
    return reply(200, { match: 1, rules: 1, rows: [row(1), row(2)] })
  }
}
const server = servers[scenario === 'append' ? 'append' : 'scripted']

global.fetch = (url, opts) => {
  const method = (opts && opts.method) || 'GET'
  fetches.push(method + ' ' + url)
  if (method === 'POST') sent.push(JSON.parse(opts.body))
  const answer = server(url, method, opts && opts.body)
  if (state.hold && state.skip-- <= 0) {
    const hold = state.hold
    state.hold = null
    return new Promise((resolve) => { hold.release = () => resolve(answer) })
  }
  return Promise.resolve(answer)
}

eval(src)

const feed = window.bd.journal('moves')
const draws = []
feed.start(
  (rows) => rows.map((r) => r.id),
  (s) => { draws.push(s) }
)
const settle = () => new Promise((resolve) => setTimeout(resolve, 20))

const steps = {
  append: async () => {
    await feed.load()
    results.push(await feed.append({ n: 1 }))
    await feed.poll()
  },
  // A failed read, in a poll or a load, must leave the board alone.
  failedRead: async () => {
    await settle()
    state.read = 'fail'
    await feed.poll()
    await feed.load()
  },
  // Other rules mean this page's reducer is the wrong one: stop and ask for
  // a full reload, never adopt the new number.
  rulesChange: async () => {
    await settle()
    state.read = 'rules'
    await feed.poll()
    await feed.poll()
    results.push(await feed.append({ n: 1 }))
  },
  // Expected refusals resolve null and never reach the error beacon. A
  // payload the server rejects is the page's bug and is reported.
  refusals: async () => {
    await settle()
    for (const [status, error] of [[429, 'slow down'], [401, 'pick a name first'], [400, 'payload must be a JSON object'], [400, '` + JournalMatchClosed + `']]) {
      state.post = status
      state.postError = error
      results.push(await feed.append({ n: 1 }))
    }
    await settle()
    state.post = 409
    state.postError = 'the page is out of date'
    results.push(await feed.append({ n: 1 }))
  },
  // A new match reloads the feed. No poll may run while that load is out.
  busyDuringLoad: async () => {
    await settle()
    state.read = 'match'
    const hold = {}
    // The poll goes through; the load it starts is parked.
    state.hold = hold
    state.skip = 1
    const polled = feed.poll()
    await settle()
    const before = fetches.length
    await feed.poll()
    results.push(fetches.length - before)
    if (hold.release) hold.release()
    await polled
    await settle()
  }
}

;(async () => {
  await steps[scenario]()
  await settle()
  console.log(JSON.stringify({
    ids: feed.rows.map((r) => r.id),
    cursor: feed.cursor,
    match: feed.match,
    rules: feed.rules,
    stale: feed.stale,
    busy: feed.busy,
    lastDraw: draws[draws.length - 1] || null,
    draws: draws.length,
    events: events,
    reports: reports,
    fetches: fetches,
    results: results,
    sent: sent,
    unhandled: unhandled
  }))
})()
`

type runtimeReport struct {
	Message string `json:"message"`
	Version int    `json:"version"`
}

type sentAppend struct {
	Match int `json:"match"`
	Rules int `json:"rules"`
}

type runtimeResult struct {
	IDs       []int64           `json:"ids"`
	Cursor    int64             `json:"cursor"`
	Match     int               `json:"match"`
	Rules     int               `json:"rules"`
	Stale     bool              `json:"stale"`
	Busy      bool              `json:"busy"`
	LastDraw  []int64           `json:"lastDraw"`
	Draws     int               `json:"draws"`
	Events    []string          `json:"events"`
	Reports   []runtimeReport   `json:"reports"`
	Fetches   []string          `json:"fetches"`
	Results   []json.RawMessage `json:"results"`
	Sent      []sentAppend      `json:"sent"`
	Unhandled int               `json:"unhandled"`
}

func runRuntime(t *testing.T, scenario string) runtimeResult {
	t.Helper()
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node is not installed")
	}

	start := strings.Index(JournalRuntime, "<script>")
	end := strings.LastIndex(JournalRuntime, "</script>")
	if start < 0 || end < start {
		t.Fatal("JournalRuntime has no script block")
	}
	script := JournalRuntime[start+len("<script>") : end]

	dir := t.TempDir()
	scriptPath := filepath.Join(dir, "runtime.js")
	harnessPath := filepath.Join(dir, "harness.js")
	if err := os.WriteFile(scriptPath, []byte(script), 0o600); err != nil {
		t.Fatalf("write runtime: %v", err)
	}
	if err := os.WriteFile(harnessPath, []byte(runtimeHarness), 0o600); err != nil {
		t.Fatalf("write harness: %v", err)
	}

	out, err := exec.Command(node, harnessPath, scriptPath, scenario).CombinedOutput()
	if err != nil {
		t.Fatalf("node: %v\n%s", err, out)
	}
	var got runtimeResult
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatalf("decode: %v\n%s", err, out)
	}
	return got
}

func TestJournalRuntimeKeepsRowsFromAnAppend(t *testing.T) {
	got := runRuntime(t, "append")

	want := []int64{1, 2, 3}
	if len(got.IDs) != len(want) {
		t.Fatalf("rows = %v, want %v (the append must not skip row 2)", got.IDs, want)
	}
	for i := range want {
		if got.IDs[i] != want[i] {
			t.Fatalf("rows = %v, want %v", got.IDs, want)
		}
	}
	if got.Cursor != 3 {
		t.Errorf("cursor = %d, want 3", got.Cursor)
	}
	if len(got.LastDraw) != 3 {
		t.Errorf("last draw = %v, want all three rows", got.LastDraw)
	}
	if len(got.Sent) != 1 || got.Sent[0].Rules != 1 || got.Sent[0].Match != 1 {
		t.Errorf("sent = %+v, want one append carrying match 1 and the page's rules 1", got.Sent)
	}
}

// A failed read in a poll or a load leaves the board as it was drawn.
func TestJournalRuntimeKeepsTheBoardOnAFailedRead(t *testing.T) {
	got := runRuntime(t, "failedRead")

	if len(got.IDs) != 2 || got.IDs[0] != 1 || got.IDs[1] != 2 {
		t.Fatalf("rows = %v, want [1 2] kept through the failed reads", got.IDs)
	}
	if len(got.LastDraw) != 2 {
		t.Fatalf("last draw = %v, want the board from before the failure", got.LastDraw)
	}
	if got.Busy {
		t.Error("a failed read left the feed busy, so it would never poll again")
	}
	if got.Stale {
		t.Error("a failed read expired the page")
	}
}

// Other rules from the server never replace the page's own number: the feed
// stops, asks for a full reload, and refuses further writes.
func TestJournalRuntimeReloadsThePageOnNewRules(t *testing.T) {
	got := runRuntime(t, "rulesChange")

	if got.Rules != 1 {
		t.Fatalf(
			"rules = %d, want 1: the page must not adopt rules its code was not written for",
			got.Rules,
		)
	}
	if !got.Stale {
		t.Fatal("the feed did not expire on new rules")
	}
	stale := 0
	for _, e := range got.Events {
		if e == "beaverdream:stale" {
			stale++
		}
	}
	if stale != 1 {
		t.Fatalf("stale events = %d, want 1 (events %v)", stale, got.Events)
	}
	for _, id := range got.IDs {
		if id == 5 {
			t.Fatal("a row under the new rules was folded by the old reducer")
		}
	}
	for _, f := range got.Fetches {
		if strings.HasPrefix(f, "POST") {
			t.Fatal("an expired page still wrote to the journal")
		}
	}
	if len(got.Results) != 1 || string(got.Results[0]) != "null" {
		t.Fatalf("append on an expired page = %s, want null", got.Results)
	}
}

// Refusals the page did not cause resolve null and stay out of the room. A
// rejected payload is the page's own bug, so it is reported, with the page's
// version. Nothing rejects.
func TestJournalRuntimeTakesRefusalsQuietly(t *testing.T) {
	got := runRuntime(t, "refusals")

	if got.Unhandled != 0 {
		t.Fatalf("unhandled rejections = %d, want 0", got.Unhandled)
	}
	if len(got.Results) != 5 {
		t.Fatalf("results = %s, want five", got.Results)
	}
	for i, r := range got.Results {
		if string(r) != "null" {
			t.Errorf("append %d resolved %s, want null", i, r)
		}
	}
	if len(got.Reports) != 1 ||
		!strings.Contains(got.Reports[0].Message, "payload must be a JSON object") {
		t.Fatalf("reports = %+v, want one for the rejected payload", got.Reports)
	}
	if got.Reports[0].Version != 7 {
		t.Errorf("report version = %d, want the page's 7", got.Reports[0].Version)
	}
	loads := 0
	for _, f := range got.Fetches {
		if strings.HasSuffix(f, "since=0") {
			loads++
		}
	}
	if loads != 2 {
		t.Errorf(
			"feed loads = %d, want the start plus one after the closed match (%v)",
			loads,
			got.Fetches,
		)
	}
	if !got.Stale {
		t.Error("a 409 did not expire the page")
	}
}

// A poll that finds a new match keeps the feed busy until the load settles,
// so no second read runs beside it.
func TestJournalRuntimeStaysBusyDuringALoad(t *testing.T) {
	got := runRuntime(t, "busyDuringLoad")

	if len(got.Results) != 1 || string(got.Results[0]) != "0" {
		t.Fatalf("reads during the load = %s, want 0", got.Results)
	}
	if got.Match != 2 {
		t.Fatalf("match = %d, want 2 after the load", got.Match)
	}
	if got.Busy {
		t.Error("the feed stayed busy after the load settled")
	}
}
