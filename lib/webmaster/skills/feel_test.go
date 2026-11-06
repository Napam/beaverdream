package skills

import (
	"encoding/json"
	"testing"

	"github.com/Napam/beaverdream/lib/webmaster/internal/jsrun"
)

// feelHarness runs the core from feelReference with frames driven by hand and
// reports what the numbers did.
const feelHarness = `
const fs = require('fs')
const vm = require('vm')
const src = fs.readFileSync(process.argv[2], 'utf8')
const out = {}
let handlers = {}

function fresh(extra) {
  handlers = {}
  const ctx = Object.assign({ addEventListener: (type, fn) => { handlers[type] = fn } }, extra || {})
  const before = Object.keys(ctx)
  vm.createContext(ctx)
  vm.runInContext(src, ctx)
  out.globals = Object.keys(ctx).filter((k) => before.indexOf(k) < 0)
  return ctx.fx
}
function run(fx, seconds, fps, each) {
  const n = Math.round(seconds * fps)
  let game = 0
  for (let i = 0; i < n; i++) { game += fx.update(1 / fps); if (each) each(fx, i) }
  return game
}
function amp(fx, seconds) {
  let m = 0
  run(fx, seconds, 60, (f) => { m = Math.max(m, Math.hypot(f.shake.x, f.shake.y)) })
  return m
}

// Shake: grows with trauma squared, caps at full trauma, and is gone once the
// trauma has decayed.
let fx = fresh()
fx.impact('land', { x: 0, y: 0 })
out.landAmp = amp(fx, 0.05)
fx = fresh()
fx.impact('kill', { x: 0, y: 0 })
out.killAmp = amp(fx, 0.05)
fx = fresh()
for (let i = 0; i < 20; i++) fx.impact('kill', { x: 0, y: 0 })
out.capAmp = amp(fx, 0.05)
run(fx, 1 / 1.5, 60)
out.afterDecay = Math.hypot(fx.shake.x, fx.shake.y)

// Distance: an impact past HEAR adds nothing, unless it is mine.
fx = fresh(); fx.at.x = 0
fx.impact('kill', { x: 900, y: 0 })
out.farAmp = amp(fx, 0.1)
fx = fresh()
fx.impact('kill', { x: 900, y: 0 }, 1, true)
out.farMineAmp = amp(fx, 0.1)

// Hum shakes only on the frames it is called.
fx = fresh()
fx.hum(0.5); fx.update(1 / 60)
out.humOn = Math.hypot(fx.shake.x, fx.shake.y) + Math.abs(fx.shake.roll)
fx.update(1 / 60); fx.update(1 / 60)
out.humOff = Math.hypot(fx.shake.x, fx.shake.y) + Math.abs(fx.shake.roll)

// Hit stop: only my own hits freeze, two stops do not add up, and the game
// gets its dt back after.
fx = fresh()
fx.impact('hit', { x: 0, y: 0 })
out.othersStop = run(fx, 0.1, 60)
fx = fresh()
fx.impact('kill', { x: 0, y: 0 }, 1, true)
fx.impact('kill', { x: 0, y: 0 }, 1, true)
out.frozen = run(fx, 0.1, 60)
out.resumed = run(fx, 0.1, 60)

// Particles: the pool never grows, every one dies within its life, and the
// shake decays the same at any frame rate.
fx = fresh()
for (let i = 0; i < 100; i++) fx.impact('kill', { x: 0, y: 0, z: 0 })
let live = 0; fx.each(() => live++)
out.live = live
let fades = true; fx.each((p, f) => { if (f < 0 || f > 1) fades = false })
out.fades = fades
run(fx, 1, 60)
live = 0; fx.each(() => live++)
out.liveAfter = live
let flatZ = true
fx = fresh(); fx.impact('smash', { x: 5, y: 5 }); run(fx, 0.2, 60)
fx.each((p) => { if (p.z !== 0) flatZ = false })
out.flatZ = flatZ

// Full trauma is gone after 1/DECAY seconds at any frame rate.
out.decay = {}
for (const fps of [30, 144]) {
  fx = fresh(); fx.impact('kill', { x: 0, y: 0 }); fx.impact('kill', { x: 0, y: 0 })
  run(fx, 0.7, fps)
  out.decay[fps] = Math.hypot(fx.shake.x, fx.shake.y)
}

// Squash: a pop overshoots once and settles near zero within half a second.
fx = fresh()
const body = {}
fx.pop(body, 0.3)
let low = 0
for (let i = 0; i < 30; i++) low = Math.min(low, fx.settle(body, 1 / 60))
out.overshoot = low
for (let i = 0; i < 30; i++) fx.settle(body, 1 / 60)
out.settled = Math.abs(body.pop)
out.flash = body.flash

// Sound: nothing before a gesture, a capped number of voices after one, and
// nothing when muted. Rumble only for my own events.
let made = 0, rumbles = 0, vibrated = 0
const fake = {
  AudioContext: function () {
    this.state = 'running'; this.currentTime = 0; this.destination = {}
    const param = () => ({ setValueAtTime() {}, exponentialRampToValueAtTime() {} })
    this.createOscillator = () => { made++; return { frequency: param(), connect() {}, start() {}, stop() {} } }
    this.createGain = () => ({ gain: param(), connect() {} })
  },
  navigator: {
    getGamepads: () => [null, { vibrationActuator: { playEffect: () => { rumbles++; return Promise.resolve() } } }],
    vibrate: () => { vibrated++ },
  },
  matchMedia: () => ({ matches: true }),
}
fx = fresh(fake)
fx.impact('hit', { x: 0, y: 0 })
out.beforeGesture = made
handlers.pointerdown()
for (let i = 0; i < 20; i++) fx.impact('hit', { x: 0, y: 0 })
out.voices = made
fx = fresh(fake); made = 0; handlers.keydown(); fx.muted = true
fx.impact('hit', { x: 0, y: 0 })
out.muted = made
rumbles = 0; vibrated = 0
fx.impact('hit', { x: 0, y: 0 })
out.othersRumble = rumbles + vibrated
fx.impact('hit', { x: 0, y: 0 }, 1, true)
out.mineRumble = [rumbles, vibrated]
fx.impact('nope', { x: 0, y: 0 })

// A long frame is capped at 1/30, so the spring and the game stay stable
// after a hidden tab.
fx = fresh()
out.capped = fx.update(0.25)
const slow = {}
fx.pop(slow, 0.3)
for (let i = 0; i < 6; i++) fx.settle(slow, fx.update(0.25))
out.slowPop = Math.abs(slow.pop)

// An impact without y, as a 3D page might send, leaves the shake a number.
fx = fresh()
fx.impact('hit', { x: 0, z: 0 })
fx.update(1 / 60)
out.noY = [fx.shake.x, fx.shake.y, fx.shake.roll].every(Number.isFinite)

console.log(JSON.stringify(out))
`

func TestFeelReferenceCore(t *testing.T) {
	out := jsrun.Run(t, feelReference, feelHarness, "")
	t.Logf("%s", out)
	var got struct {
		Globals                              []string
		LandAmp, KillAmp, CapAmp, AfterDecay float64
		FarAmp, FarMineAmp, HumOn, HumOff    float64
		OthersStop, Frozen, Resumed          float64
		Live, LiveAfter                      int
		Fades, FlatZ                         bool
		Decay                                map[string]float64
		Overshoot, Settled, Flash            float64
		BeforeGesture, Voices, Muted         int
		OthersRumble                         int
		MineRumble                           []int
		Capped, SlowPop                      float64
		NoY                                  bool
	}
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatalf("decode: %v\n%s", err, out)
	}
	if len(got.Globals) != 1 || got.Globals[0] != "fx" {
		t.Errorf("the core declares globals %v, want only fx", got.Globals)
	}
	if !(got.LandAmp > 0.3 && got.LandAmp < got.KillAmp && got.KillAmp < got.CapAmp) {
		t.Errorf(
			"shake land %v kill %v capped %v, want growing with the event",
			got.LandAmp,
			got.KillAmp,
			got.CapAmp,
		)
	}
	if got.CapAmp > 16.01 || got.AfterDecay != 0 {
		t.Errorf("capped shake %v (max 16), after decay %v (want 0)", got.CapAmp, got.AfterDecay)
	}
	for fps, amp := range got.Decay {
		if amp != 0 {
			t.Errorf("full trauma still shakes %v px after 0.7s at %s fps", amp, fps)
		}
	}
	if got.FarAmp != 0 || got.FarMineAmp == 0 {
		t.Errorf(
			"far impact shook %v, far impact of mine shook %v; want 0 and more",
			got.FarAmp,
			got.FarMineAmp,
		)
	}
	if got.HumOn == 0 || got.HumOff != 0 {
		t.Errorf("hum shook %v while called and %v after, want some and 0", got.HumOn, got.HumOff)
	}
	if got.OthersStop < 0.099 || got.Frozen != 0 || got.Resumed < 0.06 {
		t.Errorf("game dt: others' hit %v, my kills %v then %v; want 0.1, 0, over 0.06",
			got.OthersStop, got.Frozen, got.Resumed)
	}
	if got.Live != 500 || got.LiveAfter != 0 || !got.Fades || !got.FlatZ {
		t.Errorf("particles: live %d (pool 500), after 1s %d, fades %v, 2D flat %v",
			got.Live, got.LiveAfter, got.Fades, got.FlatZ)
	}
	if got.Overshoot >= 0 || got.Settled > 0.01 || got.Flash != 0 {
		t.Errorf("pop overshoot %v, settled %v, flash %v; want below 0, near 0, 0",
			got.Overshoot, got.Settled, got.Flash)
	}
	if got.BeforeGesture != 0 || got.Voices != 8 || got.Muted != 0 {
		t.Errorf(
			"sounds before a gesture %d, voices %d (cap 8), muted %d",
			got.BeforeGesture,
			got.Voices,
			got.Muted,
		)
	}
	if d := got.Capped - 1.0/30; d > 1e-9 || d < -1e-9 {
		t.Errorf("update(0.25) returned %v, want the 1/30 cap", got.Capped)
	}
	if got.SlowPop > 0.3 {
		t.Errorf("a pop after six 0.25s frames is %v, want settling, not growing", got.SlowPop)
	}
	if !got.NoY {
		t.Error("an impact without y made the shake NaN")
	}
	if got.OthersRumble != 0 || len(got.MineRumble) != 2 || got.MineRumble[0] != 1 ||
		got.MineRumble[1] != 1 {
		t.Errorf(
			"rumble for others %d, for mine %v; want 0 and one pad plus one vibrate",
			got.OthersRumble,
			got.MineRumble,
		)
	}
}
