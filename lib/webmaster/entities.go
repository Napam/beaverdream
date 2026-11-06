package webmaster

// entitiesReference is the entities skill: kinds and moves from tables, with
// charge, cooldowns, transforms, pooled shots, spawning and a fixed step. The
// model reads it with load_skill. The script block is run by
// entities_test.go, which checks that a kind only runs its own moves, the
// charge curve, the cooldown, the shot range at three frame rates, hits,
// walls, the pool and spawning; change both together.
const entitiesReference = `ENTITIES REFERENCE:
Use this when a page has several kinds of characters, enemies, units or
vehicles, each with its own moves. This core is tested. Copy it whole, then
write your own KINDS, MOVES, SHOTS and BRAINS.

<script>
var world = (function () {
  var STEP = 1 / 60, HOLD = 0.18  // a press held past HOLD charges
  // KINDS: what each kind of thing is and which key runs which move. A thing
  // can only do what its own kind lists. A new kind is a new entry; shared
  // code never asks for a kind by name.
  var KINDS = {
    scout:  { hp: 80,  speed: 7, r: 0.4, moves: { KeyX: 'dash', KeyE: 'shoot' } },
    heavy:  { hp: 160, speed: 4, r: 0.5, moves: { KeyX: 'slam', KeyQ: 'grow' } },
    giant:  { hp: 300, speed: 5, r: 1.2, moves: { KeyX: 'slam', KeyQ: 'shrink' } },
    walker: { hp: 30,  speed: 2, r: 0.4, moves: {}, brain: 'chase', max: 6 }
  }
  // MOVES: cooldown in s. With charge, a tap runs at power min and holding
  // grows it to 1 over charge s; without, a press runs at once at power 1.
  // A run that returns false did not happen: no cooldown, no onMove.
  var MOVES = {
    dash:   { cooldown: 0.6, run: function (e, p) { push(e, 18 * p) } },
    shoot:  { cooldown: 0.15, run: function (e) { return fire(e, 'bolt') !== null } },
    slam:   { cooldown: 0.7, charge: 1, min: 0.3, run: function () {} },
    grow:   { cooldown: 1, run: function (e) { become(e, 'giant') } },
    shrink: { cooldown: 1, run: function (e) { become(e, 'heavy') } }
  }
  // SHOTS: speed in units/s, range in units, damage in hp.
  var SHOTS = { bolt: { speed: 30, range: 25, r: 0.2, damage: 10 } }
  var BRAINS = {
    chase: function (e) {  // walk at the nearest player
      var t = nearest(e, function (o) { return o.player })
      if (!t) { e.mx = e.my = 0; return }
      var d = Math.hypot(t.x - e.x, t.y - e.y) || 1
      e.mx = (t.x - e.x) / d; e.my = (t.y - e.y) / d
      e.face = Math.atan2(e.my, e.mx)
    }
  }
  var EASE = 6  // per s: how fast velocity eases to the input, so pushes fade
  var things = [], shots = [], acc = 0
  for (var i = 0; i < 200; i++) shots.push({ on: false })
  var api = {
    KINDS: KINDS, MOVES: MOVES, SHOTS: SHOTS, BRAINS: BRAINS, things: things, shots: shots,
    // Hooks the page sets: walls (is a circle at x, y with radius r inside
    // one?), and what happens when moves, shots and deaths land.
    blocked: function () { return false },
    onMove: function () {}, onHit: function () {}, onShotEnd: function () {}, onDeath: function () {}
  }

  api.spawn = function (kind, x, y, player) {
    var e = {
      kind: kind, x: x, y: y, px: x, py: y, vx: 0, vy: 0, mx: 0, my: 0, face: 0,
      hp: KINDS[kind].hp, cool: {}, held: {}, player: !!player
    }
    things.push(e)
    return e
  }
  // spawnAway places a kind at a random spot in the bounds at least min from
  // every player, or returns null when its max is reached or no spot is free.
  api.spawnAway = function (kind, bounds, min) {
    var count = 0
    for (var i = 0; i < things.length; i++) if (things[i].kind === kind) count++
    if (KINDS[kind].max && count >= KINDS[kind].max) return null
    for (var tries = 0; tries < 20; tries++) {
      var x = bounds.x + Math.random() * bounds.w, y = bounds.y + Math.random() * bounds.h
      var ok = !api.blocked(x, y, KINDS[kind].r)
      for (var j = 0; ok && j < things.length; j++)
        if (things[j].player && Math.hypot(things[j].x - x, things[j].y - y) < min) ok = false
      if (ok) return api.spawn(kind, x, y)
    }
    return null
  }

  // press and release are the keys of the thing this visitor plays.
  api.press = function (e, code) {
    var name = KINDS[e.kind].moves[code]
    if (!name || e.held[code] != null) return
    if (!MOVES[name].charge) use(e, name, 1)
    else e.held[code] = 0
  }
  api.release = function (e, code) {
    var t = e.held[code]
    delete e.held[code]
    var name = KINDS[e.kind].moves[code]
    if (t == null || !name || !MOVES[name].charge) return
    use(e, name, power(MOVES[name], t))
  }
  // charging is 0 to 1 while a key is held, for a charge bar.
  api.charging = function (e, code) {
    var name = KINDS[e.kind].moves[code]
    return e.held[code] == null || !name ? 0 : power(MOVES[name], e.held[code])
  }

  api.become = become

  // frame takes the real dt and steps the world in fixed STEPs. It returns
  // how far into the next step it is, so drawing can blend px to x. A long
  // pause runs at most 0.1s of steps, not the whole gap.
  api.frame = function (dt) {
    acc = Math.min(acc + dt, 0.1)
    while (acc >= STEP) { step(); acc -= STEP }
    return acc / STEP
  }

  function step() {
    var ease = Math.exp(-EASE * STEP)
    for (var i = 0; i < things.length; i++) {
      var e = things[i], k = KINDS[e.kind]
      e.px = e.x; e.py = e.y
      for (var c in e.cool) e.cool[c] = Math.max(0, e.cool[c] - STEP)
      for (var h in e.held) e.held[h] += STEP
      if (e.driven) continue  // the page moves it, world only runs its moves
      if (k.brain) BRAINS[k.brain](e)
      var len = Math.max(1, Math.hypot(e.mx, e.my))
      var tx = e.mx / len * k.speed, ty = e.my / len * k.speed
      e.vx = tx + (e.vx - tx) * ease; e.vy = ty + (e.vy - ty) * ease
      var nx = e.x + e.vx * STEP, ny = e.y + e.vy * STEP
      if (!api.blocked(nx, e.y, k.r)) e.x = nx; else e.vx = 0
      if (!api.blocked(e.x, ny, k.r)) e.y = ny; else e.vy = 0
    }
    for (var s = 0; s < shots.length; s++) if (shots[s].on) fly(shots[s])
    for (var j = things.length - 1; j >= 0; j--) if (things[j].hp <= 0) api.onDeath(things.splice(j, 1)[0])
  }
  function fly(s) {
    var def = SHOTS[s.type], d = def.speed * STEP
    s.px = s.x; s.py = s.y
    s.x += s.dx * d; s.y += s.dy * d; s.went += d
    if (api.blocked(s.x, s.y, def.r)) return end(s, 'wall')
    for (var i = 0; i < things.length; i++) {
      var e = things[i]
      if (e === s.owner || e.hp <= 0) continue
      if (Math.hypot(e.x - s.x, e.y - s.y) <= def.r + KINDS[e.kind].r) {
        e.hp -= def.damage
        api.onHit(s, e)
        return end(s, 'hit')
      }
    }
    if (s.went >= def.range) end(s, 'range')
  }
  function end(s, why) { s.on = false; api.onShotEnd(s, why) }
  function fire(e, type) {
    for (var i = 0; i < shots.length; i++) {
      var s = shots[i]
      if (s.on) continue
      s.on = true; s.type = type; s.owner = e; s.went = 0
      s.x = s.px = e.x; s.y = s.py = e.y; s.dx = Math.cos(e.face); s.dy = Math.sin(e.face)
      return s
    }
    return null  // every shot is in flight
  }
  function use(e, name, p) {
    if (e.cool[name] > 0 || MOVES[name].run(e, p) === false) return
    e.cool[name] = MOVES[name].cooldown
    api.onMove(name, e, p)
  }
  function power(m, held) {
    var lo = m.min || 0
    return held < HOLD ? lo : lo + (1 - lo) * Math.min(1, (held - HOLD) / m.charge)
  }
  function push(e, v) { e.vx += Math.cos(e.face) * v; e.vy += Math.sin(e.face) * v }  // eases away
  // become turns a thing into another kind: its moves and size change, its
  // health keeps the same share, and its cooldowns carry over.
  function become(e, kind) { e.hp = e.hp / KINDS[e.kind].hp * KINDS[kind].hp; e.kind = kind; e.held = {} }
  function nearest(e, pick) {
    var best = null, bd = Infinity
    for (var i = 0; i < things.length; i++) {
      var o = things[i]
      if (o === e || !pick(o)) continue
      var d = Math.hypot(o.x - e.x, o.y - e.y)
      if (d < bd) { bd = d; best = o }
    }
    return best
  }
  return api
})()
</script>

THE RULE: everything a kind can do is in its KINDS entry. A new kind is a new
entry. Shared code never asks e.kind === 'name'. Two kinds that share a move
point at the same MOVES name; when one needs it different, it gets its own
move. A post about one kind changes that kind's lines and no others.

- Transform (a person who turns into a giant, a car into a plane) is
  world.become(e, other): a second entry with its own stats and moves, one of them
  a move back.
- Moving: world moves every thing on the ground. For the visitor's avatar,
  set e.mx and e.my (-1 to 1) from the held keys and e.face (radians) from
  the last input or the aim; world eases the velocity to them at the kind's
  speed, and a push like a dash eases back. Take speeds and feel from the
  controls skill, not its body code. A platformer keeps the controls core for
  its avatar: set e.driven, copy game.me's center into e.x and e.y each
  frame, and apply pushes in onMove yourself.
- Keys: each kind maps key codes to moves; press and release run them. Skip
  a keydown with e.repeat, or held keys autofire. Show the kind's keys.
- Tap, hold, charge: a move with charge runs on release, scaled by how long
  it was held. It covers "hold to charge" and "tap for small, hold for big".
  Draw world.charging(e, code) as a bar.
- Cooldowns: every move has one. Show it as a sweep on the key's icon.
- Fixed step: world.frame(dt) steps at 60 Hz at any frame rate, so a shot
  flies the same range on every screen. Draw at px + (x - px) * alpha.
- Shots fly from a fixed pool to their range, then end. A shot that moves
  more than half a wall per step needs the space skill's ray. A laser is not
  a shot: it is a ray every tick.
- Hooks: set world.blocked to the walls. With the space skill in 2D:
  function (x, y, r) { return g.boxHits({ x: x - r, y: y - r, w: 2 * r, h: 2 * r }) }.
  Set onMove, onHit, onShotEnd and onDeath to effects (the feel skill's
  fx.impact) and rows. The core draws nothing and has no effects.
- Enemies: a kind with a brain and a max. spawnAway(kind, bounds, min) on a
  timer, min at least a screen from every player. Brains chase, keep
  distance, wander, and shoot only when the space skill's g.sees says so. A
  few readable enemies beat many: wind up 0.4s before an attack.
- Balance lives in the tables: damage in SHOTS, health and speed in KINDS,
  cooldown and charge in MOVES.
- 3D: x and y here are the ground, which is x and z in three.js and in a 3D
  space grid. Pass the feet height yourself: blocked tests
  g.boxHits({ x: x - r, y: FEET, z: y - r, w: 2 * r, h: HEIGHT, d: 2 * r }), and
  effects go to fx.impact(type, { x: e.x, y: HEAD, z: e.y }).
- Multiplayer: each visitor runs their own avatar. Rows carry kind, position,
  facing and the moves run; other pages replay those for effects. The
  shooter's page judges its own hits and sends them as rows. Shared enemies
  follow the controls skill's fixed rule.
`
