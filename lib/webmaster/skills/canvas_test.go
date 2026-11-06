package skills

import (
	"encoding/json"
	"fmt"
	"testing"

	"github.com/Napam/beaverdream/lib/webmaster/internal/jsrun"
)

// canvasHarness runs the core from canvasReference against a fake 2D
// context that keeps its transform, so the test can see where things land.
const canvasHarness = `
const fs = require('fs')
const vm = require('vm')
const src = fs.readFileSync(process.argv[2], 'utf8')
const out = {}

function fakeCtx(canvas) {
  let m = [1, 0, 0, 1, 0, 0]
  const stack = []
  const ctx = {
    canvas, calls: [], fillStyle: '', globalAlpha: 1, globalCompositeOperation: 'source-over',
    imageSmoothingEnabled: true,
    setTransform(a, b, c, d, e, f) { m = [a, b, c, d, e, f] },
    save() { stack.push(m.slice()) },
    restore() { m = stack.pop() || [1, 0, 0, 1, 0, 0] },
    translate(x, y) { m = [m[0], m[1], m[2], m[3], m[0] * x + m[2] * y + m[4], m[1] * x + m[3] * y + m[5]] },
    scale(sx, sy) { m = [m[0] * sx, m[1] * sx, m[2] * sy, m[3] * sy, m[4], m[5]] },
    at(x, y) { return [m[0] * x + m[2] * y + m[4], m[1] * x + m[3] * y + m[5]] },
    clearRect() {},
    fillRect(x, y, w, h) { this.calls.push(['fillRect', this.fillStyle, x, y, w, h]) },
    drawImage(img, x, y, w, h) { this.calls.push(['drawImage', img, ...this.at(x, y), w, h, this.globalCompositeOperation, this.globalAlpha]) },
    createRadialGradient() { return { addColorStop() {} } },
  }
  return ctx
}
class FakeCanvas {
  constructor(w, h) { this.width = w || 300; this.height = h || 150; this.style = {}; this.ctx = null; this.box = { width: 400, height: 300 } }
  getContext() { return this.ctx || (this.ctx = fakeCtx(this)) }
  getBoundingClientRect() { return this.box }
}
let onResize = null
const fakes = {
  OffscreenCanvas: FakeCanvas,
  devicePixelRatio: 2,
  addEventListener: (name, fn) => { if (name === 'resize') onResize = fn },
}
const ctx = { ...fakes }
vm.createContext(ctx)
vm.runInContext(src, ctx)
out.globals = Object.keys(ctx).filter((k) => !(k in fakes))
const d = ctx.draw2d

// view: the backing store is the CSS box times the pixel ratio, and follows
// a resize. begin draws in CSS px.
const el = new FakeCanvas()
const v = d.view(el, true)
const first = [el.width, el.height, v.w, v.h, el.style.imageRendering]
el.box = { width: 500, height: 200 }
ctx.devicePixelRatio = 3
onResize()
v.begin()
out.view = [...first, el.width, el.height, v.ctx.at(10, 10)[0], v.ctx.imageSmoothingEnabled]
ctx.devicePixelRatio = 2
el.box = { width: 400, height: 300 }
onResize()

// camera: what begin draws at a world point is where toScreen says, and
// toWorld undoes it, at any zoom, position and shake.
let worst = 0, inverse = 0
const cam = d.camera(v)
for (const [x, y, zoom, sx] of [[0, 0, 1, 0], [123.4, -56.7, 2, 0], [-900, 40, 0.5, 3], [7, 7, 3.7, -2]]) {
  cam.x = x; cam.y = y; cam.zoom = zoom; cam.shakeX = sx
  v.begin(); cam.begin()
  const drawn = v.ctx.at(10, 20)
  cam.end()
  const s = cam.toScreen(10, 20)
  worst = Math.max(worst, Math.abs(drawn[0] - s.x * v.ratio), Math.abs(drawn[1] - s.y * v.ratio))
  const back = cam.toWorld(s.x, s.y)
  inverse = Math.max(inverse, Math.abs(back.x - 10), Math.abs(back.y - 20))
}
cam.shakeX = 0
cam.x = 100; cam.y = 50; cam.zoom = 2
const center = cam.toWorld(200, 150)
out.camera = [worst < 1e-9, inverse < 1e-9, center.x, center.y]

// visible covers the screen, corners included, and no more than a cell
// past it.
const vis = cam.visible(10)
out.visible = [vis.x0, vis.y0, vis.x1, vis.y1]

// snap puts the world on whole device pixels.
const snapped = d.camera(v, true)
snapped.x = 10.37; snapped.y = 3.21; snapped.zoom = 3
v.begin(); snapped.begin()
const p = v.ctx.at(0, 0)
snapped.end()
out.snap = [Number.isInteger(Math.round(p[0] * 1e9) / 1e9), Number.isInteger(Math.round(p[1] * 1e9) / 1e9)]

// layer: drawTile works in world units and lands inside its chunk; only
// chunks on screen are drawn, once; mark redraws the chunk and the
// neighbors whose edges touch it.
let tiles = 0, outside = 0
const lay = d.layer(40, 40, 10, (g, x, y) => {
  tiles++
  const [px, py] = g.at(x * 10, y * 10)
  if (px < 0 || py < 0 || px >= g.canvas.width || py >= g.canvas.height) outside++
}, 2)
const view2 = d.camera(v)
view2.x = 60; view2.y = 60; view2.zoom = 1  // sees world 0..260 by 0..210
v.begin(); view2.begin(); lay.draw(v.ctx, view2); view2.end()
const firstBuilds = lay.builds, firstTiles = tiles
v.begin(); view2.begin(); lay.draw(v.ctx, view2); view2.end()
const again = lay.builds - firstBuilds
lay.mark(8, 8)
v.begin(); view2.begin(); lay.draw(v.ctx, view2); view2.end()
const marked = lay.builds - firstBuilds
lay.mark(3, 3)
v.begin(); view2.begin(); lay.draw(v.ctx, view2); view2.end()
out.layer = [firstBuilds, firstTiles, outside, again, marked, lay.builds - firstBuilds - marked, v.ctx.calls.filter((c) => c[0] === 'drawImage').length > 0]

// A big map keeps at most 96 chunks while the camera crosses it.
const huge = d.layer(800, 800, 10, () => {}, 1)
const fly = d.camera(v)
let most = 0
for (let x = 0; x < 8000; x += 200) {
  fly.x = x; fly.y = x
  huge.draw(v.ctx, fly)
  most = Math.max(most, huge.cached())
}
out.evict = [most <= 96, most > 10]

// autotile: a plus shape of five tiles.
const plus = new Set(['5,5', '5,4', '6,5', '5,6', '4,5'])
const same = (x, y) => plus.has(x + ',' + y)
out.autotile = [d.autotile(same, 5, 5), d.autotile(same, 5, 4), d.autotile(same, 6, 5), d.autotile(same, 9, 9)]

// sprite: each character is a scaled pixel of its color, unknown ones are
// clear, mirror flips it, solid recolors it.
const rows = ['K..', 'KY.']
const pal = { K: '#111', Y: '#ff0' }
const sp = d.sprite(rows, pal, 2)
const mir = d.sprite(rows, pal, 2, true)
const flash = d.sprite(rows, d.solid(pal, '#fff'), 2)
const rects = (s) => s.getContext().calls.map((c) => c.slice(1).join(' '))
out.sprite = [sp.width, sp.height, ...rects(sp)]
out.mirror = rects(mir)
out.flash = [...new Set(flash.getContext().calls.map((c) => c[1]))]

// parallax: copies cover the screen, spaced by width, and a deeper layer
// moves further with the camera.
const copies = { far: [], near: [] }
const par = d.camera(v)
par.x = 1000; par.y = 0
d.parallax(v, par, [
  { depth: 0.2, width: 300, draw: (c, x) => copies.far.push(x) },
  { depth: 1, width: 300, draw: (c, x) => copies.near.push(x) },
])
out.parallax = [copies.far[0] <= 0, copies.far[copies.far.length - 1] + 300 >= v.w, copies.far[1] - copies.far[0], copies.far[0], copies.near[0]]

// ysort puts the lower thing in front.
out.ysort = d.ysort([{ id: 'a', y: 5 }, { id: 'b', y: 1 }, { id: 'c', y: 3 }]).map((t) => t.id)

// light: day draws nothing, night draws the half-size layer over the
// screen, and glows cut it out.
const lit = d.light(v)
v.ctx.calls.length = 0
lit.begin(0); lit.glow(10, 10, 20); lit.draw()
const day = v.ctx.calls.length
lit.begin(0.8); lit.glow(100, 80, 40); lit.draw()
const night = v.ctx.calls.filter((c) => c[0] === 'drawImage')
const layerImg = night[0] && night[0][1]
const cut = layerImg.getContext().calls.filter((c) => c[0] === 'drawImage')
out.light = [day, night.length, layerImg.width, layerImg.height, cut.length, cut[0][6], cut[0][2], cut[0][4]]

console.log(JSON.stringify(out))
`

func TestCanvasReferenceCore(t *testing.T) {
	out := jsrun.Run(t, canvasReference, canvasHarness, "")
	t.Logf("%s", out)
	var got struct {
		Globals  []string
		View     []any
		Camera   []any
		Visible  []int
		Snap     []bool
		Layer    []any
		Evict    []bool
		Autotile []int
		Sprite   []any
		Mirror   []string
		Flash    []string
		Parallax []any
		Ysort    []string
		Light    []any
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
	check("globals", got.Globals, "[draw2d]")
	check("view [w h cssW cssH rendering, after resize w h, css px 10 lands at, smoothing]",
		got.View, "[800 600 400 300 pixelated 1500 600 30 false]")
	check(
		"camera [drawn matches toScreen, toWorld inverts, center x y]",
		got.Camera,
		"[true true 100 50]",
	)
	check("visible [x0 y0 x1 y1]", got.Visible, "[0 -3 20 12]")
	check("snap [whole x whole y]", got.Snap, "[true true]")
	check(
		"layer [built tiles outside again marked unmarked-redraw drew]",
		got.Layer,
		"[12 768 0 0 4 1 true]",
	)
	check("evict [at most 96, more than 10]", got.Evict, "[true true]")
	check("autotile [center top right lone]", got.Autotile, "[15 4 8 0]")
	check("sprite [w h rects]", got.Sprite, "[6 4 #111 0 0 2 2 #111 0 2 2 2 #ff0 2 2 2 2]")
	check("mirror", got.Mirror, "[#111 4 0 2 2 #111 4 2 2 2 #ff0 2 2 2 2]")
	check("flash colors", got.Flash, "[#fff]")
	check(
		"parallax [covers left, covers right, spacing, far start, near start]",
		got.Parallax,
		"[true true 300 -200 -100]",
	)
	check("ysort", got.Ysort, "[b c a]")
	check(
		"light [day calls, night draws, w h, cuts, mode, x, w]",
		got.Light,
		"[0 1 200 150 1 destination-out 30 40]",
	)
}
