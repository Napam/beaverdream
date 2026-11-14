package skills

// feelReference is the feel skill: shake, hit stop, particles, squash, sound
// and rumble from one impact call. The model reads it with load_skill. The
// script block is run by feel_test.go, which checks the shake curve and its
// decay, the distance falloff, an impact without a place, hum, the hit stop,
// the particle pool, the spring and its cap, the audio voice cap, the named
// sounds' gate and the rumble; change both together.
const feelReference = `FEEL REFERENCE:
Use this when a page has actions: hits, shots, jumps, things breaking, a
score. Make every action answer the player, without being asked. This core is
tested. Reuse helpers already on the page, or copy the functions you need
with their dependencies intact. Tune effects in the EVENTS table.

<script>
var fx = (function () {
  // Every event in one table. Tune a number here, never at the call site.
  // trauma 0..1 adds shake, stop freezes the game (s), burst is the particle
  // count, speed (px/s) and life (s) shape them, tone is a pitch in Hz (0 is
  // silent), buzz is rumble in ms for the visitor it happened to. An optional
  // sound names a row of the table given to fx.sounds; it plays instead of the
  // tone.
  var EVENTS = {
    shot:  { trauma: 0.04, stop: 0,    burst: 3,  speed: 90,  life: 0.15, size: 2, tone: 880, buzz: 0 },
    land:  { trauma: 0.3,  stop: 0,    burst: 8,  speed: 120, life: 0.4,  size: 3, tone: 110, buzz: 20 },
    hit:   { trauma: 0.35, stop: 0.06, burst: 12, speed: 220, life: 0.35, size: 3, tone: 330, buzz: 40 },
    smash: { trauma: 0.5,  stop: 0.04, burst: 24, speed: 260, life: 0.8,  size: 4, tone: 90,  buzz: 60 },
    kill:  { trauma: 0.6,  stop: 0.12, burst: 32, speed: 320, life: 0.7,  size: 4, tone: 60,  buzz: 120 }
  }
  var SHAKE = 16, ROLL = 0.04, DECAY = 1.5  // px and radians at full trauma; trauma lost per s
  var HEAR = 800                            // farther than this from fx.at: no shake, no sound
  var GRAVITY = 600, DRAG = 3, MAX = 500    // particles: px/s2, per s, pool size
  var trauma = 0, hum = 0, stop = 0, clock = 0, cursor = 0
  var pool = []
  for (var i = 0; i < MAX; i++) pool.push({ on: false, x: 0, y: 0, z: 0, vx: 0, vy: 0, vz: 0, age: 0, life: 1, size: 1 })
  var api = { at: { x: 0, y: 0, z: 0 }, shake: { x: 0, y: 0, roll: 0 }, muted: false, EVENTS: EVENTS }

  // impact is the one call for anything that happens. strength scales it
  // (1 is normal), mine is true when it happens to this visitor's own avatar.
  // Without at it happens where the listener is.
  api.impact = function (type, at, strength, mine) {
    var e = EVENTS[type]
    if (!e) return
    var flat = !at || at.z == null  // no place, or a place on a 2D page
    at = at || api.at
    if (strength == null) strength = 1
    var d = Math.hypot((at.x || 0) - (api.at.x || 0), (at.y || 0) - (api.at.y || 0), (at.z || 0) - (api.at.z || 0))
    var near = mine ? 1 : Math.max(0, 1 - d / HEAR)
    trauma = Math.min(1, trauma + e.trauma * strength * near)
    if (mine) { stop = Math.max(stop, e.stop); buzz(e.buzz * strength) }
    burst(at, e, Math.min(strength, 2), flat)
    if (e.sound && sounds[e.sound]) api.sound(e.sound, at, strength, mine)
    else tone(e.tone, strength * near)
  }
  // sounds gives the core a table of named zzfx parameter arrays and the zzfx
  // function itself, from import { zzfx } from 'zzfx'.
  api.sounds = function (table, fn) { sounds = table || {}; synth = fn }
  // sound plays one named sound, quieter with distance like impact. Nothing
  // plays before a gesture, when muted, twice inside COOLDOWN seconds, or with
  // more than VOICES sounds started in the last 0.25 s: a burst of the same
  // thing would only clip.
  api.sound = function (name, at, strength, mine) {
    var p = sounds[name]
    if (!p || !synth || !unlocked || api.muted) return
    if (strength == null) strength = 1
    var d = at ? Math.hypot((at.x || 0) - (api.at.x || 0), (at.y || 0) - (api.at.y || 0), (at.z || 0) - (api.at.z || 0)) : 0
    var volume = (p[0] == null ? 1 : p[0]) * Math.min(strength, 1) * (mine ? 1 : Math.max(0, 1 - d / HEAR))
    if (volume < 0.05) return
    var now = nowS()
    if (now - (lastPlayed[name] == null ? -1 : lastPlayed[name]) < COOLDOWN) return
    plays = plays.filter(function (t) { return now - t < 0.25 })
    if (plays.length >= VOICES) return
    lastPlayed[name] = now
    plays.push(now)
    var args = p.slice()
    args[0] = volume
    synth.apply(null, args)
  }
  // hum shakes only while it is called, once a frame: a held beam, an engine.
  api.hum = function (amount) { hum = Math.max(hum, amount) }

  // update runs every frame with the real dt. It returns the dt the game
  // should step with: capped at 1/30 so a hidden tab cannot tunnel bodies
  // through walls, and 0 during a hit stop.
  api.update = function (dt) {
    dt = Math.min(dt, 1 / 30)
    clock += dt
    trauma = Math.max(0, trauma - DECAY * dt)
    var s = Math.max(trauma, hum)
    s *= s
    hum = 0
    api.shake.x = SHAKE * s * wave(0)
    api.shake.y = SHAKE * s * wave(1)
    api.shake.roll = ROLL * s * wave(2)
    var drag = Math.exp(-DRAG * dt)
    for (var i = 0; i < MAX; i++) {
      var p = pool[i]
      if (!p.on) continue
      p.age += dt
      if (p.age >= p.life) { p.on = false; continue }
      p.vx *= drag; p.vy = p.vy * drag + GRAVITY * dt; p.vz *= drag
      p.x += p.vx * dt; p.y += p.vy * dt; p.z += p.vz * dt
    }
    var game = stop > 0 ? 0 : dt
    stop = Math.max(0, stop - dt)
    return game
  }
  // each visits the live particles with fade 1 at birth, 0 at death.
  api.each = function (fn) {
    for (var i = 0; i < MAX; i++) if (pool[i].on) fn(pool[i], 1 - pool[i].age / pool[i].life)
  }
  // pop squashes a body that was hit and flashes it white; settle springs it
  // back, overshooting about once. Draw it scaled (1 + o.pop, 1 - o.pop).
  // Hits in a row add up to 0.8 at most, or the body would turn inside out.
  api.pop = function (o, amount) { o.pop = Math.min(0.8, (o.pop || 0) + amount); o.popV = o.popV || 0; o.flash = 0.08 }
  api.settle = function (o, dt) {
    o.popV = (o.popV || 0) + (-300 * (o.pop || 0) - 14 * o.popV) * dt
    o.pop = (o.pop || 0) + o.popV * dt
    o.flash = Math.max(0, (o.flash || 0) - dt)
    return o.pop
  }

  function wave(k) {  // smooth noise, a different one per axis
    return Math.sin(clock * (31 + 7 * k) + 1.7 * k) * 0.6 + Math.sin(clock * (17 + 5 * k) + 4.1 * k) * 0.4
  }
  function burst(at, e, strength, flat) {
    var n = Math.round(e.burst * strength)
    for (var i = 0; i < n; i++) {
      var p = pool[cursor]
      cursor = (cursor + 1) % MAX  // a full pool reuses the oldest
      var a = Math.random() * Math.PI * 2, b = flat ? 0 : Math.acos(2 * Math.random() - 1) - Math.PI / 2
      var v = e.speed * (0.4 + 0.6 * Math.random()) * Math.sqrt(strength)
      p.on = true; p.age = 0; p.life = e.life * (0.6 + 0.4 * Math.random()); p.size = e.size
      p.x = at.x || 0; p.y = at.y || 0; p.z = at.z || 0
      p.vx = Math.cos(a) * Math.cos(b) * v; p.vy = Math.sin(a) * Math.cos(b) * v; p.vz = Math.sin(b) * v
    }
  }

  var COOLDOWN = 0.05, VOICES = 6  // a name's repeat gap in s; sounds per 0.25 s
  // nowS is the gate's clock, real time and not fx.update's, so a page that
  // plays sounds without a frame loop still gets them.
  var nowS = (typeof performance !== 'undefined' && performance.now)
    ? function () { return performance.now() / 1000 }
    : function () { return Date.now() / 1000 }
  var sounds = {}, synth = null, lastPlayed = {}, plays = [], unlocked = false
  var audio = null, voices = 0
  function ear() {  // browsers start audio only after a gesture
    unlocked = true
    if (!audio && typeof AudioContext !== 'undefined') audio = new AudioContext()
    if (audio && audio.state === 'suspended') audio.resume()
  }
  addEventListener('pointerdown', ear)
  addEventListener('keydown', ear)
  function tone(hz, volume) {
    if (!hz || !audio || api.muted || volume < 0.05 || voices >= 8) return
    var now = audio.currentTime, o = audio.createOscillator(), g = audio.createGain()
    o.type = hz < 200 ? 'triangle' : 'square'
    o.frequency.setValueAtTime(hz * (0.9 + 0.2 * Math.random()), now)
    o.frequency.exponentialRampToValueAtTime(hz * 0.5, now + 0.15)
    g.gain.setValueAtTime(0.0001, now)
    g.gain.exponentialRampToValueAtTime(0.2 * Math.min(volume, 1), now + 0.005)
    g.gain.exponentialRampToValueAtTime(0.0001, now + 0.2)
    o.connect(g); g.connect(audio.destination)
    o.onended = function () { voices-- }
    o.start(now); o.stop(now + 0.21); voices++
  }
  function buzz(ms) {
    if (ms <= 0 || typeof navigator === 'undefined') return
    var pads = navigator.getGamepads ? navigator.getGamepads() : []
    for (var i = 0; i < pads.length; i++) {
      var act = pads[i] && pads[i].vibrationActuator
      if (!act) continue
      var r = act.playEffect('dual-rumble', { duration: ms, strongMagnitude: Math.min(1, ms / 120), weakMagnitude: 0.5 })
      if (r && r.catch) r.catch(function () {})
    }
    if (navigator.vibrate && matchMedia('(pointer: coarse)').matches) navigator.vibrate(Math.min(ms, 80))
  }
  return api
})()
</script>

WIRING:
- One requestAnimationFrame for the page. Each frame: var g = fx.update(dt),
  step the game with g, then draw. Particles and shake keep running during a
  hit stop, the game does not. With the controls core, drop its own loop and
  call game.update(g) from this one.
- fx.at is the listener: set its x and y (and z in 3D) to the camera or the
  visitor's avatar every frame. Far events shake less and sound quieter.
- Call fx.impact on the frame something happens, never every frame: a shot
  leaves, a hit lands, a body lands hard, a block breaks, something dies. Pass
  mine = true when this visitor's avatar did it or took it. Only those freeze
  the game and rumble.
- strength is what happened, 0 to 2: landing speed over jump speed, damage
  over max hp, the charge power. A landing under a third of jump speed is no
  impact.
- A held beam, an engine, a drill: fx.hum(0.1 to 0.3) every frame while it
  runs. It stops the frame it stops.
- Draw with the shake. Canvas: ctx.save(), translate(fx.shake.x, fx.shake.y),
  rotate(fx.shake.roll), draw the world, restore(), then the HUD unshaken.
  3D: keep the follow position apart, then each frame set camera.position to
  it plus the shake / 100 and camera.rotation.z to the roll. Never feed the
  shake back into the follow, or the camera drifts.
- Particles. Canvas: fx.each(function (p, f) {...}) and fillRect at p.x, p.y,
  p.size * f wide, globalAlpha f. 3D: one THREE.Points with a 500-slot
  position buffer written from fx.each each frame, dead slots moved far away.
  Speeds and GRAVITY are px, so in meters divide them by about 40, and flip
  GRAVITY for y up. For event-specific colors, keep a particle's event or
  color when spawning it; the core above does not store either.
- Layer an impact in time: a brief bright flash at contact, fast sparks or
  fragments moving away, then slower dust that expands and fades. Give each
  layer a distinct lifetime and speed so the motion stays readable. Keep the
  contact point and direction consistent with the actual hit.
- Dust and smoke are soft, translucent shapes with normal alpha blending: they
  can obscure what is behind them. Draw luminous sparks or glows separately
  with additive blending. In 3D use separate materials; depth-test both
  against the world, and normally disable depth writes for transparent layers.
  Reuse bounded slots for each layer, reset all fields on reuse, and retire
  them at the end of their lives. Reuse textures and materials too.
- A body that is hit: fx.pop(body, 0.2 to 0.4), then fx.settle(body, dt) each
  frame. Draw it scaled (1 + pop, 1 - pop), white while body.flash > 0.
- A mute: the M key and a small button toggle fx.muted, kept with
  bd.save('muted', fx.muted) and restored on load with bd.load('muted', false).
  Skip the key while the visitor types in the post form or any other field.
- Multiplayer: share what happened, its actor, position and parameters in an
  action row. Local and remote actions call the same effect renderer, which
  calls fx.impact there, with mine if it happened to you. Generate particles,
  shake, sound and rumble locally; do not send each particle or shake value.
  Apply a shared hit or carve once from the ordered action row; its visual
  layers never do collision or damage. Replay old rows to rebuild shared state
  without replaying their transient effects.

SOUND BEYOND A BLIP: tone() above is one pitched blip. For a sound with
character (an explosion, a coin, a laser, a powerup), the page can import the
vendored ZzFX synth, no CDN: import { zzfx, setVolume } from 'zzfx'. It makes
no sound until the visitor has pressed a key or clicked, and it needs no
setup. zzfx(...params) plays at once; every parameter is optional, and an
empty slot keeps its default. The order:
  volume, randomness, frequency, attack, sustain, release, shape (0 sine,
  1 triangle, 2 saw, 3 tan, 4 noise, 5 square), shapeCurve, slide,
  deltaSlide, pitchJump, pitchJumpTime, repeatTime, noise, modulation,
  bitCrush, delay, sustainVolume, decay, tremolo, filter (negative is high
  pass, positive is low pass, in Hz).
Seconds for the time slots, Hz for frequency. Starting points to
tune, kept in one SOUNDS table like EVENTS:
  coin:     [, , 1675, , .06, .24, 1, 1.82, , , 837, .06]
  thud:     [, , 129, .01, , .15, , , , , , , , 5]
  boom:     [, , 333, .01, 0, .9, 4, 1.9, , , , , , .5, , .6]
  gameover: [, , 925, .04, .3, .6, 1, .3, , 6.27, -184, .09, .17]
gameover is long (about a second), so it is a jingle for the end of a round,
never a shot. A short sound is a small attack and release with a slide.
- Register the table once: fx.sounds(SOUNDS, zzfx), and call
  fx.sound(name, at, strength, mine), or give an EVENTS row sound: 'thud' so
  fx.impact plays it. The core then mutes, waits for a gesture, drops a repeat
  inside 50 ms, caps the sounds started in a quarter second and lowers the
  volume with distance. Never call zzfx at the call site, or none of that
  applies.
- Play on an event, never every frame: each call builds its sound from
  scratch, and a long one costs real time on the page.
- Raise randomness (0.05 is the default) to 0.1 for sounds that repeat fast,
  footsteps, shots, pickups, so they do not sound like a machine gun.
- Keep volume under 1 and call setVolume(0.2 to 0.4) once.
- Changing one number changes the sound a lot. Change one at a time, and
  prefer a nearby preset over inventing a sound from nothing.
- Music is not in ZzFX: load the audio skill for background music.

TUNING: a post that asks for more or less of something changes that event's
row in EVENTS and nothing else. A new kind of event is a new row. Keep the
order: a kill reads bigger than a hit, a hit bigger than a shot. Trauma 0.1
is barely felt, 0.3 is clear, 0.6 is big. A burst of events caps at 1.

MORE, where the action wants it:
- Knockback: the hit body gets velocity along the hit, eased away by drag.
- Recoil: a shot kicks the shooter back a little and the camera 1 to 3 px
  against the aim.
- Windup: a heavy move crouches or glows 0.08 to 0.15s before it fires. Light
  moves fire at once.
- Trails: a fast body leaves a fading copy or particle every 0.02s.
- Speed: FOV up to +10, or streaks at the screen edge, eased with speed.
- Numbers: damage text rises 30 px and fades over 0.6s.
- Sparks: a bright shape for one or two frames at a muzzle or a hit.
- Charge: a bar or a growing glow while held, then an impact scaled by power.
- The last kill: slow the game to 0.3 for 0.4s by scaling g.
`
