package webmaster

import (
	"bytes"
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

const (
	maxJournalKeys    = 6
	maxJournalName    = 32
	maxJournalRows    = 1000
	defaultJournalMax = 300
	maxJournalPayload = 600

	// maxSaveLen is the longest JSON text bd.save keeps for one name. The
	// browser gives an origin a few megabytes, shared by every name.
	maxSaveLen = 8192

	// JournalBurst and JournalRate are one visitor's write allowance for one
	// key: a burst of tokens, then a refill at JournalRate per second. The
	// prompt quotes both, so they live here with the rest of the contract.
	JournalBurst = 30
	JournalRate  = 20
)

// JournalKey is one model-declared append-only log. Max bounds how many rows
// are kept; Rules is the model's own counter, bumped when it changes the
// reducer so old rows do not replay through new code.
type JournalKey struct {
	Max   int `json:"max,omitempty"`
	Rules int `json:"rules,omitempty"`
}

// JournalState is one key as the prompt sees it right now: the newest match,
// its rules number, how many rows are stored, and the newest rows of the match
// being played.
type JournalState struct {
	Match int
	Rules int
	Max   int
	Rows  int
	Last  []JournalTailRow
}

// JournalTailRow is one recent row as the prompt shows it, in the shape the
// reducer gets, minus the stamps that are the same for every row of a match.
type JournalTailRow struct {
	ID      int64           `json:"id"`
	Visitor string          `json:"visitor"`
	Match   int             `json:"match"`
	Payload json.RawMessage `json:"payload"`
}

type journalKeyInput struct {
	Name  string `json:"name"`
	Max   int    `json:"max"`
	Rules int    `json:"rules"`
}

// ParseJournalSchema reads the stored schema. A broken schema declares no
// keys, so nothing is writable. Keys are normalized on the way out, so a
// hand-edited zero cannot reach the trim or the rules stamp.
func ParseJournalSchema(raw string) map[string]JournalKey {
	schema := make(map[string]JournalKey)
	if strings.TrimSpace(raw) == "" {
		return schema
	}
	_ = json.Unmarshal([]byte(raw), &schema)
	for name, key := range schema {
		schema[name] = normalizeJournalKey(key)
	}
	return schema
}

// normalizeJournalKey fills the defaults the writer and the reader share: a
// key with no max or rules gets the default, and the row cap applies.
func normalizeJournalKey(key JournalKey) JournalKey {
	if key.Max <= 0 {
		key.Max = defaultJournalMax
	}
	if key.Max > maxJournalRows {
		key.Max = maxJournalRows
	}
	if key.Rules <= 0 {
		key.Rules = 1
	}
	return key
}

// cleanJournalSchema validates the model's journal keys and stores them as a
// compact JSON object.
func cleanJournalSchema(keys []journalKeyInput) (string, error) {
	if len(keys) > maxJournalKeys {
		return "", fmt.Errorf("at most %d journal keys", maxJournalKeys)
	}

	schema := make(map[string]JournalKey, len(keys))
	for _, input := range keys {
		name := strings.TrimSpace(input.Name)
		if !validStateName(name) || len([]rune(name)) < 3 ||
			len([]rune(name)) > maxJournalName {
			return "", fmt.Errorf(
				"bad journal key %q: 3 or more of lowercase letters, digits, _ or -",
				name,
			)
		}
		if _, exists := schema[name]; exists {
			return "", fmt.Errorf("duplicate journal key %q", name)
		}

		key := JournalKey{Max: input.Max, Rules: input.Rules}
		schema[name] = normalizeJournalKey(key)
	}

	data, err := json.Marshal(schema)
	if err != nil {
		return "", fmt.Errorf("encode journal schema: %w", err)
	}
	return string(data), nil
}

// journalRules picks the stored rules number for one key in a schema write.
// declared is the key in the current schema, if it is there, and used is the
// highest rules number already stamped on stored rows of that key. A rules
// number that rows already carry belongs to the reducer that wrote them, so a
// bump, a new key or a key declared again after it was dropped skips past
// used. Keeping the declared number is the one way to keep the old rows.
// raised reports that the number moved past what was asked.
func journalRules(
	requested int,
	declared JournalKey,
	isDeclared bool,
	used int,
) (rules int, raised bool, err error) {
	if isDeclared {
		switch {
		case requested <= 0 || requested == declared.Rules:
			return declared.Rules, false, nil
		case requested < declared.Rules:
			return 0, false, fmt.Errorf(
				"cannot go down from %d; old rows would replay through new code",
				declared.Rules,
			)
		}
	}
	if used > 0 && requested <= used {
		return used + 1, requested > 0, nil
	}
	return requested, false, nil
}

// journalCall matches the runtime call that opens one feed. The guard asks for
// the call, not the bare key: a key like chat appears in ordinary prose long
// after the script that used it is gone.
func journalCall(key string) *regexp.Regexp {
	return regexp.MustCompile(`bd\.journal\(\s*["']` + regexp.QuoteMeta(key) + `["']\s*\)`)
}

// journalText is the markup the model wrote, where its scripts live.
func journalText(site Site) string {
	return site.HTML
}

// MissingJournalKeys names declared keys the page no longer opens with
// bd.journal("key"). A key no script opens cannot be used, and its rows
// become unreachable.
func MissingJournalKeys(site Site) []string {
	schema := ParseJournalSchema(site.JournalSchema)
	if len(schema) == 0 {
		return nil
	}
	text := journalText(site)
	missing := make([]string, 0, len(schema))
	for key := range schema {
		if !journalCall(key).MatchString(text) {
			missing = append(missing, key)
		}
	}
	sort.Strings(missing)
	return missing
}

// CleanJournalPayload keeps a journal write small and valid. The server never
// reads the payload, so this only bounds size and requires a JSON object:
// a scalar or null would make a page's reducer throw for everyone.
func CleanJournalPayload(raw json.RawMessage) (string, error) {
	if len(raw) == 0 {
		return "{}", nil
	}
	var buf bytes.Buffer
	if err := json.Compact(&buf, raw); err != nil {
		return "", fmt.Errorf("payload is not valid JSON")
	}
	data := buf.Bytes()
	if len(data) == 0 || data[0] != '{' {
		return "", fmt.Errorf("payload must be a JSON object")
	}
	if len(data) > maxJournalPayload {
		return "", fmt.Errorf("payload is longer than %d bytes", maxJournalPayload)
	}
	return string(data), nil
}

// JournalMatchClosed is the refusal for an append to a match that is no longer
// open. The runtime knows this exact text: it means another visitor started
// the next match, so the page reloads the feed instead of reporting an error.
const JournalMatchClosed = "that match is not open"

// JournalRuntime is the code-owned client half of the journal: a tiny
// append-and-fold runtime the model's page script calls. It owns the feed,
// seat identity and error reporting. The model only writes reduce and render.
//
// A feed reads its rows once. The runtime starts that read at load and
// start() takes it. After that the page's live stream pushes every new row
// through bd._rows, so there is no poll. Rows that arrive while a read is out
// are held and merged when it lands, so a row written between the read and
// the push is never lost.
//
// The rules number in the config is the one the page's code was written for,
// and it never changes in this tab. Every append carries it. When the server
// reports other rules, the reducer on this page is the wrong one, so the page
// asks the live script for a full reload instead of folding rows it does not
// understand. A new match only reloads the feed.
//
// It is deliberately small. Keep it that way: anything the model has to
// reimplement is page code it reads back on every turn.
var JournalRuntime = `<script>
(function () {
  var tag = document.getElementById('bd-journal')
  var conf = {}
  if (tag) {
    try { conf = JSON.parse(tag.textContent || '{}') } catch (e) { conf = {} }
  }
  var visitor = conf.visitor || ''
  var keys = conf.keys || {}
  var cache = Object.create(null)

  // The world mount path and browser-key root come from the same code-owned
  // config. A board served under /worlds/SLUG/ must call its own routes and keep
  // its saves apart from every other world on this origin.
  var base = String(conf.base || '').replace(/\/+$/, '')
  var store = String(conf.store || 'bd:')
  function url(path) { return base + path }

  // A burst of rows must not re-reduce the whole feed for every row, so a
  // draw is deferred to the next frame. A hidden tab parks
  // requestAnimationFrame, so it gets a timer there and still renders titles
  // and sounds.
  var nextFrame = window.requestAnimationFrame || function (fn) {
    return setTimeout(fn, 16)
  }
  function schedule(fn) {
    if (document.hidden) return setTimeout(fn, 16)
    return nextFrame(fn)
  }

  // The version tells the server which page threw. An error from a page that
  // has since been replaced is about code nobody is served any more.
  function report(message) {
    try {
      var body = JSON.stringify({
        message: String(message).slice(0, 300),
        version: Number(document.documentElement.getAttribute('data-version')) || 0
      })
      if (navigator.sendBeacon) {
        navigator.sendBeacon(url('/errors'), new Blob([body], { type: 'application/json' }))
      } else {
        fetch(url('/errors'), { method: 'POST', body: body, keepalive: true })
      }
    } catch (e) {}
  }

  // No line number: it counts lines of the whole served document, which the
  // model never sees, so it points nowhere in the html it edits.
  window.addEventListener('error', function (e) {
    report(e.message || 'script error')
  })
  window.addEventListener('unhandledrejection', function (e) {
    var r = e.reason
    report('unhandled rejection: ' + (r && r.message ? r.message : r))
  })

  function read(key) {
    return fetch(url('/journal/' + encodeURIComponent(key) + '?since=0'), { cache: 'no-store' })
      .then(function (r) {
        if (!r.ok) throw new Error('journal read ' + r.status)
        return r.json()
      })
  }

  function Journal(key) {
    var def = keys[key] || {}
    this.key = key
    this.match = def.match || 1
    this.rules = def.rules || 1
    this.max = def.max || 0
    this.rows = []
    this.seen = {}
    this.reduce = null
    this.render = null
    this.started = false
    // loaded is true once a whole read has landed. Until then a pushed row
    // has no history to fold onto, so it starts the read again and waits in
    // held for it.
    this.loaded = false
    // retried is true once a failed first read has been retried.
    this.retried = false
    // held collects pushed rows while a read is out. null means no read is.
    this.held = null
    // pending is true while a draw waits for the next frame.
    this.pending = false
    this.stale = false
  }

  // expire stops this feed for good and asks the live script for a full
  // reload: the server has rules this page's reducer was not written for.
  Journal.prototype.expire = function () {
    if (this.stale) return
    this.stale = true
    document.dispatchEvent(new CustomEvent('beaverdream:stale', { detail: { key: this.key } }))
  }

  // add merges rows without ever repeating one, keeps them in id order, and
  // trims to max, the same window the server keeps.
  Journal.prototype.add = function (rows) {
    var changed = false
    for (var i = 0; i < rows.length; i++) {
      var row = rows[i]
      if (this.seen[row.id]) continue
      this.seen[row.id] = 1
      if (row.match === this.match && row.rules === this.rules) {
        this.rows.push(row)
        changed = true
      }
    }
    if (changed) {
      this.rows.sort(function (a, b) { return a.id - b.id })
      this.trim()
    }
    return changed
  }

  // trim keeps only the newest max rows, so a long-open tab folds the same
  // history a reloaded one gets. A late echo of a dropped row sorts to the
  // front and falls off again here.
  Journal.prototype.trim = function () {
    if (this.max <= 0 || this.rows.length <= this.max) return
    var drop = this.rows.splice(0, this.rows.length - this.max)
    for (var i = 0; i < drop.length; i++) delete this.seen[drop[i].id]
  }

  // queueDraw schedules one reduce and render for the next frame, so a burst
  // of rows costs one draw instead of one per row.
  Journal.prototype.queueDraw = function () {
    if (!this.render || this.pending) return
    var self = this
    this.pending = true
    schedule(function () {
      self.pending = false
      self.paint()
    })
  }

  Journal.prototype.paint = function () {
    var state
    try {
      state = this.reduce ? this.reduce(this.rows) : this.rows
    } catch (e) {
      report('reduce(' + this.key + '): ' + (e && e.message ? e.message : e))
      return
    }
    try {
      this.render(state, this)
    } catch (e) {
      report('render(' + this.key + '): ' + (e && e.message ? e.message : e))
    }
  }

  // receive takes rows the stream pushed. Rows under other rules expire the
  // page; a row from a newer match means someone started the next game, and
  // one from an older match means the feed was cleared, so the feed reads
  // itself whole either way.
  Journal.prototype.receive = function (rows) {
    if (this.stale || !this.started) return
    if (!this.loaded) this.load()
    if (this.held) {
      for (var i = 0; i < rows.length; i++) this.held.push(rows[i])
      return
    }
    var fresh = []
    for (var j = 0; j < rows.length; j++) {
      if (rows[j].rules !== this.rules) {
        this.expire()
        return
      }
      if (rows[j].match > this.match) {
        this.load()
        return
      }
      // The server never accepts a lower match than the newest, so a row
      // from an older match that this page has not read means the feed was
      // cleared and started over. A row already read is only a late echo.
      if (rows[j].match < this.match && !this.seen[rows[j].id]) {
        this.load()
        return
      }
      fresh.push(rows[j])
    }
    if (this.add(fresh)) this.queueDraw()
  }

  // load reads the whole feed. The rules number is not taken from the reply:
  // a different one expires the page instead. A failed read leaves the board
  // as it was, so a server hiccup never draws an empty game. A feed that
  // never loaded tries once more after a second, then on the next push. Only
  // one read is out at a time; rows pushed meanwhile wait in held. preloaded
  // is a read the runtime already sent; without one, load sends a new read.
  Journal.prototype.load = function (preloaded) {
    var self = this
    if (self.stale || self.held) return Promise.resolve()
    self.held = []
    return (preloaded || read(self.key)).then(function (d) {
      var held = self.held
      self.held = null
      if (d.rules !== self.rules) {
        self.expire()
        return
      }
      self.match = d.match || 1
      self.rows = []
      self.seen = {}
      self.loaded = true
      self.add(d.rows || [])
      self.queueDraw()
      if (held.length) self.receive(held)
    }, function () {
      var held = self.held
      self.held = null
      // Before the first read lands, held rows would fold onto nothing. The
      // next push asks for the whole feed again.
      if (held.length && self.loaded) self.receive(held)
      // A first read that fails is tried once more after a second, so a page
      // that waits on the feed before its first write still loads on a quiet
      // board.
      if (!self.loaded && !self.retried) {
        self.retried = true
        setTimeout(function () { if (!self.loaded) self.load() }, 1000)
      }
    })
  }

  // append resolves with the stored row, or with null when the server
  // refused it for a reason that is not a bug in the page: a closed match
  // reloads the feed, other rules reload the page, too fast or no name drops
  // the write. A payload the server rejects is the page's own mistake, so it
  // is reported, and the promise still resolves. Only a payload that cannot
  // be serialized throws, before any request is sent.
  Journal.prototype.append = function (payload, opts) {
    var self = this
    if (self.stale) return Promise.resolve(null)
    var match = (opts && opts.match) || self.match
    var body = JSON.stringify({ payload: payload, match: match, rules: self.rules })
    return fetch(url('/journal/' + encodeURIComponent(self.key)), {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: body,
      cache: 'no-store'
    }).then(function (r) {
      return r.json().catch(function () { return {} }).then(function (d) {
        if (r.status === 409) {
          self.expire()
          return null
        }
        if (r.status === 400 && d.error === '` + JournalMatchClosed + `') {
          self.load()
          return null
        }
        if (r.status === 429 || r.status === 401) return null
        if (!r.ok) {
          if (r.status === 400) report('append(' + self.key + '): ' + (d.error || 'bad event'))
          return null
        }
        // Show it now. The stream pushes it again and add() skips it.
        self.receive([d])
        return d
      })
    }, function () { return null })
  }

  Journal.prototype.newMatch = function (payload) {
    return this.append(payload || {}, { match: this.match + 1 })
  }

  // start opens the feed. Both functions are the page's job, so a missing one
  // is reported instead of leaving a feed that never draws.
  Journal.prototype.start = function (reduce, render) {
    if (typeof reduce !== 'function') report('start(' + this.key + '): reduce is not a function')
    if (typeof render !== 'function') report('start(' + this.key + '): render is not a function')
    this.reduce = reduce
    this.render = render
    this.started = true
    // The read taken at load is used once, and only if no row was pushed for
    // this key since then. Any later load reads again.
    var pre = preloads[this.key]
    delete preloads[this.key]
    this.load(pre && !pre.missed ? pre.read : null)
    return this
  }

  // Every declared key is read as soon as the runtime loads, so a feed that
  // starts a moment later may already have its rows. start() takes the read
  // once. A row pushed for the key before then is not in the read, so the
  // feed reads again instead. A failed read is caught here, so the rejection
  // is not reported as unhandled. start() still takes it, and load retries a
  // failed first read.
  var preloads = Object.create(null)
  Object.keys(keys).forEach(function (key) {
    var p = read(key)
    p.then(null, function () {})
    preloads[key] = { read: p, missed: false }
  })

  // bd.state is the visitor-state half: counts this visitor owns. Calls run
  // one at a time, so a trade and a spend never race on the numbers the page
  // shows; the server is atomic either way. Every call resolves, with
  // { ok: true, mine } or { ok: false, reason }. A refusal is a game outcome,
  // not a bug in the page, so none of them reaches the error beacon.
  //
  // The answer's mine goes to the caller only. $mine and bd.state.mine come
  // from the stream, which always pushes the newest count: an answer can land
  // after a push from another tab and would put an older count back.
  //
  // A call that hangs is aborted and resolves slow, so it cannot hold the
  // queue for the life of the tab.
  var queue = Promise.resolve()
  var stateTimeout = 10000
  function stateCall(path) {
    function run() {
      var abort = typeof AbortController === 'function' ? new AbortController() : null
      var timer = abort ? setTimeout(function () { abort.abort() }, stateTimeout) : null
      return fetch(path, {
        method: 'POST',
        headers: { Accept: 'application/json' },
        cache: 'no-store',
        signal: abort ? abort.signal : undefined
      }).then(function (r) {
        return r.json().catch(function () { return {} }).then(function (d) {
          if (r.ok && d && d.ok) return { ok: true, mine: d.mine || {} }
          return { ok: false, reason: (d && d.reason) || 'bad' }
        })
      }).then(null, function () {
        return { ok: false, reason: 'slow' }
      }).then(function (result) {
        if (timer) clearTimeout(timer)
        return result
      })
    }
    var p = queue.then(run, run)
    queue = p
    return p
  }
  function amount(n) {
    return n === undefined ? 1 : Math.floor(Number(n))
  }
  var state = {
    mine: conf.mine || {},
    tops: conf.tops || {},
    add: function (key, n) {
      return stateCall(url('/state/' + encodeURIComponent(key) + '?add=' + amount(n)))
    },
    spend: function (key, n) {
      return stateCall(url('/state/' + encodeURIComponent(key) + '?spend=' + amount(n)))
    },
    trade: function (recipe) {
      return stateCall(url('/state/' + encodeURIComponent(recipe)))
    }
  }

  // bd.save and bd.load keep small things in this browser across reloads
  // and page changes: where the visitor stood, the camera, settings. A save
  // lands in memory at once and reaches localStorage at most twice a second
  // and when the page hides, so a page can save every frame. Storage that
  // throws (private mode, full, blocked) still leaves the memory copy for
  // this tab. A value that is not JSON or is too long is the page's own
  // mistake: it is reported once per name and not kept. A stored value that
  // does not parse loads as the fallback. Every name is under the world's
  // immutable storage root, never the mutable slug.
  var savePrefix = store + 'save:'
  var saveMax = ` + strconv.Itoa(maxSaveLen) + `
  var saved = Object.create(null)
  var dirty = Object.create(null)
  var saveReported = Object.create(null)
  var flushTimer = null
  function storage() {
    try { return window.localStorage || null } catch (e) { return null }
  }
  function flush() {
    if (flushTimer) clearTimeout(flushTimer)
    flushTimer = null
    var ls = storage()
    for (var name in dirty) {
      delete dirty[name]
      try {
        if (!ls) continue
        if (saved[name] === null) ls.removeItem(savePrefix + name)
        else ls.setItem(savePrefix + name, saved[name])
      } catch (e) {}
    }
  }
  function saveProblem(name, what) {
    if (saveReported[name]) return
    saveReported[name] = 1
    report('save(' + name + '): ' + what)
  }
  function save(name, value) {
    name = String(name)
    var text = null
    if (value !== null && value !== undefined) {
      try { text = JSON.stringify(value) } catch (e) { text = undefined }
      if (typeof text !== 'string') {
        saveProblem(name, 'the value is not JSON')
        return false
      }
      if (text.length > saveMax) {
        saveProblem(name, 'the value is longer than ' + saveMax + ' characters')
        return false
      }
    }
    saved[name] = text
    dirty[name] = 1
    if (!flushTimer) flushTimer = setTimeout(flush, 500)
    return true
  }
  function load(name, fallback) {
    name = String(name)
    var text = saved[name]
    if (text === undefined) {
      var ls = storage()
      try { text = ls ? ls.getItem(savePrefix + name) : null } catch (e) { text = null }
    }
    if (typeof text !== 'string') return fallback
    try { return JSON.parse(text) } catch (e) { return fallback }
  }
  window.addEventListener('pagehide', flush)
  document.addEventListener('visibilitychange', function () {
    if (document.hidden) flush()
  })

  window.bd = {
    visitor: visitor,
    state: state,
    save: save,
    load: load,
    journal: function (key) {
      if (!Object.prototype.hasOwnProperty.call(keys, key)) {
        throw new Error('undeclared journal key: ' + key)
      }
      if (!cache[key]) cache[key] = new Journal(key)
      return cache[key]
    },
    // _rows and _resync are for the live stream. Rows carry their key; a key
    // no script opened is skipped.
    _rows: function (rows) {
      var byKey = Object.create(null)
      for (var i = 0; i < rows.length; i++) {
        var key = rows[i].key
        if (preloads[key]) preloads[key].missed = true // the read at load may predate it
        if (!cache[key]) continue
        if (!byKey[key]) byKey[key] = []
        byKey[key].push(rows[i])
      }
      for (var k in byKey) cache[k].receive(byKey[k])
    },
    _resync: function () {
      // A read taken at load may predate the change, so start() must not use it.
      for (var p in preloads) preloads[p].missed = true
      for (var k in cache) if (cache[k].started) cache[k].load()
    }
  }
})()
</script>`
