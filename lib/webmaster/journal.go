package webmaster

import (
	"bytes"
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strings"
)

const (
	maxJournalKeys    = 6
	maxJournalName    = 32
	maxJournalRows    = 1000
	defaultJournalMax = 300
	maxJournalPayload = 600
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
// keys, so nothing is writable.
func ParseJournalSchema(raw string) map[string]JournalKey {
	schema := make(map[string]JournalKey)
	if strings.TrimSpace(raw) == "" {
		return schema
	}
	_ = json.Unmarshal([]byte(raw), &schema)
	return schema
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
		if key.Max <= 0 {
			key.Max = defaultJournalMax
		}
		if key.Max > maxJournalRows {
			key.Max = maxJournalRows
		}
		if key.Rules <= 0 {
			key.Rules = 1
		}
		schema[name] = key
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

// journalText is every part of the page the model wrote.
func journalText(site Site) string {
	return site.Header + "\n" + site.Board + "\n" + site.Footer
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
// A feed reads its rows once when it starts. After that the page's live
// stream pushes every new row through bd._rows, so there is no poll. Rows that
// arrive while a read is out are held and merged when it lands, so a row
// written between the read and the push is never lost.
//
// The rules number in the config is the one the page's code was written for,
// and it never changes in this tab. Every append carries it. When the server
// reports other rules, the reducer on this page is the wrong one, so the page
// asks the live script for a full reload instead of folding rows it does not
// understand. A new match only reloads the feed.
//
// It is deliberately small. Every line here is a line the model does not have
// to write, and the page it writes is read back on every turn.
const JournalRuntime = `<script>
(function () {
  var tag = document.getElementById('bd-journal')
  var conf = {}
  if (tag) {
    try { conf = JSON.parse(tag.textContent || '{}') } catch (e) { conf = {} }
  }
  var visitor = conf.visitor || ''
  var keys = conf.keys || {}
  var cache = Object.create(null)

  // The version tells the server which page threw. An error from a page that
  // has since been replaced is about code nobody is served any more.
  function report(message) {
    try {
      var body = JSON.stringify({
        message: String(message).slice(0, 300),
        version: Number(document.documentElement.getAttribute('data-version')) || 0
      })
      if (navigator.sendBeacon) {
        navigator.sendBeacon('/x/err', new Blob([body], { type: 'application/json' }))
      } else {
        fetch('/x/err', { method: 'POST', body: body, keepalive: true })
      }
    } catch (e) {}
  }

  // No line number: it counts lines of the whole served document, which the
  // model never sees, so it points nowhere in the regions it edits.
  window.addEventListener('error', function (e) {
    report(e.message || 'script error')
  })
  window.addEventListener('unhandledrejection', function (e) {
    var r = e.reason
    report('unhandled rejection: ' + (r && r.message ? r.message : r))
  })

  function read(key) {
    return fetch('/j/' + encodeURIComponent(key) + '?since=0', { cache: 'no-store' })
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
    this.rows = []
    this.seen = {}
    this.reduce = null
    this.render = null
    this.started = false
    // loaded is true once a whole read has landed. Until then a pushed row
    // has no history to fold onto, so it starts the read again and waits in
    // held for it.
    this.loaded = false
    // held collects pushed rows while a read is out. null means no read is.
    this.held = null
    this.stale = false
  }

  Journal.prototype.same = function (row) {
    return row.match === this.match && row.rules === this.rules
  }

  // expire stops this feed for good and asks the live script for a full
  // reload: the server has rules this page's reducer was not written for.
  Journal.prototype.expire = function () {
    if (this.stale) return
    this.stale = true
    document.dispatchEvent(new CustomEvent('beaverdream:stale', { detail: { key: this.key } }))
  }

  // add merges rows without ever repeating one, and keeps them in id order.
  Journal.prototype.add = function (rows) {
    var changed = false
    for (var i = 0; i < rows.length; i++) {
      var row = rows[i]
      if (this.seen[row.id]) continue
      this.seen[row.id] = 1
      if (this.same(row)) {
        this.rows.push(row)
        changed = true
      }
    }
    if (changed) {
      this.rows.sort(function (a, b) { return a.id - b.id })
    }
    return changed
  }

  Journal.prototype.draw = function () {
    if (!this.render) return
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
  // page; a row from a newer match means someone started the next game, so
  // the feed reads it whole.
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
      fresh.push(rows[j])
    }
    if (this.add(fresh)) this.draw()
  }

  // load reads the whole feed. The rules number is not taken from the reply:
  // a different one expires the page instead. A failed read leaves the board
  // as it was, so a server hiccup never draws an empty game, and a feed that
  // never loaded tries again on the next push. Only one read is out at a
  // time; rows pushed meanwhile wait in held.
  Journal.prototype.load = function () {
    var self = this
    if (self.stale || self.held) return Promise.resolve()
    self.held = []
    return read(self.key).then(function (d) {
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
      self.draw()
      if (held.length) self.receive(held)
    }, function () {
      var held = self.held
      self.held = null
      // Before the first read lands, held rows would fold onto nothing. The
      // next push asks for the whole feed again.
      if (held.length && self.loaded) self.receive(held)
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
    return fetch('/j/' + encodeURIComponent(self.key), {
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

  Journal.prototype.start = function (reduce, render) {
    this.reduce = reduce
    this.render = render
    this.started = true
    this.load()
    return this
  }

  window.bd = {
    visitor: visitor,
    keys: keys,
    journal: function (key) {
      if (!Object.prototype.hasOwnProperty.call(keys, key)) {
        throw new Error('undeclared journal key: ' + key)
      }
      if (!cache[key]) cache[key] = new Journal(key)
      return cache[key]
    },
    report: report,
    // _rows and _resync are for the live stream. Rows carry their key; a key
    // no script opened is skipped.
    _rows: function (rows) {
      var byKey = Object.create(null)
      for (var i = 0; i < rows.length; i++) {
        var key = rows[i].key
        if (!cache[key]) continue
        if (!byKey[key]) byKey[key] = []
        byKey[key].push(rows[i])
      }
      for (var k in byKey) cache[k].receive(byKey[k])
    },
    _resync: function () {
      for (var k in cache) if (cache[k].started) cache[k].load()
    }
  }
})()
</script>`
