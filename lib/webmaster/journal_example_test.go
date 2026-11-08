package webmaster

import (
	"encoding/json"
	"testing"

	"github.com/Napam/beaverdream/lib/webmaster/internal/jsrun"
)

// The complete prompt example runs against feeds with immediate local echoes.
const journalExampleHarness = `
const fs = require('fs'), vm = require('vm')
const feeds = {}, played = []
let id = 0
function row(visitor, payload) { return { id: ++id, visitor, payload } }
function feed(key) {
  return feeds[key] ||= {
    match: 1,
    rows: key === 'acts' ? [row('them', { fire: 'past' })] : [],
    start(reduce, render) { this.reduce = reduce; this.render = render; this.draw() },
    draw() { this.render(this.reduce(this.rows), this) },
    push(visitor, payload) { this.rows.push(row(visitor, payload)); this.draw() },
    append(payload) { this.push('me', payload) },
    newMatch(payload) { this.match++; this.rows = []; this.push('me', payload) }
  }
}
const ctx = {
  bd: { visitor: 'me', journal: feed, load: () => null, save: () => {} },
  setInterval: () => {}, requestAnimationFrame: () => {},
  draw: () => {}, showScore: () => {},
  play: (visitor, payload) => played.push({ visitor, fire: payload.fire })
}
vm.createContext(ctx)
vm.runInContext(fs.readFileSync(process.argv[2], 'utf8'), ctx)
const past = played.length
feeds.acts.push('them', { fire: 'new' })
feeds.acts.draw()  // folding the same window again must not replay an act
ctx.act({ fire: 'local' })  // the echo must not play our local effect twice
feeds.seats.push('me', { join: 1 })
feeds.seats.push('them', { join: 1 })
feeds.seats.push('third', { join: 1 })
feeds.seats.push('third', { score: 1 })
feeds.seats.push('me', { score: 5 })  // cannot skip scores
for (let score = 1; score <= 10; score++) feeds.seats.push('me', { score })
const before = ctx.score
ctx.rematch()
console.log(JSON.stringify({
  past, played, before, after: ctx.score,
  matches: [feeds.seats.match, feeds.moves.match, feeds.acts.match]
}))
`

func TestJournalExampleSeatsActsAndRematch(t *testing.T) {
	var got struct {
		Past   int
		Played []struct{ Visitor, Fire string }
		Before struct {
			Players []string
			Scores  map[string]int
			Winner  string
		}
		After struct {
			Players []string
			Scores  map[string]int
			Winner  string
		}
		Matches []int
	}
	out := jsrun.Run(t, journalPrompt, journalExampleHarness, "")
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatal(err)
	}
	if got.Past != 0 || len(got.Played) != 2 {
		t.Fatalf(
			"past effects = %d, played = %+v, want only one new remote and one local act",
			got.Past, got.Played,
		)
	}
	if got.Played[0].Visitor != "them" || got.Played[0].Fire != "new" ||
		got.Played[1].Visitor != "me" || got.Played[1].Fire != "local" {
		t.Errorf("played = %+v, want the remote act then the local act", got.Played)
	}
	if len(got.Before.Players) != 2 || len(got.Before.Scores) != 1 ||
		got.Before.Scores["me"] != 10 || got.Before.Winner != "me" {
		t.Errorf(
			"finished game = %+v, want two seats and me winning with ten legal scores",
			got.Before,
		)
	}
	if len(got.After.Players) != 1 || got.After.Players[0] != "me" ||
		len(got.After.Scores) != 0 || got.After.Winner != "" {
		t.Errorf("rematch = %+v, want a fresh score feed with one seat", got.After)
	}
	if len(got.Matches) != 3 || got.Matches[0] != 2 ||
		got.Matches[1] != 1 || got.Matches[2] != 1 {
		t.Errorf("matches = %v, want only seats advanced: [2 1 1]", got.Matches)
	}
}
