package webmaster

import (
	"encoding/json"
	"testing"

	"github.com/Napam/beaverdream/lib/webmaster/internal/jsrun"
)

// stateHarness runs the real runtime's bd.state against a server whose every
// answer waits until the scenario releases it, so the test sees whether a
// call starts before the one ahead of it finished.
const stateHarness = `
const fs = require('fs')
const src = fs.readFileSync(process.argv[2], 'utf8')

const config = JSON.stringify({
  visitor: 'me', keys: {},
  mine: { coins: 2 }, tops: { coins: [{ name: 'me', id: 'me', amount: 2 }] }
})
function node() {
  return {
    textContent: '', hidden: false, attrs: {},
    setAttribute(name, value) { this.attrs[name] = value; signals.push(value) },
    getAttribute: () => null, addEventListener: () => {}, appendChild: () => {}
  }
}
const signals = []
const reports = []
const log = []
const page = node()
page.textContent = config

global.document = {
  hidden: false,
  getElementById: (id) => (id === 'bd-journal' ? page : null),
  createElement: () => node(),
  body: node(),
  documentElement: node(),
  addEventListener: () => {},
  dispatchEvent: () => {}
}
global.window = global
global.addEventListener = () => {}
Object.defineProperty(global, 'navigator', {
  value: { sendBeacon: (url, blob) => { reports.push(url); return true } },
  configurable: true
})
global.Blob = function () {}
global.CustomEvent = function () {}

// Each path answers once, in this order, when released.
const answers = {
  '/x/state/coins?add=3': [200, { ok: true, mine: { coins: 5 } }],
  '/x/state/coins?spend=9': [409, { ok: false, reason: 'short' }],
  '/x/state/buy_rod': [404, { ok: false, reason: 'gone', error: 'unknown state key' }],
  '/x/state/coins?add=1': 'offline',
  '/x/state/coins?add=2': 'hang'
}
// The runtime's abort timer fires at once, so a hung call ends in the test.
const realTimeout = setTimeout
global.setTimeout = (fn, ms) => (ms >= 5000 ? realTimeout(fn, 0) : realTimeout(fn, ms))
const waiting = []
global.fetch = (url, opts) => {
  log.push('start ' + url + ' ' + opts.method)
  return new Promise((resolve, reject) => {
    if (answers[url] === 'hang') {
      opts.signal.addEventListener('abort', () => {
        log.push('abort ' + url)
        reject(new Error('aborted'))
      })
      return
    }
    waiting.push(() => {
      log.push('end ' + url)
      const a = answers[url]
      if (a === 'offline') return reject(new TypeError('network'))
      resolve({ ok: a[0] < 300, status: a[0], json: async () => a[1] })
    })
  })
}

eval(src)

const tick = () => new Promise((r) => setImmediate(r))
;(async () => {
  const first = { mine: bd.state.mine, tops: bd.state.tops }
  const calls = [
    bd.state.add('coins', 3),
    bd.state.spend('coins', 9),
    bd.state.trade('buy_rod'),
    bd.state.add('coins')
  ]
  for (let i = 0; i < calls.length; i++) {
    await tick()
    const release = waiting.shift()
    if (waiting.length) log.push('overlap')
    release()
  }
  // A hung call times out, and the call queued behind it still runs.
  const hung = bd.state.add('coins', 2)
  answers['/x/state/coins?add=7'] = [200, { ok: true, mine: { coins: 12 } }]
  const after = bd.state.add('coins', 7)
  for (let i = 0; i < 200 && !waiting.length; i++) await new Promise((r) => realTimeout(r, 1))
  if (waiting.length) waiting.shift()()
  calls.push(hung, after)
  const results = await Promise.all(calls)
  console.log(JSON.stringify({ first, results, log, reports, signals, mine: bd.state.mine }))
})()
`

type stateRuntimeResult struct {
	First struct {
		Mine map[string]int64           `json:"mine"`
		Tops map[string]json.RawMessage `json:"tops"`
	} `json:"first"`
	Results []struct {
		OK     bool             `json:"ok"`
		Reason string           `json:"reason"`
		Mine   map[string]int64 `json:"mine"`
	} `json:"results"`
	Log     []string         `json:"log"`
	Reports []string         `json:"reports"`
	Signals []string         `json:"signals"`
	Mine    map[string]int64 `json:"mine"`
}

func TestStateRuntimeRunsCallsInOrderAndResolvesEveryRefusal(t *testing.T) {
	out := jsrun.Run(t, JournalRuntime, stateHarness, "")
	var got stateRuntimeResult
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatalf("decode: %v\n%s", err, out)
	}

	if got.First.Mine["coins"] != 2 || got.First.Tops["coins"] == nil {
		t.Errorf("before any call: %+v, want the config's mine and tops", got.First)
	}
	wantLog := []string{
		"start /x/state/coins?add=3 POST", "end /x/state/coins?add=3",
		"start /x/state/coins?spend=9 POST", "end /x/state/coins?spend=9",
		"start /x/state/buy_rod POST", "end /x/state/buy_rod",
		"start /x/state/coins?add=1 POST", "end /x/state/coins?add=1",
		"start /x/state/coins?add=2 POST", "abort /x/state/coins?add=2",
		"start /x/state/coins?add=7 POST", "end /x/state/coins?add=7",
	}
	if len(got.Log) != len(wantLog) {
		t.Fatalf("log = %q, want one call at a time: %q", got.Log, wantLog)
	}
	for i := range wantLog {
		if got.Log[i] != wantLog[i] {
			t.Fatalf("log = %q, want %q", got.Log, wantLog)
		}
	}

	if len(got.Results) != 6 {
		t.Fatalf("results = %+v", got.Results)
	}
	if !got.Results[0].OK || got.Results[0].Mine["coins"] != 5 {
		t.Errorf("add = %+v, want ok with 5 coins", got.Results[0])
	}
	for i, reason := range []string{"short", "gone", "slow", "slow"} {
		if r := got.Results[i+1]; r.OK || r.Reason != reason {
			t.Errorf("call %d = %+v, want refused as %s", i+2, r, reason)
		}
	}
	if len(got.Reports) != 0 {
		t.Errorf("a refusal reached the error beacon: %q", got.Reports)
	}
	if !got.Results[5].OK || got.Results[5].Mine["coins"] != 12 {
		t.Errorf("the call after the hung one = %+v, want ok", got.Results[5])
	}
	// The stream owns $mine. An answer may be older than a push from another
	// tab, so answers never write it.
	if got.Mine["coins"] != 2 || len(got.Signals) != 0 {
		t.Errorf("bd.state.mine = %v, signals = %q: an answer wrote the page's counts",
			got.Mine, got.Signals)
	}
}
