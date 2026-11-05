package webmaster

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestWantsControls(t *testing.T) {
	for body, want := range map[string]bool{
		"make pacman":                       true,
		"pac-man please":                    true,
		"a 3D room I can walk around in":    true,
		"a platformer!":                     true,
		"turn the board into an open world": true,
		"make it a game":                    true,
		"make a video game":                 true,
		"a top-down town":                   true,
		"a first person view":               true,
		"a side-scroller":                   true,
		"a farming sim":                     true,
		"a roguelike":                       true,
		"sokoban levels":                    true,
		"space invaders":                    true,
		"zombies everywhere":                true,
		"let me drive a car":                true,
		"fly a plane":                       true,
		"a racing game":                     true,
		"I want to go exploring":            true,
		"jumping cats":                      true,
		"lag et spill":                      true,
		"jeg vil hoppe":                     true,
		"the game last night was wild":      false,
		"hello world":                       false,
		"best board in the world":           false,
		"upload it to google drive":         false,
		"a fly in my soup":                  false,
		"make the sprite pink":              false,
		"race and class":                    false,
		"my car broke down":                 false,
		"a paint board":                     false,
		"make it pink":                      false,
		"I ran a marathon today":            false,
	} {
		if got := wantsControls(Site{}, []Post{{Body: body}}); got != want {
			t.Errorf("wantsControls(%q) = %v, want %v", body, got, want)
		}
	}
	batch := []Post{{Body: "hi"}, {Body: "make it a racing game"}}
	if !wantsControls(Site{}, batch) {
		t.Error("a batch with one world post did not get the reference")
	}
}

// A page that already moves something with keys gets the reference on any
// post; a page that only listens for Enter does not.
func TestWantsControlsFromThePage(t *testing.T) {
	for html, want := range map[string]bool{
		`<script>addEventListener('keydown', e => { if (e.code === 'ArrowLeft') x-- })</script>`: true,
		`<script>addEventListener('keyup', e => keys[e.code] = false); var k = 'KeyD'</script>`:  true,
		`<script>onkeydown = step; requestAnimationFrame(step)</script>`:                         true,
		`<script>canvas.onclick = () => canvas.requestPointerLock()</script>`:                    true,
		`<textarea onkeydown="if (event.key === 'Enter') this.form.requestSubmit()">`:            false,
		`<script>requestAnimationFrame(spin)</script>`:                                           false,
	} {
		site := Site{HTML: html + "{{posts}}"}
		if got := wantsControls(site, []Post{{Body: "make it faster"}}); got != want {
			t.Errorf("wantsControls on %q = %v, want %v", html, got, want)
		}
	}
}

// The reference comes after the new post as its own section, and only on the
// turns that want it, so the prefix before the new post never changes.
func TestUserPromptAddsControlsAfterTheNewPost(t *testing.T) {
	room := Room{Log: []Turn{{ID: 1, Kind: TurnPost, Author: "kit", Body: "hi"}}}
	site := Site{CSS: "body{}", HTML: "{{posts}}"}

	plain, plainSections := userPromptSections(
		site,
		room,
		[]Post{{ID: 2, Author: "dave", Body: "make it pink"}},
	)
	if strings.Contains(plain, "CONTROLS REFERENCE") {
		t.Error("a color post got the controls reference")
	}
	if last := plainSections[len(plainSections)-1]; last.Name != "new post" {
		t.Errorf("last section is %q, want new post", last.Name)
	}

	world, sections := userPromptSections(
		site,
		room,
		[]Post{{ID: 2, Author: "dave", Body: "make pacman"}},
	)
	last := sections[len(sections)-1]
	if last.Name != "controls" || last.End != len(world) {
		t.Fatalf("last section is %+v, want controls ending the prompt", last)
	}
	if !strings.HasPrefix(world[last.Start:], "CONTROLS REFERENCE:") {
		t.Errorf("controls section starts with %q", world[last.Start:last.Start+30])
	}
	if strings.Index(world, "NEW POST #2") > last.Start {
		t.Error("the controls reference comes before the new post")
	}
	cut := strings.Index(plain, "NEW POST")
	if world[:cut] != plain[:cut] {
		t.Error("the controls reference changed the prompt before the new post")
	}
}

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
function key(type, code, inForm) {
  let prevented = false
  handlers[type]({
    code: code,
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
out.arrowPrevented = key('keydown', 'ArrowLeft')
out.spaceInFormPrevented = key('keydown', 'Space', true)
out.letterPrevented = key('keydown', 'KeyA')
out.otherPrevented = key('keydown', 'PageDown')
out.mapSize = [g.W, g.H]

console.log(JSON.stringify(out))
`

func TestControlsReferenceCore(t *testing.T) {
	out := runJS(t, controlsReference, controlsHarness, "")
	t.Logf("%s", out)
	var got struct {
		Globals               []string
		LandBottom, WallRight float64
		WallVx, Tap, TypedVx  float64
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
