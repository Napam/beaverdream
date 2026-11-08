package webmaster

import (
	"encoding/json"
	"strconv"
	"testing"

	"github.com/Napam/beaverdream/lib/webmaster/internal/jsrun"
)

// saveHarness runs the real runtime's bd.save and bd.load against a fake
// storage. The scenario picks how the storage behaves; timers are held so the
// test decides when the coalesced write runs.
var saveHarness = `
const fs = require('fs')
const src = fs.readFileSync(process.argv[2], 'utf8')
const scenario = process.argv[3]

function node() {
  return {
    textContent: '{"visitor":"me","keys":{}}', setAttribute: () => {},
    getAttribute: () => null, addEventListener: () => {}, appendChild: () => {}
  }
}
const reports = []
const handlers = {}
global.document = {
  hidden: false,
  getElementById: () => node(),
  documentElement: node(),
  addEventListener: (name, fn) => { handlers['doc:' + name] = fn },
  dispatchEvent: () => {}
}
global.window = global
global.addEventListener = (name, fn) => { handlers[name] = fn }
Object.defineProperty(global, 'navigator', {
  value: { sendBeacon: (url, blob) => { reports.push(blob.body.message); return true } },
  configurable: true
})
global.Blob = function (parts) { this.body = JSON.parse(parts[0]) }
global.CustomEvent = function () {}
global.fetch = () => new Promise(() => {})

const timers = []
global.setTimeout = (fn) => { timers.push(fn); return timers.length }
global.clearTimeout = () => {}
const runTimers = () => { while (timers.length) timers.shift()() }

const stored = { 'bd:save:old': '{"x":4}', 'bd:save:broken': '{"x":', 'other': 'theirs' }
const writes = []
const fine = {
  getItem: (k) => (k in stored ? stored[k] : null),
  setItem: (k, v) => { writes.push(k); stored[k] = String(v) },
  removeItem: (k) => { writes.push('-' + k); delete stored[k] }
}
const kinds = {
  fine: fine,
  // Private mode or a full disk: every call throws.
  throwing: {
    getItem: () => { throw new Error('denied') },
    setItem: () => { throw new Error('full') },
    removeItem: () => { throw new Error('denied') }
  }
}
// Some browsers throw on the localStorage property itself.
if (scenario === 'blocked') {
  Object.defineProperty(global, 'localStorage', { get: () => { throw new Error('blocked') }, configurable: true })
} else {
  Object.defineProperty(global, 'localStorage', { value: kinds[scenario], configurable: true })
}

let thrown = null
const out = {}
try {
  eval(src)
  const bd = window.bd
  out.old = bd.load('old', null)
  out.broken = bd.load('broken', 'fallback')
  out.missing = bd.load('missing', 'fallback')

  for (let i = 0; i < 100; i++) bd.save('spot', { x: i })
  out.beforeFlush = stored['bd:save:spot'] || null
  out.loadBeforeFlush = bd.load('spot', null)
  const copy = bd.load('spot', null)
  if (copy) copy.x = -1
  out.loadAfterEdit = bd.load('spot', null)
  runTimers()
  out.writesAfterBurst = writes.length

  bd.save('spot', { x: 200 })
  handlers.pagehide()
  out.afterPagehide = stored['bd:save:spot'] || null

  const cycle = {}
  cycle.self = cycle
  out.cycle = [bd.save('cyc', cycle), bd.save('cyc', cycle), bd.save('fn', () => 1)]
  out.long = bd.save('long', 'x'.repeat(` + strconv.Itoa(maxSaveLen) + ` + 1))
  out.longLoad = bd.load('long', 'none')

  bd.save('old', null)
  out.forgotten = bd.load('old', 'gone')
  handlers.pagehide()
  out.oldStored = 'bd:save:old' in stored
  out.other = stored.other
} catch (e) {
  thrown = String(e && e.message)
}
out.thrown = thrown
out.reports = reports
console.log(JSON.stringify(out))
`

type saveResult struct {
	Old              map[string]float64 `json:"old"`
	Broken           string             `json:"broken"`
	Missing          string             `json:"missing"`
	BeforeFlush      *string            `json:"beforeFlush"`
	LoadBeforeFlush  map[string]float64 `json:"loadBeforeFlush"`
	LoadAfterEdit    map[string]float64 `json:"loadAfterEdit"`
	WritesAfterBurst int                `json:"writesAfterBurst"`
	AfterPagehide    *string            `json:"afterPagehide"`
	Cycle            []bool             `json:"cycle"`
	Long             bool               `json:"long"`
	LongLoad         string             `json:"longLoad"`
	Forgotten        string             `json:"forgotten"`
	OldStored        bool               `json:"oldStored"`
	Other            string             `json:"other"`
	Thrown           *string            `json:"thrown"`
	Reports          []string           `json:"reports"`
}

func runSave(t *testing.T, scenario string) saveResult {
	t.Helper()
	out := jsrun.Run(t, JournalRuntime, saveHarness, scenario)
	var got saveResult
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatalf("decode: %v\n%s", err, out)
	}
	return got
}

// Saves coalesce into one write, flush when the page hides, load back as
// copies, and never touch keys the page does not own. A stored value that
// does not parse is the fallback.
func TestSaveKeepsValuesInTheBrowser(t *testing.T) {
	got := runSave(t, "fine")
	if got.Thrown != nil {
		t.Fatalf("threw %q", *got.Thrown)
	}
	if got.Old["x"] != 4 || got.Broken != "fallback" || got.Missing != "fallback" {
		t.Errorf("load old = %v, broken = %q, missing = %q", got.Old, got.Broken, got.Missing)
	}
	if got.BeforeFlush != nil {
		t.Errorf("a save reached storage before its timer: %q", *got.BeforeFlush)
	}
	if got.LoadBeforeFlush["x"] != 99 || got.LoadAfterEdit["x"] != 99 {
		t.Errorf("load before the flush = %v, after editing a copy = %v, want x 99",
			got.LoadBeforeFlush, got.LoadAfterEdit)
	}
	if got.WritesAfterBurst != 1 {
		t.Errorf("100 saves wrote %d times, want once", got.WritesAfterBurst)
	}
	if got.AfterPagehide == nil || *got.AfterPagehide != `{"x":200}` {
		t.Errorf("stored after pagehide = %v, want the newest save", got.AfterPagehide)
	}
	if got.Forgotten != "gone" || got.OldStored {
		t.Errorf("after save(null): load = %q, still stored = %v", got.Forgotten, got.OldStored)
	}
	if got.Other != "theirs" {
		t.Errorf("a key outside the page's prefix changed: %q", got.Other)
	}
}

// A value that is not JSON, or too long, is refused and reported once per
// name, and the old value stays.
func TestSaveRefusesValuesItCannotKeep(t *testing.T) {
	got := runSave(t, "fine")
	if len(got.Cycle) != 3 || got.Cycle[0] || got.Cycle[1] || got.Cycle[2] {
		t.Errorf("saves of a cycle and a function = %v, want all false", got.Cycle)
	}
	if got.Long || got.LongLoad != "none" {
		t.Errorf("a long save = %v, load = %q, want refused", got.Long, got.LongLoad)
	}
	if len(got.Reports) != 3 {
		t.Errorf("reports = %q, want one each for cyc, fn and long", got.Reports)
	}
}

// Storage that throws, or a localStorage property that throws, never breaks
// the page: saves still load back in this tab.
func TestSaveSurvivesBrokenStorage(t *testing.T) {
	for _, scenario := range []string{"throwing", "blocked"} {
		t.Run(scenario, func(t *testing.T) {
			got := runSave(t, scenario)
			if got.Thrown != nil {
				t.Fatalf("threw %q", *got.Thrown)
			}
			if got.Old != nil || got.Missing != "fallback" {
				t.Errorf("load old = %v, missing = %q, want fallbacks", got.Old, got.Missing)
			}
			if got.LoadAfterEdit["x"] != 99 || got.Forgotten != "gone" {
				t.Errorf("in-tab load = %v, forgotten = %q", got.LoadAfterEdit, got.Forgotten)
			}
			if len(got.Reports) != 3 {
				t.Errorf("reports = %q, want only the three refused values", got.Reports)
			}
		})
	}
}
