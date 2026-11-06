package webmaster

import (
	"encoding/json"
	"fmt"
	"testing"
)

// spaceHarness runs the core from spaceReference with frames driven by hand and
// reports what the numbers did.
const spaceHarness = `
const fs = require('fs')
const vm = require('vm')
const src = fs.readFileSync(process.argv[2], 'utf8')
const out = {}

const ctx = {}
vm.createContext(ctx)
vm.runInContext(src, ctx)
out.globals = Object.keys(ctx)
const space = ctx.space

// 2D: 20 by 10 cells of 10 px, a wall at column 10 and another at column 19.
let g = space.grid(20, 10, 1, 10)
g.fill(10, 0, 0, 10, 9, 0, 1)
g.fill(19, 0, 0, 19, 9, 0, 1)
let hit = g.ray(5, 55, 0, 1, 0, 0, 1000)
out.wall = hit && [hit.x, hit.y, hit.t, hit.n[0], hit.px]
out.short = g.ray(5, 55, 0, 1, 0, 0, 50)
out.back = g.ray(5, 55, 0, -1, 0, 0, 1000)
out.boxBefore = g.boxHits({ x: 96, y: 51, w: 8, h: 8 })
out.seesBefore = g.sees({ x: 5, y: 55 }, { x: 150, y: 55 })
g.takeDirty()

// A carve of radius 15 at the wall opens three cells, and the ray, the box
// and line of sight all pass through the same hole.
g.save()
out.broke = g.carve(105, 55, 0, 15)
out.dirty = g.takeDirty()
hit = g.ray(5, 55, 0, 1, 0, 0, 1000)
out.through = hit && [hit.x, hit.t]
out.boxAfter = g.boxHits({ x: 96, y: 51, w: 8, h: 8 })
out.seesAfter = g.sees({ x: 5, y: 55 }, { x: 150, y: 55 })
out.edgeKept = [g.get(10, 3), g.get(10, 7)]
g.restore()
out.restored = g.get(10, 5)
out.restoreDirty = g.takeDirty().length > 0

// Hit points: a cell with 3 takes three hits of power 1.
g.set(5, 5, 0, 3)
out.hp = [g.carve(55, 55, 0, 1, 1), g.get(5, 5), g.carve(55, 55, 0, 1, 1), g.carve(55, 55, 0, 1, 1), g.get(5, 5)]

// Diagonal: a ray at 45 degrees reaches a lone cell at its corner.
g = space.grid(10, 10, 1, 10)
g.set(5, 5, 0, 1)
hit = g.ray(5, 5, 0, 1, 1, 0, 1000)
out.diagonal = hit && [hit.x, hit.y, hit.t]

// 3D: a floor at y = 0, a ray straight down from y = 5 lands on its top face.
g = space.grid(10, 10, 10, 1)
g.fill(0, 0, 0, 9, 0, 9, 1)
hit = g.ray(5.5, 5, 5.5, 0, -1, 0, 100)
out.floor = hit && [hit.y, hit.t, hit.n[1], hit.py]
out.zero = g.ray(1, 1, 1, 0, 0, 0, 10)
// Without a finite max the ray stops at the far side: it ends when it leaves
// the grid and still finds what is in reach.
out.endless = g.ray(5.5, 5, 5.5, 1, 0, 0, Infinity)
hit = g.ray(5.5, 5, 5.5, 0, -1, 0)
out.noMax = hit && hit.t
// Hit points stay in 0..255 and a negative power never heals.
g.set(2, 5, 2, 300); g.set(3, 5, 3, -5)
out.clamped = [g.get(2, 5, 2), g.get(3, 5, 3)]
g.set(4, 5, 4, 3); g.carve(4.5, 5.5, 4.5, 0.4, -10)
out.noHeal = g.get(4, 5, 4)
out.carve3d = g.carve(5, 0.5, 5, 1.2)

// Hash: only things that touch the query circle come back.
const h = space.hash(10)
const things = [
  { id: 'a', x: 1, y: 1, r: 1 },
  { id: 'b', x: 8, y: 1, r: 1 },
  { id: 'c', x: 12, y: 1, r: 3 },
  { id: 'd', x: 40, y: 40, r: 1 },
]
things.forEach(h.add)
const found = []
h.near(1, 1, 0, 8, (e) => found.push(e.id))
out.near = found.sort()
h.clear()
const none = []
h.near(1, 1, 0, 5, (e) => none.push(e.id))
out.cleared = none.length

console.log(JSON.stringify(out))
`

func TestSpaceReferenceCore(t *testing.T) {
	out := runJS(t, spaceReference, spaceHarness, "")
	t.Logf("%s", out)
	var got struct {
		Globals                        []string
		Wall, Through, Diagonal, Floor []float64
		Short, Back, Zero              any
		BoxBefore, BoxAfter            bool
		SeesBefore, SeesAfter          bool
		Broke, Restored, Carve3d       int
		Endless                        any
		NoMax                          float64
		Clamped                        []int
		NoHeal                         int
		Dirty                          []string
		EdgeKept, Hp                   []int
		RestoreDirty                   bool
		Near                           []string
		Cleared                        int
	}
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatalf("decode: %v\n%s", err, out)
	}
	if len(got.Globals) != 1 || got.Globals[0] != "space" {
		t.Errorf("the core declares globals %v, want only space", got.Globals)
	}
	want := func(name string, have, exp []float64) {
		t.Helper()
		if len(have) != len(exp) {
			t.Errorf("%s = %v, want %v", name, have, exp)
			return
		}
		for i := range exp {
			if d := have[i] - exp[i]; d > 0.01 || d < -0.01 {
				t.Errorf("%s = %v, want %v", name, have, exp)
				return
			}
		}
	}
	want("ray at the wall [x y t nx px]", got.Wall, []float64{10, 5, 95, -1, 100})
	want("ray through the hole [x t]", got.Through, []float64{19, 185})
	want("diagonal ray [x y t]", got.Diagonal, []float64{5, 5, 63.64})
	want("3D ray down [y t ny py]", got.Floor, []float64{0, 4, 1, 1})
	if got.Short != nil || got.Back != nil || got.Zero != nil {
		t.Errorf(
			"short %v, backwards %v, zero direction %v; want all clear",
			got.Short,
			got.Back,
			got.Zero,
		)
	}
	if !got.BoxBefore || got.BoxAfter || got.SeesBefore || !got.SeesAfter {
		t.Errorf("box hits %v then %v, sees %v then %v; want the hole open to both",
			got.BoxBefore, got.BoxAfter, got.SeesBefore, got.SeesAfter)
	}
	if got.Broke != 3 || len(got.Dirty) != 1 || got.Dirty[0] != "1,0,0" {
		t.Errorf("carve broke %d cells in chunks %v, want 3 in 1,0,0", got.Broke, got.Dirty)
	}
	if len(got.EdgeKept) != 2 || got.EdgeKept[0] != 1 || got.EdgeKept[1] != 1 {
		t.Errorf("cells outside the radius = %v, want kept", got.EdgeKept)
	}
	if got.Restored != 1 || !got.RestoreDirty {
		t.Errorf("restore gave %d and dirty %v, want 1 true", got.Restored, got.RestoreDirty)
	}
	if fmt.Sprint(got.Hp) != "[0 2 0 1 0]" {
		t.Errorf("three hits on a 3 hp cell = %v, want [0 2 0 1 0]", got.Hp)
	}
	if got.Endless != nil || got.NoMax != 4 {
		t.Errorf("a ray with no finite max = %v and %v, want null off the grid and 4 to the floor",
			got.Endless, got.NoMax)
	}
	if fmt.Sprint(got.Clamped) != "[255 0]" || got.NoHeal != 3 {
		t.Errorf("hp set to 300 and -5 = %v, negative carve left %d; want [255 0] and 3",
			got.Clamped, got.NoHeal)
	}
	if got.Carve3d != 4 {
		t.Errorf("3D carve broke %d floor cells, want 4", got.Carve3d)
	}
	if fmt.Sprint(got.Near) != "[a b c]" || got.Cleared != 0 {
		t.Errorf("hash near = %v, after clear %d; want [a b c] and 0", got.Near, got.Cleared)
	}
}
