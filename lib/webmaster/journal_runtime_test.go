package webmaster

import (
	"encoding/json"
	"strings"
	"testing"
)

// runtimeHarness runs the real client runtime against a scripted server. Each
// scenario names the steps a page takes and the rows the live stream pushes;
// the harness reports the feed, what the server was asked, the beacons sent,
// the events dispatched and what each append resolved to.
const runtimeHarness = `
const fs = require('fs')
const src = fs.readFileSync(process.argv[2], 'utf8')
const scenario = process.argv[3]

const config = JSON.stringify({
  visitor: 'me',
  keys: {
    moves: { match: 1, rules: 1 },
    cut: { match: 1, rules: 1, max: 3 }
  }
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
global.setInterval = () => { throw new Error('the runtime must not poll') }
// The browser draws on the next frame; tests flush it on the next tick. A
// hidden tab parses this as a pause, so asking for a frame is an error there.
global.requestAnimationFrame = (fn) => {
  if (document.hidden) throw new Error('requestAnimationFrame does not run hidden')
  return setTimeout(fn, 0)
}
process.on('unhandledRejection', () => { unhandled++ })

function reply(status, body) {
  return { ok: status >= 200 && status < 300, status: status, json: async () => body }
}

const row = (id, match, rules) => ({
  id: id, key: 'moves', visitor: 'them', payload: { n: id }, match: match || 1, rules: rules || 1
})

// state lets a scenario change the server's answers between steps. hold
// parks the next read until the scenario releases it.
const state = { read: 'ok', post: 200, postError: '', hold: null }
function server(url, method) {
  if (method === 'POST') {
    if (state.post === 200) return reply(200, { id: 3, key: 'moves', visitor: 'me', payload: { n: 1 }, match: 1, rules: 1 })
    return reply(state.post, { error: state.postError })
  }
  if (state.read === 'fail') return reply(500, { error: 'could not read that' })
  if (state.read === 'rules') return reply(200, { match: 1, rules: 2, rows: [row(5, 1, 2)] })
  if (state.read === 'match') return reply(200, { match: 2, rules: 1, rows: [row(6, 2)] })
  return reply(200, { match: 1, rules: 1, rows: [row(1), row(2)] })
}

global.fetch = (url, opts) => {
  const method = (opts && opts.method) || 'GET'
  fetches.push(method + ' ' + url)
  if (method === 'POST') sent.push(JSON.parse(opts.body))
  const answer = server(url, method)
  if (state.hold && method === 'GET') {
    const hold = state.hold
    state.hold = null
    return new Promise((resolve) => { hold.release = () => resolve(answer) })
  }
  return Promise.resolve(answer)
}

eval(src)

const settle = () => new Promise((resolve) => setTimeout(resolve, 20))
const feed = window.bd.journal('moves')
const draws = []
function start() {
  feed.start(
    (rows) => rows.map((r) => r.id),
    (s) => { draws.push(s) }
  )
}

const cut = window.bd.journal('cut')
function startCut() {
  cut.start(
    (rows) => rows.map((r) => r.id),
    () => {}
  )
}
const cutRow = (id) => ({
  id: id, key: 'cut', visitor: 'them', payload: { n: id }, match: 1, rules: 1
})

const steps = {
  // Pushed rows join the feed in order, and a row that comes back twice, from
  // the append and then from the stream, is folded once.
  push: async () => {
    start()
    await settle()
    window.bd._rows([row(4)])
    results.push(await feed.append({ n: 1 }))
    window.bd._rows([row(3, 1, 1)])
    window.bd._rows([{ id: 9, key: 'other', payload: {}, match: 1, rules: 1 }])
  },
  // A row pushed while the first read is out is held, not dropped, even when
  // the read does not include it.
  heldDuringLoad: async () => {
    const hold = {}
    state.hold = hold
    start()
    window.bd._rows([row(3)])
    const before = fetches.length
    await feed.load()
    results.push(fetches.length - before)
    hold.release()
    await settle()
  },
  // A failed read leaves the board alone.
  failedRead: async () => {
    start()
    await settle()
    state.read = 'fail'
    await feed.load()
  },
  // A feed whose first read failed has nothing to fold onto: a push reads
  // the whole feed again and keeps the pushed row.
  failedFirstRead: async () => {
    state.read = 'fail'
    start()
    await settle()
    window.bd._rows([row(3)])
    await settle()
    results.push(feed.rows.length)
    state.read = 'ok'
    window.bd._rows([row(4)])
    await settle()
  },
  // A pushed row under other rules means this page's reducer is the wrong
  // one: stop and ask for a full reload, never fold it.
  rulesChange: async () => {
    start()
    await settle()
    window.bd._rows([row(5, 1, 2)])
    window.bd._rows([row(6, 1, 2)])
    results.push(await feed.append({ n: 1 }))
  },
  // A pushed row from the next match reads the feed again.
  newMatch: async () => {
    start()
    await settle()
    state.read = 'match'
    window.bd._rows([row(6, 2)])
    await settle()
  },
  // A resync reads every started feed again.
  resync: async () => {
    start()
    await settle()
    window.bd._resync()
    await settle()
  },
  // Expected refusals resolve null and never reach the error beacon. A
  // payload the server rejects is the page's bug and is reported.
  refusals: async () => {
    start()
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
  // The feed keeps at most the key's max rows, the same window the server
  // keeps, and a trimmed row cannot come back from a late echo.
  trim: async () => {
    startCut()
    await settle()
    window.bd._rows([
      cutRow(11), cutRow(12), cutRow(13),
      cutRow(14), cutRow(15), cutRow(16)
    ])
    await settle()
    results.push(cut.rows.map((r) => r.id))
    results.push(Object.keys(cut.seen).length)
    window.bd._rows([cutRow(11)])
    await settle()
    results.push(cut.rows.map((r) => r.id))
  },
  // Rows that arrive in one burst cost one draw, on the next frame.
  oneDrawPerFrame: async () => {
    start()
    await settle()
    const before = draws.length
    window.bd._rows([row(4)])
    window.bd._rows([row(5)])
    window.bd._rows([row(6)])
    results.push(draws.length - before)
    await settle()
    results.push(draws.length - before)
  },
  // A hidden tab still renders, so a title or sound set in render fires.
  hiddenStillDraws: async () => {
    start()
    await settle()
    document.hidden = true
    try {
      window.bd._rows([row(4)])
      await settle()
      results.push(draws[draws.length - 1])
    } finally {
      document.hidden = false
    }
  }
}

;(async () => {
  await steps[scenario]()
  await settle()
  console.log(JSON.stringify({
    ids: feed.rows.map((r) => r.id),
    match: feed.match,
    rules: feed.rules,
    stale: feed.stale,
    held: feed.held !== null,
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
	Match     int               `json:"match"`
	Rules     int               `json:"rules"`
	Stale     bool              `json:"stale"`
	Held      bool              `json:"held"`
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
	out := runJS(t, JournalRuntime, runtimeHarness, scenario)
	var got runtimeResult
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatalf("decode: %v\n%s", err, out)
	}
	return got
}

func sameIDs(got, want []int64) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range want {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}

func TestJournalRuntimeFoldsPushedRows(t *testing.T) {
	got := runRuntime(t, "push")

	if want := []int64{1, 2, 3, 4}; !sameIDs(got.IDs, want) {
		t.Fatalf("rows = %v, want %v", got.IDs, want)
	}
	if !sameIDs(got.LastDraw, []int64{1, 2, 3, 4}) {
		t.Errorf("last draw = %v, want all four rows", got.LastDraw)
	}
	if len(got.Sent) != 1 || got.Sent[0].Rules != 1 || got.Sent[0].Match != 1 {
		t.Errorf("sent = %+v, want one append carrying match 1 and the page's rules 1", got.Sent)
	}
	reads := 0
	for _, f := range got.Fetches {
		if strings.HasPrefix(f, "GET") {
			reads++
		}
	}
	if reads != 1 {
		t.Errorf(
			"reads = %d, want only the start: pushed rows need no read (%v)",
			reads,
			got.Fetches,
		)
	}
}

// A row the stream pushes while the first read is out must survive the read
// landing without it.
func TestJournalRuntimeHoldsRowsDuringALoad(t *testing.T) {
	got := runRuntime(t, "heldDuringLoad")

	if want := []int64{1, 2, 3}; !sameIDs(got.IDs, want) {
		t.Fatalf("rows = %v, want %v: the pushed row was lost", got.IDs, want)
	}
	if len(got.Results) != 1 || string(got.Results[0]) != "0" {
		t.Fatalf("reads started during the load = %s, want 0", got.Results)
	}
	if got.Held {
		t.Error("the feed still holds rows after the read landed")
	}
}

// A failed read leaves the board as it was drawn.
func TestJournalRuntimeKeepsTheBoardOnAFailedRead(t *testing.T) {
	got := runRuntime(t, "failedRead")

	if !sameIDs(got.IDs, []int64{1, 2}) {
		t.Fatalf("rows = %v, want [1 2] kept through the failed read", got.IDs)
	}
	if len(got.LastDraw) != 2 {
		t.Fatalf("last draw = %v, want the board from before the failure", got.LastDraw)
	}
	if got.Held {
		t.Error("a failed read left the feed holding, so pushed rows would pile up")
	}
	if got.Stale {
		t.Error("a failed read expired the page")
	}
}

// A push to a feed that never loaded must not fold onto an empty history.
func TestJournalRuntimeRecoversFromAFailedFirstRead(t *testing.T) {
	got := runRuntime(t, "failedFirstRead")

	if len(got.Results) != 1 || string(got.Results[0]) != "0" {
		t.Fatalf("rows after a push to an unloaded feed = %s, want 0", got.Results)
	}
	if want := []int64{1, 2, 4}; !sameIDs(got.IDs, want) {
		t.Fatalf("rows = %v, want %v once a read succeeds", got.IDs, want)
	}
	reads := 0
	for _, f := range got.Fetches {
		if strings.HasSuffix(f, "since=0") {
			reads++
		}
	}
	if reads != 3 {
		t.Errorf("reads = %d, want the start and one per push (%v)", reads, got.Fetches)
	}
	if got.Held {
		t.Error("the feed still holds rows after the read landed")
	}
}

// The feed keeps the same newest rows the server keeps, so a long-open tab and
// a reloaded one fold the same history. A trimmed row cannot return on a late
// echo.
func TestJournalRuntimeKeepsTheServerRowWindow(t *testing.T) {
	got := runRuntime(t, "trim")

	if len(got.Results) != 3 {
		t.Fatalf("results = %s, want ids, seen count and ids again", got.Results)
	}
	var ids []int64
	if err := json.Unmarshal(got.Results[0], &ids); err != nil {
		t.Fatalf("decode ids: %v", err)
	}
	if want := []int64{14, 15, 16}; !sameIDs(ids, want) {
		t.Fatalf("rows = %v, want %v", ids, want)
	}
	var seen int
	if err := json.Unmarshal(got.Results[1], &seen); err != nil {
		t.Fatalf("decode seen count: %v", err)
	}
	if seen != 3 {
		t.Errorf("seen ids = %d, want 3: trimmed ids must not stay in memory", seen)
	}
	if err := json.Unmarshal(got.Results[2], &ids); err != nil {
		t.Fatalf("decode ids after the echo: %v", err)
	}
	if want := []int64{14, 15, 16}; !sameIDs(ids, want) {
		t.Fatalf("rows = %v after a trimmed echo, want %v", ids, want)
	}
}

// A burst of pushed rows costs one reduce and one render, on the next frame.
func TestJournalRuntimeDrawsOncePerFrame(t *testing.T) {
	got := runRuntime(t, "oneDrawPerFrame")

	if len(got.Results) != 2 {
		t.Fatalf("results = %s, want the count before and after the frame", got.Results)
	}
	if string(got.Results[0]) != "0" {
		t.Errorf("draws in the same tick = %s, want 0", got.Results[0])
	}
	if string(got.Results[1]) != "1" {
		t.Errorf("draws after the frame = %s, want 1", got.Results[1])
	}
	if want := []int64{1, 2, 4, 5, 6}; !sameIDs(got.LastDraw, want) {
		t.Errorf("last draw = %v, want every row, %v", got.LastDraw, want)
	}
}

// A hidden tab parks requestAnimationFrame, so the runtime must fall back to
// a timer there. Otherwise a title or sound set in render never fires.
func TestJournalRuntimeRendersInAHiddenTab(t *testing.T) {
	got := runRuntime(t, "hiddenStillDraws")

	if len(got.Results) != 1 {
		t.Fatalf("results = %s, want the draw from the hidden tab", got.Results)
	}
	var ids []int64
	if err := json.Unmarshal(got.Results[0], &ids); err != nil {
		t.Fatalf("decode ids: %v", err)
	}
	if want := []int64{1, 2, 4}; !sameIDs(ids, want) {
		t.Fatalf("rows in the hidden draw = %v, want %v", ids, want)
	}
}

// Other rules never replace the page's own number: the feed stops, asks for
// a full reload, and refuses further writes.
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
		if id == 5 || id == 6 {
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

func TestJournalRuntimeReadsANewMatch(t *testing.T) {
	got := runRuntime(t, "newMatch")

	if got.Match != 2 {
		t.Fatalf("match = %d, want 2", got.Match)
	}
	if !sameIDs(got.IDs, []int64{6}) {
		t.Fatalf("rows = %v, want only the new match's row", got.IDs)
	}
}

func TestJournalRuntimeResyncReadsAgain(t *testing.T) {
	got := runRuntime(t, "resync")

	reads := 0
	for _, f := range got.Fetches {
		if strings.HasSuffix(f, "since=0") {
			reads++
		}
	}
	if reads != 2 {
		t.Fatalf("reads = %d, want the start plus the resync (%v)", reads, got.Fetches)
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
