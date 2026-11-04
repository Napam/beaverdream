package mockllm

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Napam/beaverdream/lib/webmaster"
)

// gameHarness boots the applet with a fake page: a bd stub captures the
// reducer the applet hands to feed.start, then the reducer is called on a row
// list from argv. No DOM is involved, so the rules can be checked on their
// own.
const gameHarness = `
const fs = require('fs')
const src = fs.readFileSync(process.argv[2], 'utf8')
let reduce = null
const feed = {
  start: function (r) { reduce = r },
  append: function () {},
  newMatch: function () {},
  rows: []
}
global.bd = {
  visitor: 'a',
  journal: function () { return feed }
}
global.document = {
  getElementById: function () { return null },
  querySelectorAll: function () { return [] },
  documentElement: {
    dataset: {},
    setAttribute: function () {},
    getAttribute: function () { return null }
  },
  addEventListener: function () {}
}
global.window = global
global.fetch = function () { return Promise.reject(new Error('offline')) }
eval(src)
if (!reduce) {
  console.error('the applet never started a feed')
  process.exit(2)
}
const rows = JSON.parse(process.argv[3])
console.log(JSON.stringify(reduce(rows)))
`

type gameState struct {
	Board   []string `json:"board"`
	Players []string `json:"players"`
	Winner  string   `json:"winner"`
	Turn    string   `json:"turn"`
}

func runGame(t *testing.T, rows []map[string]any) gameState {
	t.Helper()
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node is not installed")
	}

	start := strings.Index(GameScript, "<script>")
	end := strings.LastIndex(GameScript, "</script>")
	if start < 0 || end < start {
		t.Fatal("GameScript has no script block")
	}
	applet := GameScript[start+len("<script>") : end]

	dir := t.TempDir()
	appletPath := filepath.Join(dir, "applet.js")
	harnessPath := filepath.Join(dir, "harness.js")
	if err := os.WriteFile(appletPath, []byte(applet), 0o600); err != nil {
		t.Fatalf("write applet: %v", err)
	}
	if err := os.WriteFile(harnessPath, []byte(gameHarness), 0o600); err != nil {
		t.Fatalf("write harness: %v", err)
	}
	data, err := json.Marshal(rows)
	if err != nil {
		t.Fatalf("marshal rows: %v", err)
	}

	out, err := exec.Command(node, harnessPath, appletPath, string(data)).CombinedOutput()
	if err != nil {
		t.Fatalf("node: %v\n%s", err, out)
	}
	var state gameState
	if err := json.Unmarshal(out, &state); err != nil {
		t.Fatalf("decode state: %v\n%s", err, out)
	}
	return state
}

func gameRow(id int, visitor string, payload map[string]any) map[string]any {
	return map[string]any{
		"id":      id,
		"visitor": visitor,
		"payload": payload,
		"match":   1,
		"rules":   1,
		"at":      "2026-01-01T00:00:00Z",
	}
}

func TestGameAppletFindsAWinner(t *testing.T) {
	state := runGame(t, []map[string]any{
		gameRow(1, "a", map[string]any{"join": 1}),
		gameRow(2, "b", map[string]any{"join": 1}),
		gameRow(3, "a", map[string]any{"cell": 0}),
		gameRow(4, "b", map[string]any{"cell": 3}),
		gameRow(5, "a", map[string]any{"cell": 1}),
		gameRow(6, "b", map[string]any{"cell": 4}),
		gameRow(7, "a", map[string]any{"cell": 2}),
	})

	if state.Winner != "X" {
		t.Fatalf("winner = %q, want X (board %v)", state.Winner, state.Board)
	}
	want := []string{"X", "X", "X", "O", "O", "", "", "", ""}
	for i := range want {
		if state.Board[i] != want[i] {
			t.Fatalf("board = %v, want %v", state.Board, want)
		}
	}
}

func TestGameAppletIgnoresOutOfTurnMoves(t *testing.T) {
	state := runGame(t, []map[string]any{
		gameRow(1, "a", map[string]any{"join": 1}),
		gameRow(2, "b", map[string]any{"join": 1}),
		gameRow(3, "a", map[string]any{"cell": 0}),
		gameRow(4, "a", map[string]any{"cell": 1}), // not a's turn
		gameRow(5, "c", map[string]any{"cell": 2}), // not a player
		gameRow(6, "b", map[string]any{"cell": 4}),
	})

	if state.Board[0] != "X" || state.Board[4] != "O" {
		t.Fatalf("board = %v, want X at 0 and O at 4", state.Board)
	}
	if state.Board[1] != "" || state.Board[2] != "" {
		t.Fatalf("board = %v, out-of-turn and non-player moves must be ignored", state.Board)
	}
	if state.Turn != "a" {
		t.Fatalf("turn = %q, want a", state.Turn)
	}
}

// One player cannot fill the board alone: a game needs two seats.
func TestGameAppletNeedsTwoPlayers(t *testing.T) {
	state := runGame(t, []map[string]any{
		gameRow(1, "a", map[string]any{"join": 1}),
		gameRow(2, "a", map[string]any{"cell": 0}),
		gameRow(3, "a", map[string]any{"cell": 1}),
	})
	for i, cell := range state.Board {
		if cell != "" {
			t.Fatalf("board[%d] = %q, want an empty board until two players sit", i, cell)
		}
	}
	if state.Turn != "" {
		t.Fatalf("turn = %q, want nobody", state.Turn)
	}
}

// The third visitor to press join is a spectator, not a third player.
func TestGameAppletCapsPlayersAtTwo(t *testing.T) {
	state := runGame(t, []map[string]any{
		gameRow(1, "a", map[string]any{"join": 1}),
		gameRow(2, "b", map[string]any{"join": 1}),
		gameRow(3, "c", map[string]any{"join": 1}),
		gameRow(4, "a", map[string]any{"cell": 0}),
		gameRow(5, "c", map[string]any{"cell": 1}),
		gameRow(6, "b", map[string]any{"cell": 3}),
	})
	if len(state.Players) != 2 {
		t.Fatalf("players = %v, want two", state.Players)
	}
	if state.Board[1] != "" {
		t.Fatalf("board = %v, a spectator's move landed", state.Board)
	}
	if state.Board[0] != "X" || state.Board[3] != "O" {
		t.Fatalf("board = %v, want X at 0 and O at 3", state.Board)
	}
}

// Once someone has won, later moves do not keep drawing on the board.
func TestGameAppletStopsAfterAWin(t *testing.T) {
	state := runGame(t, []map[string]any{
		gameRow(1, "a", map[string]any{"join": 1}),
		gameRow(2, "b", map[string]any{"join": 1}),
		gameRow(3, "a", map[string]any{"cell": 0}),
		gameRow(4, "b", map[string]any{"cell": 3}),
		gameRow(5, "a", map[string]any{"cell": 1}),
		gameRow(6, "b", map[string]any{"cell": 4}),
		gameRow(7, "a", map[string]any{"cell": 2}),
		gameRow(8, "b", map[string]any{"cell": 5}),
	})
	if state.Winner != "X" {
		t.Fatalf("winner = %q", state.Winner)
	}
	if state.Board[5] != "" {
		t.Fatalf("board = %v, a move after the win was applied", state.Board)
	}
}

// The shipped page opens its feed with the call form the journal guard looks
// for, so a later restyle that keeps it is never deferred.
func TestGamePageOpensItsFeed(t *testing.T) {
	var schema struct {
		Keys []struct {
			Name  string `json:"name"`
			Max   int    `json:"max"`
			Rules int    `json:"rules"`
		} `json:"keys"`
	}
	var site struct {
		Board string `json:"board"`
	}
	for _, call := range gameCalls() {
		switch call.Function.Name {
		case "set_journal_schema":
			if err := json.Unmarshal([]byte(call.Function.Arguments), &schema); err != nil {
				t.Fatalf("decode schema: %v", err)
			}
		case "set_site":
			if err := json.Unmarshal([]byte(call.Function.Arguments), &site); err != nil {
				t.Fatalf("decode site: %v", err)
			}
		}
	}
	declared := map[string]webmaster.JournalKey{}
	for _, key := range schema.Keys {
		declared[key.Name] = webmaster.JournalKey{Max: key.Max, Rules: key.Rules}
	}
	raw, err := json.Marshal(declared)
	if err != nil {
		t.Fatalf("encode schema: %v", err)
	}
	missing := webmaster.MissingJournalKeys(webmaster.Site{
		Board:         site.Board,
		JournalSchema: string(raw),
	})
	if len(declared) == 0 || len(missing) != 0 {
		t.Fatalf(
			"declared %v, missing %v: the game page must call bd.journal for its key",
			declared,
			missing,
		)
	}
}
