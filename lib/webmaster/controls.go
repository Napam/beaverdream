package webmaster

import (
	"strings"
	"unicode"
)

func wordSet(words ...string) map[string]bool {
	set := make(map[string]bool, len(words))
	for _, w := range words {
		set[w] = true
	}
	return set
}

// controlsStrong are words that ask for a world someone moves through on
// their own. Two-word names are matched joined, so "top-down", "top down" and
// "topdown" are the same word.
var controlsStrong = wordSet(
	"platformer", "roguelike", "sokoban", "tetris", "arcade", "shooter", "fps",
	"maze", "dungeon", "explore", "jump", "zombie", "multiplayer", "flappy",
	"invaders", "pokemon", "stardew", "gta", "simulator", "racetrack", "kart",
	"mario", "zelda", "snake", "pong", "breakout", "asteroids", "minecraft",
	"roblox", "rpg", "mmo", "3d", "2d", "avatar", "wasd", "joystick", "gamepad",
	"spaceship", "playable", "videogame", "minigame", "topdown", "firstperson",
	"thirdperson", "sidescroller", "sidescroll", "pacman", "openworld",
	"hoppe", "hopp", "hopper",
)

// controlsWeak are words that mean a world only next to a cue or another
// weak word: "make it a game" and "drive a car" ask for one, "the game last
// night", "google drive" and "a fly in my soup" do not.
var controlsWeak = wordSet(
	"game", "world", "map", "level", "race", "racer", "drive", "fly", "flight",
	"car", "plane", "boat", "ship", "tank", "rocket", "space", "shoot", "steer",
	"farm", "sim", "platform", "walk", "character", "camera", "controls",
	"keyboard", "gravity", "physics", "enemy", "doom", "sonic",
	"spill", "spillet", "bil", "kjøre", "kjøring",
)

// controlsCues are the words that turn a nearby weak word into a request.
var controlsCues = wordSet(
	"make", "build", "create", "turn", "into", "open", "play", "around",
	"through", "lag", "lage", "gjør", "bygg",
)

// controlsWindow is how many words apart a weak word and its cue may be.
const controlsWindow = 3

// wordIn reports whether word, or a plain English form of it (a plural, an
// -ing or an -ed), is in set.
func wordIn(set map[string]bool, word string) bool {
	if set[word] {
		return true
	}
	if stem, ok := strings.CutSuffix(word, "ies"); ok && set[stem+"y"] {
		return true
	}
	if stem, ok := strings.CutSuffix(word, "s"); ok && set[stem] {
		return true
	}
	for _, suffix := range []string{"ing", "ed"} {
		if stem, ok := strings.CutSuffix(word, suffix); ok && (set[stem] || set[stem+"e"]) {
			return true
		}
	}
	return false
}

// postWantsControls reports whether one post asks for a world.
func postWantsControls(body string) bool {
	words := strings.FieldsFunc(strings.ToLower(body), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	})
	var weak, cues []int
	for i, word := range words {
		if wordIn(controlsStrong, word) {
			return true
		}
		if i > 0 && wordIn(controlsStrong, words[i-1]+word) {
			return true
		}
		if wordIn(controlsWeak, word) {
			weak = append(weak, i)
		}
		if controlsCues[word] {
			cues = append(cues, i)
		}
	}
	for _, w := range weak {
		for _, other := range append(cues, weak...) {
			if other != w && other >= w-controlsWindow && other <= w+controlsWindow {
				return true
			}
		}
	}
	return false
}

// pageMoves reports whether the current page moves something with keys: it
// listens for keys and also reads arrows or WASD or runs a frame loop, or it
// locks the pointer. A keydown that only sends the form on Enter does not count.
func pageMoves(html string) bool {
	if strings.Contains(html, "requestPointerLock") {
		return true
	}
	if !strings.Contains(html, "keydown") && !strings.Contains(html, "keyup") {
		return false
	}
	for _, sign := range []string{"Arrow", "KeyW", "KeyA", "KeyD", "requestAnimationFrame"} {
		if strings.Contains(html, sign) {
			return true
		}
	}
	return false
}

// wantsControls reports whether a turn gets the controls reference: a new
// post asks for a world, game or movement, or the current page already moves
// something with keys, so a follow-up like "it feels slow" sees it too.
func wantsControls(site Site, triggers []Post) bool {
	if pageMoves(site.HTML) {
		return true
	}
	for _, trigger := range triggers {
		if postWantsControls(trigger.Body) {
			return true
		}
	}
	return false
}

// controlsReference is how a page with movement should feel. It rides after
// the new post only on turns wantsControls picks, so other turns do not pay
// for it and the system prompt keeps its cache. The model picks the genre from
// the post. The script block is run by controls_test.go, which checks the
// landing, the wall, the jump height, the coyote time, the buffer, the keys
// and frame-rate independence; change both together.
const controlsReference = `CONTROLS REFERENCE:
This turn may involve a world someone moves through. If you build or change
one, make it feel good without being asked. If not, ignore this section.
Pick the genre from the post and use its GENRES line.

This platformer core is tested. Reuse its parts in any genre:

<script>
var game = (function () {
  var T = 32, MAP = [  // constant, never random
    '##########',
    '#........#',
    '#........#',
    '#....###.#',
    '#........#',
    '##########']
  var W = MAP[0].length * T, H = MAP.length * T
  var SPEED = 220, START = 14, STOP = 18  // px/s, ease rates per second
  var GRAVITY = 1800, MAX_FALL = 900, JUMP = Math.sqrt(2 * GRAVITY * 80)  // 80px
  var KEYS = { ArrowLeft: 1, ArrowRight: 1, KeyA: 1, KeyD: 1, ArrowUp: 2, Space: 2, KeyW: 2 }  // 2 jumps
  var me = { x: 48, y: 132, w: 20, h: 28, vx: 0, vy: 0, ground: false, coyote: 0, buffer: 0 }
  var keys = {}

  function press(code) {
    if (!keys[code] && KEYS[code] === 2) me.buffer = 0.12
    keys[code] = true
  }
  function release(code) {
    if (KEYS[code] === 2 && me.vy < 0) me.vy *= 0.5
    keys[code] = false
  }
  addEventListener('keydown', function (e) {
    if (!KEYS[e.code] || e.target.closest && e.target.closest('input, textarea, select, [contenteditable]')) return
    if (e.code.indexOf('Key') !== 0) e.preventDefault()  // arrows and Space scroll
    press(e.code)
  })
  addEventListener('keyup', function (e) { release(e.code) })
  addEventListener('blur', function () { keys = {} })

  function held(a, b) { return keys[a] || keys[b] ? 1 : 0 }
  function ease(v, target, k, dt) { return target + (v - target) * Math.exp(-k * dt) }
  function solid(tx, ty) { return !MAP[ty] || !MAP[ty][tx] || MAP[ty][tx] === '#' }
  function hits() {
    for (var ty = Math.floor(me.y / T); ty <= Math.floor((me.y + me.h - 0.001) / T); ty++)
      for (var tx = Math.floor(me.x / T); tx <= Math.floor((me.x + me.w - 0.001) / T); tx++)
        if (solid(tx, ty)) return true
    return false
  }

  function update(dt) {
    var dir = held('ArrowRight', 'KeyD') - held('ArrowLeft', 'KeyA')
    var vx = me.vx, vy = me.vy
    me.vx = ease(vx, dir * SPEED, (dir ? START : STOP) * (me.ground ? 1 : 0.6), dt)
    me.coyote = me.ground ? 0.1 : me.coyote - dt
    me.buffer -= dt
    if (me.buffer > 0 && me.coyote > 0) { vy = -JUMP; me.buffer = me.coyote = 0 }
    me.vy = Math.min(vy + GRAVITY * dt, MAX_FALL)

    var dx = (vx + me.vx) / 2 * dt  // the average speed is right at any fps
    me.x += dx
    if (hits()) {
      me.x = dx > 0 ? Math.floor((me.x + me.w) / T) * T - me.w : (Math.floor(me.x / T) + 1) * T
      me.vx = 0
    }
    var dy = (vy + me.vy) / 2 * dt
    me.y += dy
    me.ground = false
    if (hits()) {
      if (dy > 0) { me.y = Math.floor((me.y + me.h) / T) * T - me.h; me.ground = true }
      else me.y = (Math.floor(me.y / T) + 1) * T
      me.vy = 0
    }
  }

  var last = 0
  function frame(now) {
    var dt = last ? Math.min((now - last) / 1000, 1 / 30) : 0
    last = now
    update(dt)
    draw(me)
    requestAnimationFrame(frame)
  }
  requestAnimationFrame(frame)
  return { me: me, press: press, release: release, update: update, W: W, H: H }
})()
</script>

- draw(me) is your canvas or DOM and only draws. game wraps the core so it
  can share a page with the journal example. Then that example's me is game.me,
  its bounds are game.W and game.H, not 100, and one frame loop runs.
- Stopping eases at least as fast as starting, both 0.6 as fast in the air.
  Keep any speed times 1/30 under one tile, or bodies pass through walls.
- If the world does not fill the page, skip keys while it is off screen.
- Top-down: no gravity, ease vy like vx, divide the input by Math.hypot(dx, dy)
  when it is over 1. Facing is the last nonzero input.
- Grid: move to the next tile center at a fixed speed. A step that would pass
  it stops there, takes the queued turn if open, and spends the rest. Never
  test x % T === 0.
- Steps: one tile per press, tweened over 0.1s. A held key repeats after 0.18s,
  then 8 a second; a press mid-step is kept. Snake: a fixed tick, two queued
  turns, no reversing.
- 3D: import * as THREE from 'https://unpkg.com/three@0.160.0/build/three.module.js'
  in a module script. rotation.order 'YXZ'; yaw and pitch -= movementX and
  movementY * 0.0025, pitch clamped to -1.4..1.4. Forward (-sin yaw, 0, -cos
  yaw) and right (cos yaw, 0, -sin yaw) sum, normalized, eased. Move x then z,
  each against walls, so the player slides.
- Touch: buttons under @media (pointer: coarse), touch-action:none.
  pointerdown calls game.press(code) and preventDefault; pointerup, cancel and
  leave call game.release(code). Keep the code in data-hold.

CAMERAS: after the player moves, lerp(goal, 1 - Math.exp(-k * dt)). Snap on
the first frame and after a respawn. A fixed factor like 0.1 jitters.
- Side-scroll: a deadzone a third of the screen wide, look-ahead a quarter
  screen, k 4, clamped to the level, whole pixels.
- Top-down: k 6, clamped to the map, none if the map fits. Isometric: a fixed
  angle, WASD along the screen axes.
- Third person: drag turns yaw, goal 4 back and 1.5 up, k 8, pulled in to a
  ray hit. It stays put when the player stops. Move by camera yaw; the avatar
  turns to its movement at 10 rad/s.
- First person: pointer lock on a canvas click, drag to look while unlocked.
  Look is never smoothed. No head bob unless asked. Sprint eases FOV up 5.
- Chase: behind and above, k 5, aimed ahead, FOV 70 to 85 with speed.
- Orbit: drag orbits, wheel zooms, k 6, pitch 0.1..1.4, auto-rotate if asked.

GENRES:
- FPS: first person. Walk 4.5 m/s, Shift x1.6, Space jump, C crouch at half
  speed, start k 10, stop k 15, FOV 75.
- Third-person action: third person, jumps like the core.
- Farm or cozy sim: top-down at k 4 from a high 3/4 view, 3 to 4 tiles/s, E
  uses the highlighted tile in front. No mouse look.
- Top-down RPG: 8-way, 4 to 6 tiles/s, k 15.
- Platformer: the core with side-scroll, jumps 2 to 3 tiles, x1.5 gravity
  while falling.
- Pacman: grid, the whole maze in view, 7 to 8 tiles/s.
- Sokoban, roguelike: steps, and the camera snaps per room.
- Racing: chase. W/S throttle and brake, A/D steer less as speed grows,
  sideways speed eased to 0 at k 6 (2 to drift). Never instant top speed.
- Flight, space: chase at k 6. W thrusts along facing, A/D roll, mouse pitch.
  No gravity, drag k 0.3, a top speed, no stop on release.
- Multiplayer: your avatar, physics and camera are local. Rows carry position,
  facing and actions; state carries tallies. Others ease to their newest row
  at k 10 and snap past a few tiles.

ROWS: free movement sends on the journal example's 60ms timer, even while
still. A grid or step world sends one row per step, and again every 2s while
still. A world change (a block, a pellet, a lap) is one row in a slow key.
Shared enemies follow a fixed rule of Date.now() + skew, where skew =
Date.parse(row.at) - Date.now() each time your own append resolves.
`
