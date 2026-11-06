package skills

import (
	"encoding/json"
	"fmt"
	"testing"

	"github.com/Napam/beaverdream/lib/webmaster/internal/jsrun"
)

// pathsHarness runs the core from pathsReference on hand-built maps and
// reports what the paths did.
const pathsHarness = `
const fs = require('fs')
const vm = require('vm')
const src = fs.readFileSync(process.argv[2], 'utf8')
const out = {}

const ctx = { Math }
vm.createContext(ctx)
vm.runInContext(src, ctx)
out.globals = Object.keys(ctx).filter((k) => k !== 'Math')
const paths = ctx.paths

// A seeded random, so the maze is the same every run.
let seed = 7
const rand = () => (seed = (seed * 1103515245 + 12345) % 2147483648) / 2147483648

// walls is a set of "x,y" cells; open reads it live.
function grid(walls) { return (x, y) => !walls.has(x + ',' + y) }
const at = (p) => [Math.floor(p.x), Math.floor(p.y)]
function cost(from, path) {
  let c = 0, a = from
  for (const p of path) { c += Math.hypot(p.x - a.x, p.y - a.y); a = p }
  return c
}
// legal: every point open and no diagonal step past a wall corner.
function legal(open, from, path) {
  let a = at(from)
  for (const p of path) {
    const b = at(p)
    if (!open(b[0], b[1])) return false
    if (Math.abs(b[0] - a[0]) > 1 || Math.abs(b[1] - a[1]) > 1) return false
    if (b[0] !== a[0] && b[1] !== a[1] && (!open(b[0], a[1]) || !open(a[0], b[1]))) return false
    a = b
  }
  return true
}

// Heap: pops in key order.
const q = paths.heap()
const keys = []
for (let i = 0; i < 200; i++) { const k = rand(); keys.push(k); q.push(i, k) }
const popped = []
while (q.size()) popped.push(keys[q.pop()])
out.heapSorted = popped.length === 200 && popped.every((k, i) => !i || popped[i - 1] <= k)

// Open ground: a diagonal is five diagonal steps.
let open = grid(new Set())
let m = paths.map(10, 10, 1, open)
let p = m.find(0.5, 0.5, 5.5, 5.5)
out.open = [p.length, p.reached, Math.round(cost({ x: 0.5, y: 0.5 }, p) * 1000) / 1000]
p = m.find(3.5, 3.5, 3.2, 3.9)
out.sameCell = [p.length, p.reached]

// A wall at x = 5 with one gap at the bottom: the way goes through the gap.
let walls = new Set()
for (let y = 0; y < 9; y++) walls.add('5,' + y)
open = grid(walls)
m = paths.map(10, 10, 1, open)
const from = { x: 2.5, y: 2.5 }
p = m.find(2.5, 2.5, 7.5, 2.5)
const f = m.flow()
f.build([{ x: 7.5, y: 2.5 }])
out.around = [p.reached, legal(open, from, p), Math.abs(cost(from, p) - f.dist(2.5, 2.5)) < 1e-9, p.some((q) => at(q)[1] === 9)]

// Following the path, a walker reaches the goal and never stands in a wall.
let walker = { x: 2.5, y: 2.5 }, inWall = false, steps = 0
for (; steps < 1000; steps++) {
  const t = paths.follow(p, walker.x, walker.y, 0.3)
  const d = Math.hypot(t.x - walker.x, t.y - walker.y)
  if (d < 0.01) break
  const s = Math.min(0.1, d)
  walker.x += (t.x - walker.x) / d * s; walker.y += (t.y - walker.y) / d * s
  if (!open(...at(walker))) inWall = true
}
out.followed = [Math.hypot(walker.x - 7.5, walker.y - 2.5) < 0.02, inWall, steps < 1000]

// The map reads the cells live: break the wall and the way is straight.
walls.delete('5,2')
p = m.find(2.5, 2.5, 7.5, 2.5)
out.broken = p.length
f.build([{ x: 7.5, y: 2.5 }])
out.brokenDist = f.dist(2.5, 2.5)

// A shut-in goal: find leads to the closest cell it can reach and says so.
walls = new Set()
for (let x = 6; x <= 8; x++) for (let y = 6; y <= 8; y++) if (x !== 7 || y !== 7) walls.add(x + ',' + y)
open = grid(walls)
m = paths.map(12, 12, 1, open)
p = m.find(1.5, 1.5, 7.5, 7.5)
const last = at(p[p.length - 1])
out.shut = [p.reached, Math.max(Math.abs(last[0] - 7), Math.abs(last[1] - 7)), legal(open, { x: 1.5, y: 1.5 }, p)]
const shutFlow = m.flow()
shutFlow.build([{ x: 7.5, y: 7.5 }])
out.shutFlow = [shutFlow.toward(1.5, 1.5), shutFlow.dist(1.5, 1.5) === Infinity]
out.inWall = m.find(6.5, 6.5, 1.5, 1.5)

// Corners: two walls touching at a corner cannot be slipped between.
open = grid(new Set(['1,0', '0,1']))
m = paths.map(2, 2, 1, open)
out.corner = m.find(0.5, 0.5, 1.5, 1.5).reached

// Budget: a far goal on a big map stops early and still gives a way to go.
m = paths.map(200, 200, 1, grid(new Set()))
p = m.find(0.5, 0.5, 199.5, 199.5, 50)
out.budget = [p.reached, p.length > 0]

// A random maze: A* and the flow field agree on every distance, and A*
// says reached exactly when the flow can get there.
walls = new Set()
for (let x = 0; x < 40; x++) for (let y = 0; y < 40; y++) if (rand() < 0.3) walls.add(x + ',' + y)
open = grid(walls)
m = paths.map(40, 40, 1, open)
const cells = []
for (let x = 0; x < 40; x++) for (let y = 0; y < 40; y++) if (open(x, y)) cells.push([x + 0.5, y + 0.5])
const pick = () => cells[Math.floor(rand() * cells.length)]
let agree = 0, reachable = 0, unreachable = 0, illegal = 0
const mf = m.flow()
for (let i = 0; i < 40; i++) {
  const a = pick(), b = pick()
  mf.build([{ x: b[0], y: b[1] }])
  const pa = m.find(a[0], a[1], b[0], b[1], 1e6)
  const dist = mf.dist(a[0], a[1])
  if (!legal(open, { x: a[0], y: a[1] }, pa)) illegal++
  if (dist === Infinity) { unreachable++; if (!pa.reached) agree++ }
  else { reachable++; if (pa.reached && Math.abs(cost({ x: a[0], y: a[1] }, pa) - dist) < 1e-6) agree++ }
}
out.maze = { agree, reachable, unreachable, illegal }

// Flow walkers on the maze: every one that can reach the target does,
// without standing in a wall.
const goal = pick()
mf.build([{ x: goal[0], y: goal[1] }])
let arrived = 0, sent = 0, walled = 0
for (let i = 0; i < 20; i++) {
  const s = pick()
  if (mf.dist(s[0], s[1]) === Infinity) continue
  sent++
  const w = { x: s[0], y: s[1] }
  for (let n = 0; n < 3000; n++) {
    const t = mf.toward(w.x, w.y)
    const d = Math.hypot(t.x - w.x, t.y - w.y)
    if (d < 0.01) { arrived++; break }
    const st = Math.min(0.1, d)
    w.x += (t.x - w.x) / d * st; w.y += (t.y - w.y) / d * st
    if (!open(...at(w))) { walled++; break }
  }
}
out.walkers = { sent, arrived, walled }

// Several targets: each walker heads for its nearest one.
m = paths.map(20, 5, 1, grid(new Set()))
const two = m.flow()
two.build([{ x: 1.5, y: 2.5 }, { x: 18.5, y: 2.5 }])
out.nearest = [two.toward(4.5, 2.5).x < 4.5, two.toward(15.5, 2.5).x > 15.5, two.dist(4.5, 2.5), two.toward(1.7, 2.1)]

// World units: a map with 10 px cells takes and gives px.
m = paths.map(10, 10, 10, grid(new Set()))
p = m.find(5, 5, 35, 5)
out.units = p.map((q) => q.x)

// follow drops what is reached and keeps the goal.
const route = [{ x: 1, y: 0 }, { x: 2, y: 0 }, { x: 3, y: 0 }]
out.follow = [paths.follow(route, 1, 0, 0.3).x, route.length, paths.follow(route, 2, 0, 0.3).x, route.length, paths.follow(route, 3, 0, 0.3).x, route.length, paths.follow([], 0, 0, 1)]

// apart pushes overlapping things away from each other, equal and opposite,
// ignores the asker and things out of reach, and never gives NaN.
const a = { x: 0, y: 0, r: 0.4 }, b = { x: 0.5, y: 0, r: 0.4 }, far = { x: 9, y: 9, r: 0.4 }
const pa = paths.apart(a, [a, b, far], 0.1), pb = paths.apart(b, [a, b, far], 0.1)
const twin = paths.apart({ x: 1, y: 1, r: 0.4 }, [{ x: 1, y: 1, r: 0.4 }], 0)
out.apart = [pa.x < 0, Math.abs(pa.x + pb.x) < 1e-9, pa.y === 0, Number.isFinite(twin.x) && Math.hypot(twin.x, twin.y) > 0.9]
out.alone = paths.apart(far, [a, b, far], 0.1)

// A flow over 250 by 250 open cells builds in well under a frame budget of
// a few hundred ms, so a rebuild every 0.25s is cheap.
m = paths.map(250, 250, 1, () => true)
const big = m.flow()
const t0 = Date.now()
big.build([{ x: 125, y: 125 }])
out.bigMs = Date.now() - t0
out.bigCorner = Math.round(big.dist(0.5, 0.5) * 100) / 100

console.log(JSON.stringify(out))
`

func TestPathsReferenceCore(t *testing.T) {
	out := jsrun.Run(t, pathsReference, pathsHarness, "")
	t.Logf("%s", out)
	var got struct {
		Globals    []string
		HeapSorted bool
		Open       []any
		SameCell   []any
		Around     []bool
		Followed   []bool
		Broken     int
		BrokenDist float64
		Shut       []any
		ShutFlow   []any
		InWall     any
		Corner     bool
		Budget     []bool
		Maze       struct{ Agree, Reachable, Unreachable, Illegal int }
		Walkers    struct{ Sent, Arrived, Walled int }
		Nearest    []any
		Units      []float64
		Follow     []any
		Apart      []bool
		Alone      map[string]float64
		BigMs      int
		BigCorner  float64
	}
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatalf("decode: %v\n%s", err, out)
	}
	if fmt.Sprint(got.Globals) != "[paths]" {
		t.Errorf("the core declares globals %v, want only paths", got.Globals)
	}
	if !got.HeapSorted {
		t.Error("the heap did not pop in key order")
	}
	if fmt.Sprint(got.Open) != "[5 true 7.071]" || fmt.Sprint(got.SameCell) != "[0 true]" {
		t.Errorf(
			"open ground [steps reached cost] = %v, same cell %v; want [5 true 7.071] and [0 true]",
			got.Open,
			got.SameCell,
		)
	}
	if fmt.Sprint(got.Around) != "[true true true true]" {
		t.Errorf(
			"around the wall [reached legal shortest through-gap] = %v, want all true",
			got.Around,
		)
	}
	if fmt.Sprint(got.Followed) != "[true false true]" {
		t.Errorf("following [arrived in-wall in-time] = %v, want [true false true]", got.Followed)
	}
	if got.Broken != 5 || got.BrokenDist != 5 {
		t.Errorf(
			"after the wall broke: path %d steps, flow %v; want 5 and 5",
			got.Broken,
			got.BrokenDist,
		)
	}
	if fmt.Sprint(got.Shut) != "[false 2 true]" {
		t.Errorf("shut-in goal [reached ring-distance legal] = %v, want [false 2 true]", got.Shut)
	}
	if fmt.Sprint(got.ShutFlow) != "[<nil> true]" || got.InWall != nil {
		t.Errorf(
			"shut-in flow = %v, start in a wall = %v; want [<nil> true] and nil",
			got.ShutFlow,
			got.InWall,
		)
	}
	if got.Corner {
		t.Error("a path slipped between two walls that touch at a corner")
	}
	if fmt.Sprint(got.Budget) != "[false true]" {
		t.Errorf("budget [reached has-way] = %v, want [false true]", got.Budget)
	}
	m := got.Maze
	if m.Agree != 40 || m.Reachable < 10 || m.Illegal != 0 {
		t.Errorf(
			"maze: %+v; want A* and flow to agree on all 40 pairs, at least 10 reachable, none illegal",
			m,
		)
	}
	w := got.Walkers
	if w.Sent < 5 || w.Arrived != w.Sent || w.Walled != 0 {
		t.Errorf("flow walkers: %+v; want every walker sent to arrive and none in a wall", w)
	}
	if fmt.Sprint(got.Nearest) != "[true true 3 map[x:1.5 y:2.5]]" {
		t.Errorf(
			"two targets [left right dist in-cell] = %v, want [true true 3 the target]",
			got.Nearest,
		)
	}
	if fmt.Sprint(got.Units) != "[15 25 35]" {
		t.Errorf("a 10 px map gave %v, want cell centers [15 25 35]", got.Units)
	}
	if fmt.Sprint(got.Follow) != "[2 2 3 1 3 1 <nil>]" {
		t.Errorf("follow = %v, want [2 2 3 1 3 1 <nil>]", got.Follow)
	}
	if fmt.Sprint(got.Apart) != "[true true true true]" || got.Alone["x"] != 0 ||
		got.Alone["y"] != 0 {
		t.Errorf("apart [away equal along-x twins] = %v, alone %v; want all true and no push",
			got.Apart, got.Alone)
	}
	if got.BigMs > 500 || got.BigCorner < 175 || got.BigCorner > 177 {
		t.Errorf(
			"250x250 flow took %d ms, corner dist %v; want under 500 ms and about 176",
			got.BigMs,
			got.BigCorner,
		)
	}
}
