package webmaster

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/Napam/beaverdream/lib/webmaster/internal/jsrun"
)

// exampleMotion is the motion half of the journal example in journalPrompt
// (changeset.go). exampleRun in the harness runs these lines. The drift test in
// journal_test.go checks that the prompt contains this block whole, so change
// both together.
const exampleMotion = `var me = { x: 50, y: 50, look: 'hero' }  // fixed spawn, never random
var pos = {}, ready = false
function inBounds(x, y) {
  return isFinite(x) && isFinite(y) && x >= 0 && y >= 0 && x <= 100 && y <= 100
}

moves.start(function (rows) {
  var p = {}
  for (var i = 0; i < rows.length; i++) {
    var r = rows[i], x = Number(r.payload.x), y = Number(r.payload.y)
    if (!inBounds(x, y)) continue                    // the referee: in bounds only
    p[r.visitor] = { x: x, y: y, look: String(r.payload.look || '') }  // draw them as they are
  }
  return p
}, function (p) {
  pos = p
  if (!ready) {                                  // first render: put the visitor back
    var back = p[bd.visitor] || bd.load('spot', null)  // newest row, else this browser's save
    if (back && inBounds(Number(back.x), Number(back.y))) {
      me.x = Number(back.x); me.y = Number(back.y)
    }
    ready = true
  }
})

setInterval(function () {
  if (!ready) return
  moves.append({ x: me.x, y: me.y, look: me.look })
  bd.save('spot', me)
}, 60)
`

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
const pagehide = []
global.addEventListener = (name, fn) => { if (name === 'pagehide') pagehide.push(fn) }
global.dispatchPagehide = () => pagehide.forEach((fn) => fn())
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
// This browser's storage. Node has a localStorage of its own that warns on
// stdout, so the fake replaces it.
const stored = {}
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

const row = (id, match, rules) => ({
  id: id, key: 'moves', visitor: 'them', payload: { n: id }, match: match || 1, rules: rules || 1
})
const badRow = (id) => ({ ...row(id), payload: { boom: true } })
const cutRow = (id) => ({
  id: id, key: 'cut', visitor: 'them', payload: { n: id }, match: 1, rules: 1
})

// state lets a scenario change the server's answers between steps. hold
// parks the next read until the scenario releases it. extra adds rows to the
// default read, the way a row written before a push is in the next read.
const state = { read: 'ok', post: 200, postError: '', postMatch: 1, hold: null, extra: [] }
function server(url, method) {
  if (method === 'POST') {
    if (state.post === 200) return reply(200, { id: 3, key: 'moves', visitor: 'me', payload: { n: 1 }, match: state.postMatch, rules: 1 })
    return reply(state.post, { error: state.postError })
  }
  if (state.read === 'fail') return reply(500, { error: 'could not read that' })
  if (state.read === 'rules') return reply(200, { match: 1, rules: 2, rows: [row(5, 1, 2)] })
  if (state.read === 'match') return reply(200, { match: 2, rules: 1, rows: [row(6, 2)] })
  if (state.read === 'own') return reply(200, { match: 1, rules: 1, rows: [{ id: 1, key: 'moves', visitor: 'me', payload: { x: 20, y: 70 }, match: 1, rules: 1 }] })
  if (url.indexOf('/journal/cut') >= 0) return reply(200, { match: 1, rules: 1, rows: [cutRow(1), cutRow(2)] })
  return reply(200, { match: 1, rules: 1, rows: [row(1), row(2), ...state.extra] })
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

// The runtime reads every declared key while it loads, before any step runs,
// so a scenario that needs a failed or held first read sets it here. hold is
// shared with the steps that release it.
const hold = {}
const beforeInit = {
  failedFirstRead: () => { state.read = 'fail' },
  failedPreloadResync: () => { state.read = 'fail' },
  failedFirstReadRetry: () => { state.read = 'fail' },
  heldDuringLoad: () => { state.hold = hold },
  appendDuringLoad: () => { state.hold = hold },
  clearedFeed: () => { state.read = 'match' },
  exampleTickFirst: () => { state.read = 'own'; state.hold = hold },
  exampleReadFirst: () => { state.read = 'own' },
  exampleFallback: () => { state.hold = hold },
  exampleFromSave: () => { stored['bd:save:spot'] = '{"x":10,"y":30}' },
  exampleRowBeatsSave: () => { state.read = 'own'; stored['bd:save:spot'] = '{"x":10,"y":30}' },
  exampleBadSave: () => { stored['bd:save:spot'] = '{"x":500,"y":30}' }
}
if (beforeInit[scenario]) beforeInit[scenario]()

eval(src)

const settle = () => new Promise((resolve) => setTimeout(resolve, 20))
const movesReads = () => fetches.filter((f) => f === 'GET /journal/moves?since=0').length
const cutReads = () => fetches.filter((f) => f === 'GET /journal/cut?since=0').length
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
// exampleRun runs the motion half of the journal example. The lines come from
// exampleMotion, so the page's code is the same text the prompt shows. It
// returns the tick and the page's variables instead of setting a timer, so a
// step fires the tick when it wants.
function exampleRun() {
  const moves = feed
  const ticks = []
  global.setInterval = (fn) => { ticks.push(fn) }
` + exampleMotion + `
  return { tick: ticks[0], me: () => me, pos: () => pos }
}

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
  // the read does not include it. start() takes the read taken at load, which
  // is still in flight, so the row waits for it.
  heldDuringLoad: async () => {
    start()
    const before = fetches.length
    window.bd._rows([row(3)])
    results.push(fetches.length - before)
    hold.release()
    await settle()
  },
  // A row pushed while a reload is out is held the same way.
  heldDuringReload: async () => {
    start()
    await settle()
    const read = {}
    state.hold = read
    const reload = feed.load()
    window.bd._rows([row(3)])
    const before = fetches.length
    await feed.load()
    results.push(fetches.length - before)
    read.release()
    await reload
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
  // A pushed row from an older match that the page never read means the feed
  // was cleared and started over, so the feed reads itself again.
  clearedFeed: async () => {
    start()
    await settle()
    state.read = 'ok'
    window.bd._rows([row(7, 1)])
    await settle()
  },
  // A late echo of a row the page already read is not a clear.
  oldMatchEcho: async () => {
    start()
    await settle()
    feed.match = 2
    window.bd._rows([row(2, 1)])
    await settle()
  },
  // A resync reads every started feed again.
  resync: async () => {
    start()
    await settle()
    window.bd._resync()
    await settle()
  },
  // The read taken at load is the only read before start, and start takes it
  // instead of reading again. Every declared key is read at load.
  earlyRead: async () => {
    await settle()
    results.push(movesReads())
    results.push(cutReads())
    start()
    await settle()
    results.push(movesReads())
  },
  // A row pushed before start stays out of the feed until start. The row was
  // written after the load read, so start reads again and draws it once.
  rowBeforeStart: async () => {
    state.extra = [row(3)]
    await settle()
    window.bd._rows([row(3)])
    await settle()
    results.push(feed.rows.length)
    results.push(draws.length)
    start()
    await settle()
    results.push(movesReads())
  },
  // A load read that failed leaves the feed unloaded. A resync reads again
  // and draws the board.
  failedPreloadResync: async () => {
    start()
    await settle()
    results.push(feed.rows.length)
    state.read = 'ok'
    window.bd._resync()
    await settle()
  },
  // A first read that failed is tried once more after a second, with no push
  // or resync needed. The retry runs after the server recovers.
  failedFirstReadRetry: async () => {
    start()
    await settle()
    results.push(feed.rows.length)
    state.read = 'ok'
    await new Promise((resolve) => setTimeout(resolve, 1200))
    await settle()
  },
  // A resync before start means the read taken at load may predate a clear.
  // The server now has another match, so start must read it again.
  resyncBeforeStart: async () => {
    await settle()
    state.read = 'match'
    window.bd._resync()
    start()
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
  },
  // The page's own append resolves while a read is out. It waits in held like
  // any pushed row, survives the read landing without it, and is folded once.
  appendDuringLoad: async () => {
    start()
    results.push(await feed.append({ n: 1 }))
    hold.release()
    await settle()
  },
  // The same append while a reload is out, after the feed has loaded.
  appendDuringReload: async () => {
    start()
    await settle()
    const read = {}
    state.hold = read
    const reload = feed.load()
    results.push(await feed.append({ n: 1 }))
    read.release()
    await reload
    await settle()
  },
  // newMatch sends the next number. Its answer opens the new match, so the
  // feed reads the new one whole instead of folding the row onto the old
  // board.
  newMatchAppend: async () => {
    start()
    await settle()
    state.read = 'match'
    state.postMatch = 2
    results.push(await feed.newMatch({}))
    await settle()
  },
  // A row that makes reduce throw reports once and leaves the last draw. A new
  // match drops the row, so the feed recovers.
  reduceThrows: async () => {
    feed.start(
      (rows) => {
        for (const r of rows) if (r.payload && r.payload.boom) throw new Error('boom')
        return rows.map((r) => r.id)
      },
      (s) => { draws.push(s) }
    )
    await settle()
    window.bd._rows([badRow(5)])
    await settle()
    results.push(draws.length)
    state.read = 'match'
    window.bd._rows([row(6, 2)])
    await settle()
  },
  // A rules change stops only the key whose page code is now wrong. The other
  // key keeps folding until the live script reloads the page.
  oneKeyStale: async () => {
    start()
    startCut()
    await settle()
    window.bd._rows([row(5, 1, 2)])
    window.bd._rows([cutRow(11)])
    await settle()
    results.push([feed.stale, cut.stale])
    results.push(cut.rows.map((r) => r.id))
  },
  // A feed started without render would never draw, so it is reported instead
  // of failing silently.
  startWithoutRender: async () => {
    feed.start((rows) => rows.map((r) => r.id))
    await settle()
  },
  // The first tick lands while the first read is out, the order a slow
  // network gives. The page has not rendered, so that tick sends nothing.
  exampleTickFirst: async () => {
    const run = exampleRun()
    run.tick()
    await settle()
    hold.release()
    await settle()
    run.tick()
    await settle()
    results.push({ me: run.me(), own: run.pos().me || null })
  },
  // The read lands before the tick. The first render has already restored the
  // visitor, so the tick sends the restored position.
  exampleReadFirst: async () => {
    const run = exampleRun()
    await settle()
    run.tick()
    await settle()
    results.push({ me: run.me(), own: run.pos().me || null })
  },
  // No row for the viewer: the first render keeps the spawn, and the tick
  // sends it only after that render.
  exampleFallback: async () => {
    const run = exampleRun()
    run.tick()
    await settle()
    hold.release()
    await settle()
    run.tick()
    await settle()
    results.push({ me: run.me(), own: run.pos().me || null })
  },
  // The feed has no row of the viewer's, so the first render takes the spot
  // this browser saved, when it is in bounds. The tick saves where they are.
  exampleFromSave: async () => {
    const run = exampleRun()
    await settle()
    run.tick()
    await settle()
    window.dispatchPagehide()
    results.push({ me: run.me(), own: run.pos().me || null, saved: JSON.parse(stored['bd:save:spot']) })
  },
  exampleRowBeatsSave: async () => {
    const run = exampleRun()
    await settle()
    run.tick()
    await settle()
    results.push({ me: run.me(), own: run.pos().me || null })
  },
  exampleBadSave: async () => {
    const run = exampleRun()
    await settle()
    run.tick()
    await settle()
    results.push({ me: run.me(), own: run.pos().me || null })
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
	Match   int             `json:"match"`
	Rules   int             `json:"rules"`
	Payload json.RawMessage `json:"payload"`
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
	out := jsrun.Run(t, JournalRuntime, runtimeHarness, scenario)
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

// readsOf counts the feed reads of one key in the requests the harness saw.
func readsOf(fetches []string, key string) int {
	n := 0
	for _, f := range fetches {
		if f == "GET /journal/"+key+"?since=0" {
			n++
		}
	}
	return n
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
	if reads := readsOf(got.Fetches, "moves"); reads != 1 {
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

// A row the stream pushes while a reload is out must survive the reload
// landing without it.
func TestJournalRuntimeHoldsRowsDuringAReload(t *testing.T) {
	got := runRuntime(t, "heldDuringReload")

	if want := []int64{1, 2, 3}; !sameIDs(got.IDs, want) {
		t.Fatalf("rows = %v, want %v: the pushed row was lost", got.IDs, want)
	}
	if len(got.Results) != 1 || string(got.Results[0]) != "0" {
		t.Fatalf("reads started during the reload = %s, want 0", got.Results)
	}
	if got.Held {
		t.Error("the feed still holds rows after the reload landed")
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
	if reads := readsOf(got.Fetches, "moves"); reads != 3 {
		t.Errorf("reads = %d, want the load and one per push (%v)", reads, got.Fetches)
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

// An append that resolves while the first read is out must wait in held like a
// pushed row: the read may not include it, and it must not appear twice.
func TestJournalRuntimeKeepsAnAppendThroughALoad(t *testing.T) {
	got := runRuntime(t, "appendDuringLoad")

	if want := []int64{1, 2, 3}; !sameIDs(got.IDs, want) {
		t.Fatalf("rows = %v, want %v: the append was lost or doubled", got.IDs, want)
	}
	if got.Held {
		t.Error("the feed still holds rows after the read landed")
	}
	if reads := readsOf(got.Fetches, "moves"); reads != 1 {
		t.Errorf(
			"reads = %d, want only the read taken at load, which the append waited in (%v)",
			reads, got.Fetches)
	}
}

// An append that resolves while a reload is out must wait in held like a
// pushed row: the read may not include it, and it must not appear twice.
func TestJournalRuntimeKeepsAnAppendThroughAReload(t *testing.T) {
	got := runRuntime(t, "appendDuringReload")

	if want := []int64{1, 2, 3}; !sameIDs(got.IDs, want) {
		t.Fatalf("rows = %v, want %v: the append was lost or doubled", got.IDs, want)
	}
	if got.Held {
		t.Error("the feed still holds rows after the reload landed")
	}
	if reads := readsOf(got.Fetches, "moves"); reads != 2 {
		t.Errorf(
			"reads = %d, want the init read and the one the append landed in (%v)",
			reads, got.Fetches)
	}
}

// newMatch sends the next match number. The stream's echo opens that match,
// so the feed reloads instead of folding the new row onto the old board.
func TestJournalRuntimeOpensTheNextMatch(t *testing.T) {
	got := runRuntime(t, "newMatchAppend")

	if len(got.Sent) != 1 || got.Sent[0].Match != 2 {
		t.Fatalf("sent = %+v, want one append asking for match 2", got.Sent)
	}
	if got.Match != 2 {
		t.Fatalf("match = %d, want 2", got.Match)
	}
	if !sameIDs(got.IDs, []int64{6}) {
		t.Fatalf("rows = %v, want only the new match's row", got.IDs)
	}
}

// A row that makes reduce throw is reported once and leaves the last draw.
// The next match drops it, so the feed recovers.
func TestJournalRuntimeSurvivesAThrowingReduce(t *testing.T) {
	got := runRuntime(t, "reduceThrows")

	if len(got.Results) != 1 || string(got.Results[0]) != "1" {
		t.Fatalf("draws after the bad row = %s, want 1: the bad row must not redraw", got.Results)
	}
	if len(got.Reports) != 1 || !strings.Contains(got.Reports[0].Message, "reduce(moves): boom") {
		t.Fatalf("reports = %+v, want one reduce failure", got.Reports)
	}
	if want := []int64{6}; !sameIDs(got.LastDraw, want) {
		t.Errorf("last draw = %v, want %v once the new match drops the row", got.LastDraw, want)
	}
	if got.Unhandled != 0 {
		t.Errorf("unhandled rejections = %d, want 0", got.Unhandled)
	}
}

// New rules expire only the feed whose reducer is now wrong. The other key
// keeps folding until the live script reloads the page.
func TestJournalRuntimeStopsOnlyTheStaleKey(t *testing.T) {
	got := runRuntime(t, "oneKeyStale")

	if len(got.Results) != 2 {
		t.Fatalf("results = %s, want both stale flags and the other key's rows", got.Results)
	}
	var stale []bool
	if err := json.Unmarshal(got.Results[0], &stale); err != nil {
		t.Fatalf("decode stale flags: %v", err)
	}
	if len(stale) != 2 || !stale[0] || stale[1] {
		t.Fatalf("stale flags = %v, want the changed key stale and the other live", stale)
	}
	var ids []int64
	if err := json.Unmarshal(got.Results[1], &ids); err != nil {
		t.Fatalf("decode ids: %v", err)
	}
	if want := []int64{1, 2, 11}; !sameIDs(ids, want) {
		t.Fatalf("other key's rows = %v, want %v", ids, want)
	}
}

// A feed started without a render function never draws, so the runtime
// reports it instead of failing silently.
func TestJournalRuntimeReportsAStartWithoutRender(t *testing.T) {
	got := runRuntime(t, "startWithoutRender")

	if len(got.Reports) != 1 ||
		!strings.Contains(got.Reports[0].Message, "render is not a function") {
		t.Fatalf("reports = %+v, want one about the missing render", got.Reports)
	}
	if got.Draws != 0 {
		t.Errorf("draws = %d, want 0 without a render", got.Draws)
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

// An admin clear sends the key back to match 1. A page that was only
// watching a later match reads the feed again when the first new row arrives,
// instead of dropping it.
func TestJournalRuntimeReadsAClearedFeed(t *testing.T) {
	got := runRuntime(t, "clearedFeed")

	if got.Match != 1 {
		t.Fatalf("match = %d, want 1 after the clear", got.Match)
	}
	if !sameIDs(got.IDs, []int64{1, 2}) {
		t.Fatalf("rows = %v, want the cleared feed's rows", got.IDs)
	}
}

func TestJournalRuntimeIgnoresALateEchoOfAnOldMatch(t *testing.T) {
	got := runRuntime(t, "oldMatchEcho")

	if reads := readsOf(got.Fetches, "moves"); reads != 1 {
		t.Fatalf("reads = %d, want only the init read (%v)", reads, got.Fetches)
	}
}

func TestJournalRuntimeResyncReadsAgain(t *testing.T) {
	got := runRuntime(t, "resync")

	if reads := readsOf(got.Fetches, "moves"); reads != 2 {
		t.Fatalf("reads = %d, want the init read plus the resync (%v)", reads, got.Fetches)
	}
}

// Every declared key is read when the runtime loads. start() takes that read
// for its feed, so starting makes no second request.
func TestJournalRuntimeStartTakesTheLoadRead(t *testing.T) {
	got := runRuntime(t, "earlyRead")

	if len(got.Results) != 3 {
		t.Fatalf("results = %s, want the reads before start for both keys, then after", got.Results)
	}
	if string(got.Results[0]) != "1" || string(got.Results[1]) != "1" {
		t.Errorf(
			"reads before start = %s %s, want 1 for each declared key",
			got.Results[0],
			got.Results[1],
		)
	}
	if string(got.Results[2]) != "1" {
		t.Errorf("reads after start = %s, want 1: start must not read again", got.Results[2])
	}
	if want := []int64{1, 2}; !sameIDs(got.LastDraw, want) {
		t.Errorf("last draw = %v, want the loaded rows %v", got.LastDraw, want)
	}
}

// A row pushed before start is kept out of the feed until start. That row was
// written after the load read, so start reads again rather than use it.
func TestJournalRuntimeHoldsARowPushedBeforeStart(t *testing.T) {
	got := runRuntime(t, "rowBeforeStart")

	if len(got.Results) != 3 {
		t.Fatalf("results = %s, want the rows and draws before start, then the reads", got.Results)
	}
	if string(got.Results[0]) != "0" || string(got.Results[1]) != "0" {
		t.Errorf(
			"rows and draws before start = %s %s, want 0 and 0",
			got.Results[0],
			got.Results[1],
		)
	}
	if string(got.Results[2]) != "2" {
		t.Errorf("reads = %s, want 2: the load read and the read at start", got.Results[2])
	}
	if want := []int64{1, 2, 3}; !sameIDs(got.IDs, want) || !sameIDs(got.LastDraw, want) {
		t.Errorf("rows = %v, last draw = %v, want %v once start draws", got.IDs, got.LastDraw, want)
	}
}

// A load read that failed leaves the feed unloaded. A resync reads again, and
// the failure is neither reported nor an unhandled rejection.
func TestJournalRuntimeResyncRecoversAFailedLoadRead(t *testing.T) {
	got := runRuntime(t, "failedPreloadResync")

	if len(got.Results) != 1 || string(got.Results[0]) != "0" {
		t.Fatalf("rows after the failed load read = %s, want 0", got.Results)
	}
	if want := []int64{1, 2}; !sameIDs(got.IDs, want) || !sameIDs(got.LastDraw, want) {
		t.Fatalf("rows = %v, last draw = %v, want %v after the resync", got.IDs, got.LastDraw, want)
	}
	if reads := readsOf(got.Fetches, "moves"); reads != 2 {
		t.Errorf("reads = %d, want the failed load read and the resync (%v)", reads, got.Fetches)
	}
	if got.Unhandled != 0 || len(got.Reports) != 0 {
		t.Errorf(
			"unhandled = %d, reports = %+v, want a failed load read to stay quiet",
			got.Unhandled,
			got.Reports,
		)
	}
	if got.Held {
		t.Error("the feed still holds rows after the resync")
	}
}

// A first read that failed is tried once more after a second, so a page that
// has written nothing yet still loads on a quiet board.
func TestJournalRuntimeRetriesAFailedFirstReadOnce(t *testing.T) {
	got := runRuntime(t, "failedFirstReadRetry")

	if len(got.Results) != 1 || string(got.Results[0]) != "0" {
		t.Fatalf("rows after the failed first read = %s, want 0", got.Results)
	}
	if want := []int64{1, 2}; !sameIDs(got.IDs, want) || !sameIDs(got.LastDraw, want) {
		t.Fatalf("rows = %v, last draw = %v, want %v after the retry", got.IDs, got.LastDraw, want)
	}
	if reads := readsOf(got.Fetches, "moves"); reads != 2 {
		t.Errorf(
			"reads = %d, want the failed first read and its one retry (%v)",
			reads,
			got.Fetches,
		)
	}
}

// A resync before start means the read taken at load may predate a clear or
// a reset, so start reads again and draws what the server now has.
func TestJournalRuntimeReadsAgainAfterAResyncBeforeStart(t *testing.T) {
	got := runRuntime(t, "resyncBeforeStart")

	if reads := readsOf(got.Fetches, "moves"); reads != 2 {
		t.Fatalf("reads = %d, want the load read and the read at start (%v)", reads, got.Fetches)
	}
	if got.Match != 2 || !sameIDs(got.IDs, []int64{6}) {
		t.Errorf("match = %d, rows = %v, want match 2 with only row 6", got.Match, got.IDs)
	}
}

// A rematch reads the new match. The load read is used once, by start.
func TestJournalRuntimeRematchReadsAgain(t *testing.T) {
	got := runRuntime(t, "newMatch")

	if reads := readsOf(got.Fetches, "moves"); reads != 2 {
		t.Fatalf("reads = %d, want the load read and the rematch read (%v)", reads, got.Fetches)
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
	if loads := readsOf(got.Fetches, "moves"); loads != 2 {
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

type examplePoint struct{ X, Y float64 }

// exampleSent decodes the positions the page appended, oldest first.
func exampleSent(t *testing.T, got runtimeResult) []examplePoint {
	t.Helper()
	var out []examplePoint
	for _, s := range got.Sent {
		var p examplePoint
		if err := json.Unmarshal(s.Payload, &p); err != nil {
			t.Fatalf("decode payload: %v", err)
		}
		out = append(out, p)
	}
	return out
}

// examplePage decodes the page's own position and the viewer's position on the
// feed. own is nil when the feed has no row for the viewer.
func examplePage(t *testing.T, got runtimeResult) (examplePoint, *examplePoint) {
	t.Helper()
	if len(got.Results) != 1 {
		t.Fatalf("results = %s, want the page's position", got.Results)
	}
	var page struct {
		Me  examplePoint  `json:"me"`
		Own *examplePoint `json:"own"`
	}
	if err := json.Unmarshal(got.Results[0], &page); err != nil {
		t.Fatalf("decode page: %v", err)
	}
	return page.Me, page.Own
}

// The example restores the viewer from their own row on the first render. The
// tick before that render sends nothing, so the only append carries the
// restored position, whichever order the read and the tick land in.
func TestJournalExampleRestoresTheVisitor(t *testing.T) {
	want := examplePoint{X: 20, Y: 70}
	for _, scenario := range []string{"exampleTickFirst", "exampleReadFirst"} {
		t.Run(scenario, func(t *testing.T) {
			got := runRuntime(t, scenario)

			if sent := exampleSent(t, got); len(sent) != 1 || sent[0] != want {
				t.Errorf("sent = %+v, want one append at the restored %+v", sent, want)
			}
			me, own := examplePage(t, got)
			if me != want {
				t.Errorf("me = %+v after the first render, want the restored %+v", me, want)
			}
			if own == nil || *own != want {
				t.Errorf("the viewer's row = %+v, want %+v on the feed", own, want)
			}
		})
	}
}

// With no row for the viewer, the first render keeps the spawn and sending
// starts after it.
func TestJournalExampleFallsBackToSpawn(t *testing.T) {
	spawn := examplePoint{X: 50, Y: 50}
	got := runRuntime(t, "exampleFallback")

	if sent := exampleSent(t, got); len(sent) != 1 || sent[0] != spawn {
		t.Errorf("sent = %+v, want one append at the spawn %+v", sent, spawn)
	}
	me, own := examplePage(t, got)
	if me != spawn {
		t.Errorf("me = %+v, want the spawn %+v", me, spawn)
	}
	if own != nil {
		t.Errorf("the feed has a row for the viewer %+v, want none", own)
	}
}

// Every motion row carries how the player looks, so the other pages draw them
// as they chose to be and not as a default figure.
func TestJournalExampleSendsTheLook(t *testing.T) {
	got := runRuntime(t, "exampleFallback")
	if len(got.Sent) != 1 {
		t.Fatalf("sent %d rows, want one", len(got.Sent))
	}
	var payload struct {
		Look string `json:"look"`
	}
	if err := json.Unmarshal(got.Sent[0].Payload, &payload); err != nil {
		t.Fatalf("decode payload: %v", err)
	}
	if payload.Look == "" {
		t.Errorf("payload %s carries no look", got.Sent[0].Payload)
	}
}

// With no row of the viewer's in the feed, the example restores the spot this
// browser saved, and keeps saving where the viewer is. A row in the feed is
// newer than any save, so it wins, and a saved spot out of bounds is ignored.
func TestJournalExampleRestoresFromTheSave(t *testing.T) {
	cases := []struct {
		scenario string
		want     examplePoint
	}{
		{"exampleFromSave", examplePoint{X: 10, Y: 30}},
		{"exampleRowBeatsSave", examplePoint{X: 20, Y: 70}},
		{"exampleBadSave", examplePoint{X: 50, Y: 50}},
	}
	for _, c := range cases {
		t.Run(c.scenario, func(t *testing.T) {
			got := runRuntime(t, c.scenario)
			if sent := exampleSent(t, got); len(sent) != 1 || sent[0] != c.want {
				t.Errorf("sent = %+v, want one append at %+v", sent, c.want)
			}
			me, _ := examplePage(t, got)
			if me != c.want {
				t.Errorf("me = %+v, want %+v", me, c.want)
			}
			if len(got.Reports) != 0 {
				t.Errorf("reports = %+v, want none", got.Reports)
			}
		})
	}
	got := runRuntime(t, "exampleFromSave")
	var page struct {
		Saved examplePoint `json:"saved"`
	}
	if err := json.Unmarshal(got.Results[0], &page); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if page.Saved != (examplePoint{X: 10, Y: 30}) {
		t.Errorf("saved = %+v after pagehide, want where the viewer is", page.Saved)
	}
}
