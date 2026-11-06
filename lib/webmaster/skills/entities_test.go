package skills

import (
	"encoding/json"
	"fmt"
	"testing"

	"github.com/Napam/beaverdream/lib/webmaster/internal/jsrun"
)

// entitiesHarness runs the core from entitiesReference with frames driven by hand and
// reports what the numbers did.
const entitiesHarness = `
const fs = require('fs')
const vm = require('vm')
const src = fs.readFileSync(process.argv[2], 'utf8')
const out = {}

function fresh() {
  const ctx = {}
  vm.createContext(ctx)
  vm.runInContext(src, ctx)
  out.globals = Object.keys(ctx)
  return ctx.world
}
function run(w, seconds, fps) {
  const n = Math.round(seconds * fps)
  for (let i = 0; i < n; i++) w.frame(1 / fps)
}

// A kind only does what it lists: a scout has no grow, a heavy has no shoot.
let w = fresh()
const moves = []
w.onMove = (name, e, p) => moves.push([name, Math.round(p * 100) / 100])
const scout = w.spawn('scout', 0, 0, true)
const heavy = w.spawn('heavy', 0, 5, true)
w.press(scout, 'KeyQ'); w.release(scout, 'KeyQ')
w.press(heavy, 'KeyE'); w.release(heavy, 'KeyE')
out.wrongKeys = [scout.kind, moves.length]

// Transform: grow swaps the moves and keeps the share of health.
heavy.hp = 80
w.press(heavy, 'KeyQ'); w.release(heavy, 'KeyQ')
out.grown = [heavy.kind, heavy.hp]
w.press(heavy, 'KeyE')
out.grownShoots = moves.length
run(w, 1, 60)

// Charge: a tap is min power, a long hold is full, halfway is in between.
// A held key does nothing until it is released.
function charge(seconds) {
  moves.length = 0
  run(w, 1, 60)
  w.press(heavy, 'KeyX')
  run(w, seconds, 60)
  const during = moves.length
  const bar = w.charging(heavy, 'KeyX')
  w.release(heavy, 'KeyX')
  return [during, Math.round(bar * 100) / 100, moves[0] && moves[0][1]]
}
out.tap = charge(0.05)
out.half = charge(0.68)
out.full = charge(1.5)

// Cooldown: a second slam inside 0.7s is ignored.
moves.length = 0
run(w, 1, 60)
w.press(heavy, 'KeyX'); w.release(heavy, 'KeyX')
run(w, 0.3, 60)
w.press(heavy, 'KeyX'); w.release(heavy, 'KeyX')
out.cooled = moves.length
run(w, 0.5, 60)
w.press(heavy, 'KeyX'); w.release(heavy, 'KeyX')
out.again = moves.length

// Shots fly their range and end there at any frame rate, since the world
// steps in fixed steps.
out.range = {}
for (const fps of [30, 60, 144]) {
  w = fresh()
  const ends = []
  w.onShotEnd = (s, why) => ends.push([why, Math.round(s.x * 100) / 100])
  const me = w.spawn('scout', 0, 0, true)
  w.press(me, 'KeyE')
  run(w, 0.5, fps)
  const mid = ends.length
  run(w, 1, fps)
  out.range[fps] = [mid, ends]
}

// A shot hits the first thing in its path, never its owner; three kill a walker.
w = fresh()
const hits = [], deaths = []
w.onHit = (s, e) => hits.push(e.kind)
w.onDeath = (e) => deaths.push(e.kind)
const me = w.spawn('scout', 0, 0, true)
const walker = w.spawn('walker', 10, 0)
w.BRAINS.chase = () => {}
for (let i = 0; i < 3; i++) { w.press(me, 'KeyE'); run(w, 0.6, 60) }
out.hits = hits
out.deaths = [deaths, w.things.length]

// Walls stop shots and bodies at their edge, not their center: the wall is
// at x = 2 and the dash alone would carry the scout past 2.5.
w = fresh()
const why = []
w.blocked = (x, y, r) => x + r > 2
w.onShotEnd = (s, r) => why.push([r, Math.round(s.x * 100) / 100])
const shooter = w.spawn('scout', 0, 0, true)
w.press(shooter, 'KeyE')
w.press(shooter, 'KeyX')
run(w, 1, 60)
out.wall = why
out.wallX = Math.round(shooter.x * 100) / 100
w = fresh()
const free = w.spawn('scout', 0, 0, true)
w.press(free, 'KeyX')
run(w, 1, 60)
out.dashAlone = Math.round(free.x * 100) / 100
out.dashSettles = Math.abs(free.vx) < 0.05

// The shot pool never grows.
w = fresh()
const gun = w.spawn('scout', 0, 0, true)
let fired = 0
w.onMove = () => fired++
for (let i = 0; i < 400; i++) { gun.cool = {}; w.press(gun, 'KeyE') }
out.pool = [w.shots.length, w.shots.filter((s) => s.on).length, fired]

// Spawning: never within min of a player, and never past the kind's max.
w = fresh()
const p1 = w.spawn('scout', 5, 5, true)
const p2 = w.spawn('scout', 15, 15, true)
let closest = Infinity, made = 0
for (let i = 0; i < 50; i++) {
  const e = w.spawnAway('walker', { x: 0, y: 0, w: 40, h: 40 }, 6)
  if (!e) continue
  made++
  closest = Math.min(closest, Math.hypot(e.x - p1.x, e.y - p1.y), Math.hypot(e.x - p2.x, e.y - p2.y))
}
out.spawn = [made, closest >= 6]
w = fresh()
w.spawn('scout', 5, 5, true)
out.noRoom = w.spawnAway('walker', { x: 0, y: 0, w: 10, h: 10 }, 100)

// Brains: a walker closes on the nearest player and faces it.
w = fresh()
w.spawn('scout', 0, 0, true)
const chaser = w.spawn('walker', 10, 0)
run(w, 1, 60)
out.chase = Math.round(chaser.x * 100) / 100
out.chaseFace = Math.round(chaser.face * 100) / 100

// Input: mx and my move at the kind's speed along any direction.
w = fresh()
const walkerMe = w.spawn('scout', 0, 0, true)
walkerMe.mx = 1; walkerMe.my = 1
run(w, 2, 60)
out.diagonalSpeed = Math.round(Math.hypot(walkerMe.vx, walkerMe.vy) * 100) / 100

// A driven thing is moved by the page: world runs its timers, not its body.
w = fresh()
const driven = w.spawn('scout', 3, 3, true)
driven.driven = true; driven.mx = 1
w.press(driven, 'KeyX')
run(w, 0.5, 60)
out.driven = [driven.x, driven.y, driven.cool.dash < 0.6]

// A long pause runs at most 0.1s of steps.
w = fresh()
let steps = 0
w.BRAINS.count = () => { steps++ }
w.KINDS.walker.brain = 'count'
w.spawn('walker', 0, 0)
w.frame(5)
out.pauseSteps = steps

// A charge move without min starts at 0, and become is open to the page.
w = fresh()
w.MOVES.slam.min = undefined
const powers = []
w.onMove = (name, e, p) => powers.push(p)
const big = w.spawn('heavy', 0, 0, true)
w.press(big, 'KeyX'); w.release(big, 'KeyX')
out.noMin = powers
w.become(big, 'giant')
out.become = big.kind

// Drawing blends: one and a half steps run one step and leave alpha at a
// half, with px one step behind x.
w = fresh()
const runner = w.spawn('scout', 0, 0, true)
runner.mx = 1
const alpha = w.frame(1.5 / 60)
out.alpha = [Math.round(alpha * 100) / 100, runner.px < runner.x]

console.log(JSON.stringify(out))
`

func TestEntitiesReferenceCore(t *testing.T) {
	out := jsrun.Run(t, entitiesReference, entitiesHarness, "")
	t.Logf("%s", out)
	var got struct {
		Globals          []string
		WrongKeys, Grown []any
		GrownShoots      int
		Tap, Half, Full  []float64
		Cooled, Again    int
		Range            map[string][]json.RawMessage
		Hits             []string
		Deaths           []json.RawMessage
		Wall             json.RawMessage
		WallX, DashAlone float64
		DashSettles      bool
		ChaseFace        float64
		DiagonalSpeed    float64
		Driven           []any
		PauseSteps       int
		NoMin            []float64
		Become           string
		Pool             []int
		Spawn            []any
		NoRoom           any
		Chase            float64
		Alpha            []any
	}
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatalf("decode: %v\n%s", err, out)
	}
	if len(got.Globals) != 1 || got.Globals[0] != "world" {
		t.Errorf("the core declares globals %v, want only world", got.Globals)
	}
	if fmt.Sprint(got.WrongKeys) != "[scout 0]" {
		t.Errorf("keys a kind does not list = %v, want [scout 0]: nothing ran", got.WrongKeys)
	}
	if fmt.Sprint(got.Grown) != "[giant 150]" || got.GrownShoots != 1 {
		t.Errorf(
			"grow = %v with %d moves, want [giant 150] and only the grow",
			got.Grown,
			got.GrownShoots,
		)
	}
	for name, c := range map[string][]float64{"tap": got.Tap, "half": got.Half, "full": got.Full} {
		exp := map[string]float64{"tap": 0.3, "half": 0.65, "full": 1}[name]
		if len(c) != 3 || c[0] != 0 || c[1] != exp || c[2] != exp {
			t.Errorf("%s charge = %v, want [0 %v %v]", name, c, exp, exp)
		}
	}
	if got.Cooled != 1 || got.Again != 2 {
		t.Errorf("slams during the cooldown %d, after %d; want 1 and 2", got.Cooled, got.Again)
	}
	for _, fps := range []string{"30", "60", "144"} {
		if r := got.Range[fps]; len(r) != 2 || string(r[0]) != "0" ||
			string(r[1]) != `[["range",25]]` {
			t.Errorf("shot at %s fps = %s, want in flight at 0.5s and ended at 25", fps, r)
		}
	}
	if fmt.Sprint(got.Hits) != "[walker walker walker]" ||
		len(
			got.Deaths,
		) != 2 || string(got.Deaths[0]) != `["walker"]` || string(got.Deaths[1]) != "1" {
		t.Errorf("hits %v deaths %s, want three hits and the walker gone", got.Hits, got.Deaths)
	}
	if string(got.Wall) != `[["wall",2]]` || got.WallX < 1.5 || got.WallX > 1.6 {
		t.Errorf(
			"wall: shot %s, scout at %v; want the shot ended at the wall and the scout's edge at 2",
			got.Wall,
			got.WallX,
		)
	}
	if got.DashAlone < 2.5 || !got.DashSettles {
		t.Errorf("a dash with no wall went %v and settles %v; want past 2.5 and back to rest",
			got.DashAlone, got.DashSettles)
	}
	if fmt.Sprint(got.Pool) != "[200 200 200]" {
		t.Errorf("pool [size live moves] = %v, want [200 200 200]", got.Pool)
	}
	if fmt.Sprint(got.Spawn) != "[6 true]" || got.NoRoom != nil {
		t.Errorf(
			"spawn [made far] = %v, no room = %v; want [6 true] and null",
			got.Spawn,
			got.NoRoom,
		)
	}
	if got.Chase < 8.2 || got.Chase > 8.5 || got.ChaseFace != 3.14 {
		t.Errorf(
			"walker after 1s at x = %v facing %v, want about 8.33 facing pi",
			got.Chase,
			got.ChaseFace,
		)
	}
	if got.DiagonalSpeed != 7 {
		t.Errorf("diagonal input reached speed %v, want the scout's 7", got.DiagonalSpeed)
	}
	if fmt.Sprint(got.Driven) != "[3 3 true]" {
		t.Errorf("driven [x y cooling] = %v, want [3 3 true]: unmoved, timers running", got.Driven)
	}
	if got.PauseSteps != 6 {
		t.Errorf("a 5s frame ran %d steps, want 6 (0.1s)", got.PauseSteps)
	}
	if fmt.Sprint(got.NoMin) != "[0]" || got.Become != "giant" {
		t.Errorf(
			"a tap without min gave %v and become gave %q; want [0] and giant",
			got.NoMin,
			got.Become,
		)
	}
	if fmt.Sprint(got.Alpha) != "[0.5 true]" {
		t.Errorf("blend = %v, want [0.5 true]", got.Alpha)
	}
}
