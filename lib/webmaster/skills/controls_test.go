package skills

import (
	"encoding/json"
	"testing"

	"github.com/Napam/beaverdream/lib/webmaster/internal/jsrun"
)

// controlsHarness runs the platformer core from controlsReference with key
// events and frames driven by hand, and reports what the numbers did.
const controlsHarness = `
const fs = require('fs')
const vm = require('vm')
const src = fs.readFileSync(process.argv[2], 'utf8')
let handlers = {}
const out = {}

function fresh() {
  handlers = {}
  const ctx = {
    addEventListener: (type, fn) => { handlers[type] = fn },
    requestAnimationFrame: () => {},
    draw: () => {},
  }
  const before = Object.keys(ctx)
  vm.createContext(ctx)
  vm.runInContext(src, ctx)
  out.globals = Object.keys(ctx).filter((k) => before.indexOf(k) < 0)
  return ctx.game
}
function key(type, code, inForm, meta, ctrl, alt) {
  let prevented = false
  handlers[type]({
    code: code,
    metaKey: !!meta,
    ctrlKey: !!ctrl,
    altKey: !!alt,
    target: { closest: () => (inForm ? {} : null) },
    preventDefault: () => { prevented = true },
  })
  return prevented
}
function run(g, seconds, fps, each) {
  const n = Math.round(seconds * fps)
  for (let i = 0; i < n; i++) { g.update(1 / fps); if (each) each(g.me, i) }
}

// Landing: fall from the spawn row onto the floor.
let g = fresh()
g.me.y = 40
run(g, 1, 60)
out.landBottom = g.me.y + g.me.h
out.landGround = g.me.ground

// Wall: hold right on the floor until the right wall stops the box.
key('keydown', 'ArrowRight')
run(g, 3, 60)
out.wallRight = g.me.x + g.me.w
out.wallVx = g.me.vx
key('keyup', 'ArrowRight')

// Frame-rate independence: 0.3s of holding right from rest, and the height
// of a held jump from the floor.
out.x = {}; out.vx = {}; out.jump = {}
for (const fps of [30, 60, 144]) {
  g = fresh(); run(g, 0.5, fps)
  key('keydown', 'KeyD'); run(g, 0.3, fps)
  out.x[fps] = g.me.x; out.vx[fps] = g.me.vx
  key('keyup', 'KeyD')

  g = fresh(); run(g, 0.5, fps)
  const floorY = g.me.y
  let top = floorY
  key('keydown', 'Space')
  run(g, 1, fps, (me) => { top = Math.min(top, me.y) })
  key('keyup', 'Space')
  out.jump[fps] = floorY - top
}

// A short tap rises less than a held jump.
g = fresh(); run(g, 0.5, 60)
let tapTop = g.me.y, tapFloor = g.me.y
key('keydown', 'Space'); run(g, 0.05, 60); key('keyup', 'Space')
run(g, 1, 60, (me) => { tapTop = Math.min(tapTop, me.y) })
out.tap = tapFloor - tapTop

// Coyote time and the jump buffer: walk off the platform's right edge.
function offLedge(lateSeconds) {
  const g = fresh()
  g.me.x = 200; g.me.y = 96 - g.me.h
  run(g, 0.2, 60)
  key('keydown', 'ArrowRight')
  for (let i = 0; i < 120 && g.me.ground; i++) g.update(1 / 60)
  key('keyup', 'ArrowRight')
  run(g, lateSeconds, 60)
  key('keydown', 'Space')
  g.update(1 / 60)
  const immediate = g.me.vy < 0
  let later = false
  run(g, 0.2, 60, (me) => { if (me.vy < 0) later = true })
  key('keyup', 'Space')
  return { immediate: immediate, later: later }
}
out.coyote = offLedge(0.05)
out.late = offLedge(0.2)

// Keys typed into the post form do not move the avatar, only arrows and Space
// are kept from scrolling, and keys the game does not use are left alone.
g = fresh()
key('keydown', 'KeyD', true)
run(g, 0.5, 60)
out.typedVx = g.me.vx
// Cmd+D is a shortcut, and its keyup never comes: it must not hold the key.
key('keydown', 'KeyD', false, true)
run(g, 0.5, 60)
out.shortcutVx = g.me.vx
// Ctrl and Alt are shortcuts too.
g = fresh(); run(g, 0.5, 60)
key('keydown', 'KeyD', false, false, true)
run(g, 0.3, 60)
out.ctrlVx = g.me.vx
g = fresh(); run(g, 0.5, 60)
key('keydown', 'KeyD', false, false, false, true)
run(g, 0.3, 60)
out.altVx = g.me.vx
// A keyup for a key never pressed here (typed into the form) does not cut a
// jump short.
g = fresh(); run(g, 0.5, 60)
key('keydown', 'Space'); run(g, 0.05, 60)
const rising = g.me.vy
key('keyup', 'KeyW')
out.strayRelease = g.me.vy === rising && rising < 0
key('keyup', 'Space')
// A key let go while Cmd is down sends no keyup on macOS: letting go of Cmd
// drops what is held, so the box coasts to a stop instead of running on.
g = fresh(); run(g, 0.5, 60)
key('keydown', 'KeyD'); run(g, 0.3, 60)
handlers.keyup({ code: 'MetaLeft', key: 'Meta' })
run(g, 0.2, 60)
out.afterCmd = g.me.vx < 50
out.arrowPrevented = key('keydown', 'ArrowLeft')
out.spaceInFormPrevented = key('keydown', 'Space', true)
out.letterPrevented = key('keydown', 'KeyA')
out.otherPrevented = key('keydown', 'PageDown')
out.mapSize = [g.W, g.H]

console.log(JSON.stringify(out))
`

func TestControlsReferenceCore(t *testing.T) {
	out := jsrun.Run(t, controlsReference, controlsHarness, "")
	t.Logf("%s", out)
	var got struct {
		Globals               []string
		LandBottom, WallRight float64
		WallVx, Tap, TypedVx  float64
		ShortcutVx            float64
		CtrlVx, AltVx         float64
		StrayRelease          bool
		AfterCmd              bool
		LandGround            bool
		X, Vx, Jump           map[string]float64
		Coyote, Late          struct{ Immediate, Later bool }
		ArrowPrevented        bool
		SpaceInFormPrevented  bool
		LetterPrevented       bool
		OtherPrevented        bool
		MapSize               []float64
	}
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatalf("decode: %v\n%s", err, out)
	}

	if len(got.Globals) != 1 || got.Globals[0] != "game" {
		t.Errorf("the core declares globals %v, want only game", got.Globals)
	}
	if got.LandBottom != 160 || !got.LandGround {
		t.Errorf("landed with bottom %v ground %v, want 160 true", got.LandBottom, got.LandGround)
	}
	if got.WallRight != 288 || got.WallVx != 0 {
		t.Errorf("stopped at %v with vx %v, want flush at 288 with 0", got.WallRight, got.WallVx)
	}
	for _, fps := range []string{"30", "144"} {
		if d := got.X[fps] - got.X["60"]; d > 1 || d < -1 {
			t.Errorf(
				"0.3s of running covers %v px at %sfps and %v at 60fps",
				got.X[fps],
				fps,
				got.X["60"],
			)
		}
		if d := got.Vx[fps] - got.Vx["60"]; d > 0.5 || d < -0.5 {
			t.Errorf(
				"speed after 0.3s is %v at %sfps and %v at 60fps",
				got.Vx[fps],
				fps,
				got.Vx["60"],
			)
		}
	}
	for _, fps := range []string{"30", "60", "144"} {
		if h := got.Jump[fps]; h < 79.5 || h > 80.01 {
			t.Errorf("jump at %sfps rose %v px, want 80", fps, h)
		}
	}
	if got.Tap <= 0 || got.Tap > 0.6*got.Jump["60"] {
		t.Errorf("a tap rose %v px against a full jump of %v", got.Tap, got.Jump["60"])
	}
	if !got.Coyote.Immediate {
		t.Error("a jump 0.05s after leaving the ledge did not fire")
	}
	if got.Late.Immediate {
		t.Error("a jump 0.2s after leaving the ledge fired in the air")
	}
	if !got.Late.Later {
		t.Error("a press shortly before landing was not buffered into a jump")
	}
	if got.TypedVx != 0 {
		t.Errorf("a key typed into the form moved the avatar: vx %v", got.TypedVx)
	}
	if !got.StrayRelease || !got.AfterCmd {
		t.Errorf(
			"a stray keyup left the jump alone %v, letting go of Cmd dropped the held key %v; want both",
			got.StrayRelease,
			got.AfterCmd,
		)
	}
	if got.ShortcutVx != 0 || got.CtrlVx != 0 || got.AltVx != 0 {
		t.Errorf(
			"a key pressed with Cmd, Ctrl or Alt moved the avatar: vx %v, %v, %v",
			got.ShortcutVx,
			got.CtrlVx,
			got.AltVx,
		)
	}
	if !got.ArrowPrevented || got.SpaceInFormPrevented || got.LetterPrevented ||
		got.OtherPrevented {
		t.Errorf(
			"preventDefault: arrow %v, Space in the form %v, letter %v, PageDown %v; want true false false false",
			got.ArrowPrevented,
			got.SpaceInFormPrevented,
			got.LetterPrevented,
			got.OtherPrevented,
		)
	}
	if len(got.MapSize) != 2 || got.MapSize[0] != 320 || got.MapSize[1] != 192 {
		t.Errorf("game.W and game.H are %v, want 320 192", got.MapSize)
	}
}
