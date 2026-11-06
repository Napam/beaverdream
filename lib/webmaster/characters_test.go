package webmaster

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"testing"
)

// charactersHarness runs the core from charactersReference with the vendored
// three.js and drives every plan through scripted movement.
const charactersHarness = `
const fs = require('fs')
const vm = require('vm')
const { pathToFileURL } = require('url')

;(async () => {
const THREE = await import(pathToFileURL(process.argv[3]).href)
const src = fs.readFileSync(process.argv[2], 'utf8')
const ctx = {}
vm.createContext(ctx)
vm.runInContext(src, ctx)
const out = { globals: Object.keys(ctx) }
const kit = ctx.characterKit(THREE)
const DT = 1 / 60, v = new THREE.Vector3()

function world(obj) { obj.updateWorldMatrix(true, false); return obj.getWorldPosition(new THREE.Vector3()) }
// local turns a world point into the character's own frame: x left, z forward.
function local(r, p) { return r.root.children[0].worldToLocal(p.clone()) }
function corr(a, b) {
  const ma = a.reduce((s, x) => s + x, 0) / a.length, mb = b.reduce((s, x) => s + x, 0) / b.length
  let ab = 0, aa = 0, bb = 0
  for (let i = 0; i < a.length; i++) { ab += (a[i] - ma) * (b[i] - mb); aa += (a[i] - ma) ** 2; bb += (b[i] - mb) ** 2 }
  return ab / Math.sqrt(aa * bb)
}
// bounds is the box around a figure's meshes, without the outline shells.
function bounds(obj) {
  obj.updateMatrixWorld(true)
  const box = new THREE.Box3()
  obj.traverse((o) => {
    if (!o.isMesh || o.material.side === THREE.BackSide) return
    const pos = o.geometry.attributes.position
    for (let i = 0; i < pos.count; i++) box.expandByPoint(v.fromBufferAttribute(pos, i).applyMatrix4(o.matrixWorld))
  })
  return box
}
function meshes(obj) { let n = 0; obj.traverse((o) => { if (o.isMesh) n++ }); return n }
function finite(r) {
  let ok = true
  r.root.traverse((o) => { for (const k of ['x', 'y', 'z']) if (!Number.isFinite(o.rotation[k]) || !Number.isFinite(o.position[k])) ok = false })
  return ok
}

// walk moves the root along its facing at speed for seconds and records the
// feet, without telling pose anything, the way a remote player is posed.
function walk(r, speed, seconds, yaw, rec) {
  r.root.rotation.y = yaw
  for (let f = 0; f < seconds * 60; f++) {
    r.root.position.x += -Math.sin(yaw) * speed * DT
    r.root.position.z += -Math.cos(yaw) * speed * DT
    r.pose(DT)
    r.root.updateMatrixWorld(true)
    if (rec) rec(f)
  }
}

// Every plan: the height asked for, standing on the ground, finite.
out.plans = {}
for (const plan of kit.plans) {
  const r = kit.rig({ plan, height: 2, extras: ['cape', 'hat', 'ears', 'horns', 'tail', 'wings'] })
  for (let i = 0; i < 30; i++) r.pose(DT, { grounded: true })
  r.root.updateMatrixWorld(true)
  const r2 = kit.rig({ plan, height: 2, extras: [] })
  r2.pose(DT)
  r2.root.updateMatrixWorld(true)
  const plain = bounds(r2.root)
  const soles = r.legs.map((l) => world(l.sole).y)
  out.plans[plan] = [
    plain.max.y > 1.7 && plain.max.y < 2.25,
    plain.min.y > -0.01 && plain.min.y < 0.05,
    soles.length === 0 || Math.abs(Math.min(...soles)) < 0.01,
    finite(r),
    meshes(r.root) > meshes(r2.root),
  ]
}
out.ghost = (() => { const g = kit.rig({ plan: 'flyer', legs: 0 }); g.pose(DT); g.root.updateMatrixWorld(true); return bounds(g.root).min.y > 0.2 })()

// Legged plans walking: the lowest foot is on the ground every frame, no
// foot goes under it, and a planted foot does not slide.
out.walk = {}
const gaits = [['biped', {}, 1.2], ['biped run', {}, 6], ['biped back', {}, -1], ['quadruped', {}, 1.2],
  ['quadruped run', {}, 7], ['bug', { legs: 8 }, 0.8], ['flyer', {}, 1.2]]
for (const [name, extra, speed] of gaits) {
  const r = kit.rig({ plan: name.split(' ')[0], height: 1.8, ...extra })
  walk(r, 0, 0.5, 0.7)
  walk(r, speed, 1, 0.7)
  let lowest = 0, under = 0, slide = 0, flight = 0
  const planted = r.legs.map(() => null)
  walk(r, speed, 3, 0.7, () => {
    const ys = r.legs.map((l) => world(l.sole))
    const low = Math.min(...ys.map((p) => p.y))
    lowest = Math.max(lowest, Math.abs(low))
    if (low > 0.01) flight++
    under = Math.min(under, ...ys.map((p) => p.y))
    ys.forEach((p, i) => {
      if (p.y < 0.002) {
        if (planted[i]) slide = Math.max(slide, Math.hypot(p.x - planted[i].x, p.z - planted[i].z))
        else planted[i] = p.clone()
      } else planted[i] = null
    })
  })
  // A walk always has a foot down; a run has moments with none.
  out.walk[name] = [name.endsWith('run') ? flight > 10 : lowest < 0.004, under > -0.004, slide < 0.02]
}

// Gaits: a biped's legs alternate and its arms swing against them; a
// quadruped trots in diagonal pairs; the body bobs twice per cycle.
{
  const r = kit.rig({ plan: 'biped' })
  walk(r, 1.2, 1, 0)
  const u0 = [], u1 = [], a0 = [], ys = []
  let wraps = 0, lastCycle = r.motion.cycle
  walk(r, 1.2, 3, 0, () => {
    const m = r.motion
    u0.push(m.legs[0].u); u1.push(m.legs[1].u); a0.push(m.arms[0].swing); ys.push(r.joints.body.position.y)
    if (m.cycle < lastCycle) wraps++
    lastCycle = m.cycle
  })
  let dips = 0
  for (let i = 1; i < ys.length - 1; i++) if (ys[i] < ys[i - 1] && ys[i] <= ys[i + 1]) dips++
  out.biped = [corr(u0, u1) < -0.9, corr(a0, u1) > 0.9, wraps >= 2, Math.abs(dips - 2 * wraps) <= 1]

  const q = kit.rig({ plan: 'quadruped' })
  walk(q, 1.5, 1, 0)
  const legs = [[], [], [], []]
  walk(q, 1.5, 2, 0, () => q.motion.legs.forEach((l, i) => legs[i].push(l.u)))
  // legs: front left, front right, back left, back right
  out.trot = [corr(legs[0], legs[3]) > 0.9, corr(legs[1], legs[2]) > 0.9, corr(legs[0], legs[1]) < -0.9]
}

// Lean: running forward leans the head ahead of the hips; turning left
// leans into the turn, to the left.
{
  const r = kit.rig({ plan: 'biped' })
  walk(r, 6, 1.5, 0)
  const ahead = local(r, world(r.joints.head)).z - local(r, world(r.joints.body)).z
  let yaw = 0
  for (let f = 0; f < 90; f++) {
    yaw += 1.5 * DT
    r.root.rotation.y = yaw
    r.root.position.x += -Math.sin(yaw) * 6 * DT
    r.root.position.z += -Math.cos(yaw) * 6 * DT
    r.pose(DT)
  }
  r.root.updateMatrixWorld(true)
  const left = local(r, world(r.joints.head)).x - local(r, world(r.joints.body)).x
  out.lean = [ahead > 0.05, left > 0.05]
}

// Jump and land: legs tuck while rising, the body dips on landing and
// comes back up.
{
  const r = kit.rig({ plan: 'biped' })
  for (let i = 0; i < 60; i++) r.pose(DT, { grounded: true })
  const rest = r.joints.body.position.y
  let vy = 7, tuck = 0, dip = rest, after = 0
  for (let f = 0; f < 150; f++) {
    const air = r.root.position.y > 0 || vy > 0
    if (air) { vy -= 20 * DT; r.root.position.y = Math.max(0, r.root.position.y + vy * DT) }
    r.pose(DT, { grounded: !air })
    r.root.updateMatrixWorld(true)
    if (air && vy > 2) tuck = Math.max(tuck, Math.min(...r.legs.map((l) => world(l.sole).y)) - r.root.position.y)
    if (!air) dip = Math.min(dip, r.joints.body.position.y)
    after = r.joints.body.position.y
  }
  out.jump = [tuck > 0.1, rest - dip > 0.05, Math.abs(after - rest) < 0.01]
}

// A remote jump, posed from the root alone: legs tuck in the air, the body
// dips on landing. A flyer cruising above the ground keeps its wings open.
{
  const r = kit.rig({ plan: 'biped' })
  for (let i = 0; i < 60; i++) r.pose(DT)
  const rest = r.joints.body.position.y
  let vy = 7, y = 0, air = 1, dip = rest
  for (let f = 0; f < 120; f++) {
    if (y > 0 || vy > 0) { vy -= 20 * DT; y = Math.max(0, y + vy * DT) }
    r.root.position.y = y
    r.pose(DT)
    if (y > 0.9) air = Math.min(air, r.motion.air)
    if (y === 0) dip = Math.min(dip, r.joints.body.position.y)
  }
  const bird = kit.rig({ plan: 'flyer' })
  bird.root.position.y = 4
  walk(bird, 3, 1, 0)
  out.remote = [air > 0.5, rest - dip > 0.05, bird.motion.air > 0.9]
}

// Blends: idle, walk, run, jump, land, stop. Run at 240 and 480 frames a
// second: a joint in motion moves half as far per frame at twice the rate,
// a snap moves the same. A teleport and odd frame times stay finite.
function blend(fps) {
  const r = kit.rig({ plan: 'biped', extras: ['cape', 'tail'] })
  const joints = [r.joints.chest, r.joints.head, ...r.legs.flatMap((l) => [l.thigh, l.knee]), ...r.arms.flatMap((a) => [a.shoulder, a.elbow])]
  let prev = null, worst = 0, y = 0, vy = 0
  const dt = 1 / fps
  for (const [seconds, speed, ground] of [[1, 0, true], [1, 1.2, true], [1, 4, true], [1, 7, true], [0.7, 7, false], [1, 3, true], [1, 0, true]]) {
    if (!ground) vy = 6
    for (let f = 0; f < seconds * fps; f++) {
      if (!ground || y > 0) { vy -= 20 * dt; y = Math.max(0, y + vy * dt) }
      r.root.position.z -= speed * dt
      r.root.position.y = y
      r.pose(dt, { grounded: y === 0 && vy <= 0 })
      const now = joints.map((j) => [j.rotation.x, j.rotation.z])
      if (prev) now.forEach((a, i) => { worst = Math.max(worst, Math.abs(a[0] - prev[i][0]), Math.abs(a[1] - prev[i][1])) })
      prev = now
    }
  }
  return { r, worst }
}
{
  const a = blend(240), b = blend(480), r = a.r
  r.root.position.set(900, 0, -400)
  r.pose(DT); r.pose(0); r.pose(5); r.pose(NaN)
  out.blend = [b.worst < 0.65 * a.worst, a.worst < 0.3, finite(r), Math.abs(r.motion.speed) < 1]
}

// shapes and parts: a swapped part keeps its size and place, the figure
// keeps its height and feet, and every plan names its parts.
{
  const size = (m) => { m.updateWorldMatrix(true, false); return bounds(m).getSize(new THREE.Vector3()) }
  const a = kit.rig({ plan: 'biped', extras: [] }), b = kit.rig({ plan: 'biped', extras: [], shapes: { head: 'box', torso: 'box', foot: 'ball', nope: 'box', hand: 'star' } })
  a.pose(DT); b.pose(DT)
  const sa = size(a.parts.head[0]), sb = size(b.parts.head[0]), ta = size(a.parts.torso[0]), tb = size(b.parts.torso[0])
  const same = (p, q) => Math.abs(p.x - q.x) < 1e-3 && Math.abs(p.y - q.y) < 1e-3 && Math.abs(p.z - q.z) < 1e-3
  const boxed = b.parts.head[0].geometry !== a.parts.head[0].geometry && b.parts.hand[0].geometry === a.parts.hand[0].geometry
  const ha = bounds(a.root), hb = bounds(b.root)
  const c = kit.rig({ plan: 'quadruped', extras: ['tail'] })
  c.pose(DT)
  const named = kit.plans.every((plan) => { const r = kit.rig({ plan }); return r.parts.torso && r.parts.torso.length > 0 })
  out.shapes = [same(sa, sb), same(ta, tb), boxed, Math.abs(ha.max.y - hb.max.y) < 0.02, Math.abs(hb.min.y) < 0.01,
    named, c.parts.tail.length === 3]
}

// look: the same seed gives the same look, colors come from the palette,
// extras from the list, and seeds differ.
{
  const pal = ['#111111', '#222222', '#333333', '#444444', '#555555', '#666666', '#777777', '#888888']
  const opts = { palette: pal, extras: ['cape', 'hat', 'horns'] }
  const same = JSON.stringify(kit.look('ada', opts)) === JSON.stringify(kit.look('ada', opts))
  const looks = Array.from({ length: 40 }, (_, i) => kit.look('visitor-' + i, opts))
  const mains = new Set(looks.map((l) => l.colors.main))
  const inPal = looks.every((l) => pal.includes(l.colors.main) && pal.includes(l.colors.second) && l.colors.main !== l.colors.second)
  const inList = looks.every((l) => l.extras.every((e) => opts.extras.includes(e)))
  const shapes = new Set(looks.map((l) => l.extras.join()))
  out.look = [same, mains.size >= 6, inPal, inList, shapes.size >= 4]
}

// act: the move shows on its joint while it runs and is gone after.
{
  const a = kit.rig({ plan: 'biped' }), b = kit.rig({ plan: 'biped' })
  a.act('armR', 0.5, () => [-2, 0, 0])
  for (let i = 0; i < 15; i++) { a.pose(DT); b.pose(DT) }
  const mid = a.joints.armR.rotation.x - b.joints.armR.rotation.x
  for (let i = 0; i < 30; i++) { a.pose(DT); b.pose(DT) }
  const later = a.joints.armR.rotation.x - b.joints.armR.rotation.x
  const quiet = kit.rig({ plan: 'biped' })
  quiet.act('head', 0.2, () => {})  // a move that returns nothing changes nothing
  for (let i = 0; i < 6; i++) quiet.pose(DT)
  out.act = [Math.abs(mid + 2) < 0.01, Math.abs(later) < 1e-9, finite(quiet)]
}

// vehicle: wheels roll the distance driven, the nose lifts while speeding up.
{
  const r = kit.rig({ plan: 'vehicle', height: 1.5 })
  let lift = 0
  for (let f = 0; f < 120; f++) {
    r.root.position.z -= Math.min(8, f * 0.2) * DT
    r.pose(DT, { grounded: true })
    if (f < 30) lift = Math.min(lift, r.joints.chest.rotation.x)
  }
  const wheel = r.wheels[0]
  out.vehicle = [Math.abs(wheel.pivot.rotation.x * wheel.r + r.root.position.z) < 0.15, lift < -0.01, r.legs.length, r.eyes.length]
}

// wings flap in the air and fold on the ground; a hover flyer always flaps.
{
  const r = kit.rig({ plan: 'flyer' })
  const width = () => { r.root.updateMatrixWorld(true); const b = bounds(r.root); return b.max.x - b.min.x }
  for (let f = 0; f < 30; f++) r.pose(DT, { grounded: true })
  const foldedWidth = width()
  let open = 0
  let lo = 9, hi = -9
  for (let f = 0; f < 60; f++) { r.pose(DT, { grounded: false, vy: 1 }); open = Math.max(open, width()); lo = Math.min(lo, r.wings[1].pivot.rotation.z); hi = Math.max(hi, r.wings[1].pivot.rotation.z) }
  const g = kit.rig({ plan: 'flyer', legs: 0 })
  let glo = 9, ghi = -9
  for (let f = 0; f < 60; f++) { g.pose(DT); glo = Math.min(glo, g.wings[1].pivot.rotation.z); ghi = Math.max(ghi, g.wings[1].pivot.rotation.z) }
  out.wings = [open > 1.5 * foldedWidth, hi - lo > 0.8, ghi - glo > 0.8, g.legs.length]
}

// blob: hops as it moves and never sinks into the ground.
{
  const r = kit.rig({ plan: 'blob', height: 1 })
  let lo = 9, hi = -9, bottom = 9
  walk(r, 2, 1, 0)
  walk(r, 2, 2, 0, () => {
    lo = Math.min(lo, r.joints.body.position.y); hi = Math.max(hi, r.joints.body.position.y)
    bottom = Math.min(bottom, bounds(r.root).min.y)
  })
  out.blob = [hi - lo > 0.1, bottom > -0.01]
}

// 2D: the documented drawing math puts the lowest foot on the ground and a
// planted foot stays put.
{
  const m = kit.motion({ legs: [{ L: 16, phase: 0 }, { L: 16, phase: 0.5 }], arms: 2, H: 48 })
  let x = 0, worst = 0, under = 0, slide = 0
  const planted = [null, null]
  for (let f = 0; f < 240; f++) {
    x += 40 * DT
    m.update(DT, { speed: 40, vy: 0, grounded: true })
    if (f < 60) continue
    const hipY = m.hip + m.bodyY  // height above the ground
    const feet = m.legs.map((l) => ({
      x: x + 8 * Math.sin(l.thigh) + 8 * Math.sin(l.thigh - l.knee),
      y: hipY - 8 * Math.cos(l.thigh) - 8 * Math.cos(l.thigh - l.knee),
    }))
    worst = Math.max(worst, Math.abs(Math.min(feet[0].y, feet[1].y)))
    under = Math.min(under, feet[0].y, feet[1].y)
    feet.forEach((p, i) => {
      if (p.y < 0.01) { if (planted[i] == null) planted[i] = p.x; else slide = Math.max(slide, Math.abs(p.x - planted[i])) } else planted[i] = null
    })
  }
  out.flat = [worst < 0.02, under > -0.02, slide < 0.2, m.arms.length]
}

// The 2D arm recipe (rotate by -swing, then -elbow) puts the hand where the
// 3D rig does, seen from the side.
{
  const r = kit.rig({ plan: 'biped', height: 1.8 })
  let worst = 0
  walk(r, 5, 2, 0, () => {
    const a = r.motion.arms[1], arm = r.arms[1]  // the right arm
    const chest = r.joints.chest, sh = chest.worldToLocal(world(arm.shoulder)), hand = chest.worldToLocal(world(arm.hand))
    const upper = arm.elbow.position.length(), fore = arm.hand.position.length()
    const fwd = upper * Math.sin(a.swing) + fore * Math.sin(a.swing + a.elbow)
    const down = upper * Math.cos(a.swing) + fore * Math.cos(a.swing + a.elbow)
    worst = Math.max(worst, Math.abs((hand.z - sh.z) - fwd), Math.abs((sh.y - hand.y) - down))
  })
  out.arm2d = worst < 0.03
}

console.log(JSON.stringify(out))
})().catch((e) => { console.error(e); process.exit(1) })
`

func TestCharactersReferenceCore(t *testing.T) {
	three, err := filepath.Abs("../../web/vendor/three-v0.187.0.module.min.js")
	if err != nil {
		t.Fatal(err)
	}
	out := runJS(t, charactersReference, charactersHarness, three)
	t.Logf("%s", out)
	var got struct {
		Globals []string
		Plans   map[string][]bool
		Ghost   bool
		Walk    map[string][]bool
		Remote  []bool
		Arm2d   bool
		Biped   []bool
		Trot    []bool
		Lean    []bool
		Jump    []bool
		Blend   []bool
		Shapes  []bool
		Look    []bool
		Act     []bool
		Vehicle []any
		Wings   []any
		Blob    []bool
		Flat    []any
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
	check("globals", got.Globals, "[characterKit]")
	for _, plan := range []string{"biped", "quadruped", "bug", "flyer", "blob", "vehicle"} {
		check(
			plan+" [height above-ground feet-down finite extras-add-parts]",
			got.Plans[plan],
			"[true true true true true]",
		)
	}
	check("hover flyer off the ground", got.Ghost, "true")
	gaits := []string{
		"biped",
		"biped run",
		"biped back",
		"quadruped",
		"quadruped run",
		"bug",
		"flyer",
	}
	for _, gait := range gaits {
		check(
			gait+" [foot-down-or-run-flies none-under no-slide]",
			got.Walk[gait],
			"[true true true]",
		)
	}
	check(
		"remote [air-at-apex landing-dip cruising-flyer-airborne]",
		got.Remote,
		"[true true true]",
	)
	check("2D arm recipe matches the 3D hand", got.Arm2d, "true")
	check(
		"biped [legs-alternate arms-counter cycles bob-twice]",
		got.Biped,
		"[true true true true]",
	)
	check("trot [FL-BR FR-BL FL-FR-opposite]", got.Trot, "[true true true]")
	check("lean [forward into-turn]", got.Lean, "[true true]")
	check("jump [tuck dip recover]", got.Jump, "[true true true]")
	check(
		"blend [halves-with-dt small finite teleport-ignored]",
		got.Blend,
		"[true true true true]",
	)
	check(
		"look [same palette-spread in-palette in-list varied]",
		got.Look,
		"[true true true true true]",
	)
	check(
		"shapes [head-size torso-size swapped-only-known height feet all-named tail-parts]",
		got.Shapes,
		"[true true true true true true true]",
	)
	check("act [mid gone empty-ok]", got.Act, "[true true true]")
	check("vehicle [wheels-roll nose-up legs eyes]", got.Vehicle, "[true true 0 0]")
	check("wings [fold-narrower flap hover-flap hover-legs]", got.Wings, "[true true true 0]")
	check("blob [hops above-ground]", got.Blob, "[true true]")
	check("2D [on-ground none-under no-slide arms]", got.Flat, "[true true true 2]")
}
