package skills

import (
	"encoding/json"
	"fmt"
	"testing"

	"github.com/Napam/beaverdream/lib/webmaster/internal/jsrun"
)

// randomHarness runs the core from randomReference and reports what the
// generators made.
const randomHarness = `
const fs = require('fs')
const vm = require('vm')
const src = fs.readFileSync(process.argv[2], 'utf8')
const out = {}

const ctx = { Math, Date, Uint8Array, Int32Array, String }
vm.createContext(ctx)
vm.runInContext(src, ctx)
out.globals = Object.keys(ctx).filter((k) => !['Math', 'Date', 'Uint8Array', 'Int32Array', 'String'].includes(k))
const seeded = ctx.seeded
const series = (r, n) => Array.from({ length: n }, () => r())

// hash: stable, and a thousand names give a thousand seeds.
const seeds = new Set()
for (let i = 0; i < 1000; i++) seeds.add(seeded.hash('player' + i))
out.hash = [seeded.hash('abc') === seeded.hash('abc'), seeds.size, seeded.hash(42) === seeded.hash('42')]

// rng: the same seed repeats, another seed does not, draws stay in [0, 1).
const a = series(seeded.rng('world:3'), 1000), b = series(seeded.rng('world:3'), 1000)
const c = series(seeded.rng('world:4'), 1000)
const mean = a.reduce((s, v) => s + v, 0) / a.length
out.rng = [
  a.every((v, i) => v === b[i]),
  a.filter((v, i) => v === c[i]).length,
  a.every((v) => v >= 0 && v < 1),
  Math.abs(mean - 0.5) < 0.05,
]

// int covers both ends and nothing outside them.
let r = seeded.rng(1)
const ints = new Set(Array.from({ length: 2000 }, () => r.int(1, 6)))
out.ints = [...ints].sort()

// weighted follows the weights and skips names without one.
r = seeded.rng(2)
const tally = { common: 0, rare: 0, never: 0 }
for (let i = 0; i < 10000; i++) tally[r.weighted({ common: 3, rare: 1, never: 0 })]++
out.weighted = [Math.abs(tally.common - 7500) < 300, tally.never, r.weighted({ a: 0 }), r.weighted({})]

// shuffle is a permutation, and the same seed shuffles the same way.
const deck = seeded.rng(5).shuffle(Array.from({ length: 52 }, (_, i) => i))
const again = seeded.rng(5).shuffle(Array.from({ length: 52 }, (_, i) => i))
out.shuffle = [[...deck].sort((x, y) => x - y).every((v, i) => v === i), deck.join() === again.join(), deck.join() !== again.slice().sort((x, y) => x - y).join()]

// fork depends only on the seed and the name, not on draws made before.
const quiet = seeded.rng('s'), busy = seeded.rng('s')
series(busy, 50)
out.fork = [quiet.fork('walls')() === busy.fork('walls')(), quiet.fork('walls')() !== quiet.fork('loot')()]

// day is a date and moves with the offset.
out.day = [/^\d{4}-\d{2}-\d{2}$/.test(seeded.day()), seeded.day() !== seeded.day(1), seeded.day() === seeded.day(0)]

// noise: the same seed repeats, values stay in -1..1 and change smoothly.
const n = seeded.noise('land'), m = seeded.noise('land'), o = seeded.noise('sea')
let lo = 0, hi = 0, jump = 0, same = true, differ = 0
for (let y = 0; y < 60; y++) for (let x = 0; x < 60; x++) {
  for (const oct of [1, 4]) {
    const v = n(x / 7.3, y / 7.3, oct)
    lo = Math.min(lo, v); hi = Math.max(hi, v)
    if (v !== m(x / 7.3, y / 7.3, oct)) same = false
    if (Math.abs(v - o(x / 7.3, y / 7.3, oct)) > 1e-9) differ++
  }
  jump = Math.max(jump, Math.abs(n(x / 7.3, y / 7.3) - n(x / 7.3 + 0.01, y / 7.3)))
}
out.noise = [same, differ > 6000, lo >= -1 && hi <= 1, lo < -0.3 && hi > 0.3, jump < 0.05]

// reach counts the open cells reachable from (sx, sy) and all open cells.
function reach(area, sx, sy) {
  const seen = new Set([sx + ',' + sy]), todo = [[sx, sy]]
  while (todo.length) {
    const [x, y] = todo.pop()
    for (const [dx, dy] of [[1, 0], [-1, 0], [0, 1], [0, -1]]) {
      const k = (x + dx) + ',' + (y + dy)
      if (!area.wall(x + dx, y + dy) && !seen.has(k)) { seen.add(k); todo.push([x + dx, y + dy]) }
    }
  }
  let open = 0
  for (let y = 0; y < area.h; y++) for (let x = 0; x < area.w; x++) if (!area.wall(x, y)) open++
  return [seen.size, open]
}
function borderWalled(area) {
  for (let x = 0; x < area.w; x++) if (!area.wall(x, 0) || !area.wall(x, area.h - 1)) return false
  for (let y = 0; y < area.h; y++) if (!area.wall(0, y) || !area.wall(area.w - 1, y)) return false
  return true
}

// maze: a tree over every room, so open cells are rooms plus one fewer
// passage; loops add passages and keep it whole.
const mz = seeded.maze(15, 10, seeded.rng('m'))
const mz2 = seeded.maze(15, 10, seeded.rng('m'))
const looped = seeded.maze(15, 10, seeded.rng('m'), 0.2)
out.maze = [mz.w, mz.h, ...reach(mz, 1, 1), borderWalled(mz), mz.cells.join() === mz2.cells.join()]
const lr = reach(looped, 1, 1)
out.loops = [lr[0] === lr[1], lr[1] > 299, borderWalled(looped)]

// rooms: apart from each other, inside the edge, all joined.
let joined = 0, apart = true, inside = true
for (let s = 0; s < 20; s++) {
  const d = seeded.rooms(60, 40, seeded.rng('d' + s), 9, 4, 9)
  const [got, open] = reach(d, d.rooms[0].cx, d.rooms[0].cy)
  if (got === open && d.rooms.length >= 4) joined++
  if (!borderWalled(d)) inside = false
  for (const p of d.rooms) for (const q of d.rooms)
    if (p !== q && !(p.x > q.x + q.w || q.x > p.x + p.w || p.y > q.y + q.h || q.y > p.y + p.h)) apart = false
}
out.rooms = [joined, apart, inside, seeded.rooms(10, 10, seeded.rng(1), 5, 20, 30).rooms.length]

// cave: one region, walled edge, a fair share open, the same each time.
let whole = 0, share = 1
for (let s = 0; s < 20; s++) {
  const cv = seeded.cave(80, 50, seeded.rng('c' + s))
  let sx = -1, sy = -1
  for (let y = 0; y < cv.h && sx < 0; y++) for (let x = 0; x < cv.w; x++) if (!cv.wall(x, y)) { sx = x; sy = y; break }
  const [got, open] = reach(cv, sx, sy)
  if (got === open && borderWalled(cv)) whole++
  share = Math.min(share, open / (80 * 50))
}
out.cave = [whole, share > 0.2, seeded.cave(40, 30, seeded.rng(9)).cells.join() === seeded.cave(40, 30, seeded.rng(9)).cells.join()]

// scatter: in bounds, never closer than the gap, few holes, ok respected.
const pts = seeded.scatter(100, 100, 5, seeded.rng('p'), (x) => x < 50 || x > 60)
let close = 0, outside = 0, refused = 0
for (let i = 0; i < pts.length; i++) {
  const p = pts[i]
  if (p.x < 0 || p.y < 0 || p.x >= 100 || p.y >= 100) outside++
  if (p.x >= 50 && p.x <= 60) refused++
  for (let j = i + 1; j < pts.length; j++) if (Math.hypot(p.x - pts[j].x, p.y - pts[j].y) < 5) close++
}
let holes = 0
for (let y = 2.5; y < 100; y += 5) for (let x = 2.5; x < 100; x += 5) {
  if (x >= 45 && x <= 65) continue
  if (!pts.some((p) => Math.hypot(p.x - x, p.y - y) < 10)) holes++
}
const pts2 = seeded.scatter(100, 100, 5, seeded.rng('p'), (x) => x < 50 || x > 60)
out.scatter = [pts.length > 200, close, outside, refused, holes, pts.length === pts2.length && pts.every((p, i) => p.x === pts2[i].x)]

console.log(JSON.stringify(out))
`

func TestRandomReferenceCore(t *testing.T) {
	out := jsrun.Run(t, randomReference, randomHarness, "")
	t.Logf("%s", out)
	var got struct {
		Globals  []string
		Hash     []any
		Rng      []any
		Ints     []int
		Weighted []any
		Shuffle  []bool
		Fork     []bool
		Day      []bool
		Noise    []bool
		Maze     []any
		Loops    []bool
		Rooms    []any
		Cave     []any
		Scatter  []any
	}
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatalf("decode: %v\n%s", err, out)
	}
	check := func(name string, have any, want string) {
		t.Helper()
		if s := fmt.Sprint(have); s != want {
			t.Errorf("%s = %s, want %s", name, s, want)
		}
	}
	check("globals", got.Globals, "[seeded]")
	check("hash [stable distinct number-as-text]", got.Hash, "[true 1000 true]")
	check("rng [repeats same-as-other in-range fair]", got.Rng, "[true 0 true true]")
	check("int 1..6", got.Ints, "[1 2 3 4 5 6]")
	check("weighted [three-to-one never zero empty]", got.Weighted, "[true 0 <nil> <nil>]")
	check("shuffle [permutation repeats moved]", got.Shuffle, "[true true true]")
	check("fork [ignores draws differs by name]", got.Fork, "[true true]")
	check("day [format moves zero]", got.Day, "[true true true]")
	check(
		"noise [repeats seeds-differ in-range spread smooth]",
		got.Noise,
		"[true true true true true]",
	)
	check("maze [w h reached open walled repeats]", got.Maze, "[31 21 299 299 true true]")
	check("loops [whole more walled]", got.Loops, "[true true true]")
	check("rooms [joined-of-20 apart inside too-big]", got.Rooms, "[20 true true 0]")
	check("cave [whole-of-20 share repeats]", got.Cave, "[20 true true]")
	check("scatter [many close outside refused holes repeats]", got.Scatter, "[true 0 0 0 0 true]")
}
