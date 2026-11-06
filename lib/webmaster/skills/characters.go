package skills

// charactersReference is the characters skill: three.js figures from body
// plans, animated from how they move, plus the same motion for 2D. The model
// reads it with load_skill. The script block is run by characters_test.go
// against the vendored three.js, which checks every plan's height, feet on
// the ground without sliding, gait phases, arm swing, bob, lean, landing,
// smooth blends, seeded looks, actions, wheels, wings, hops and the 2D
// numbers; change both together.
const charactersReference = `CHARACTERS REFERENCE:
Use this for any character someone plays or meets: players, enemies,
animals, pets, monsters, robots, vehicles. The kit is a helper: it gives a
body a skeleton, a walk, a run, a jump and a starting shape. The design is
yours. This core is tested. Copy it whole, in the module script or a plain
script before it.

<script>
function characterKit(THREE) {
  var TAU = Math.PI * 2, ZERO = [0, 0, 0]
  function clamp(v, a, b) { return v < a ? a : v > b ? b : v }
  function smooth(t) { t = clamp(t, 0, 1); return t * t * (3 - 2 * t) }
  function toward(cur, goal, k, dt) { return cur + (goal - cur) * (1 - Math.exp(-k * dt)) }
  function frac(x) { return x - Math.floor(x) }
  function wrap(a) { return a - TAU * Math.floor((a + Math.PI) / TAU) }
  function rng(seed) {
    var s = 2166136261, text = String(seed)
    for (var i = 0; i < text.length; i++) { s ^= text.charCodeAt(i); s = Math.imul(s, 16777619) }
    function r() {
      s = (s + 0x6D2B79F5) | 0
      var t = Math.imul(s ^ (s >>> 15), 1 | s)
      t = (t + Math.imul(t ^ (t >>> 7), 61 | t)) ^ t
      return ((t ^ (t >>> 14)) >>> 0) / 4294967296
    }
    r.pick = function (list) { return list[Math.floor(r() * list.length)] }
    return r
  }

  // motion turns movement into angles, with no three.js, so 2D can use it.
  // d.legs: [{ L, phase, bend, splay, rest }]: L hip to sole, phase 0..1 in
  // the step cycle, bend -1 for a joint that points back (hind legs),
  // splay radians outward, rest the share of L the hip stands at. Each frame
  // update(dt, { speed, vy, turn, grounded, aim }): speed along facing,
  // negative backward; vy up; turn rad/s; aim { yaw, pitch } for the head,
  // relative to the body. Then read legs[i].thigh and knee (radians, forward
  // positive), bodyY (add to the hip height), arms[i].swing, raise and
  // elbow, lean, roll, pitch, headYaw, headPitch, squash, breath, blink,
  // hopY and flap.
  function motion(d) {
    var legs = (d.legs || []).map(function (l) {
      var splay = l.splay || 0, rest = (l.rest || 0.97) * l.L
      // A splayed foot keeps its sideways distance from the hip, side.
      return { L: l.L, bend: l.bend || 1, phase: l.phase || 0, R: rest * Math.cos(splay), side: rest * Math.sin(splay),
        spread: splay, u: 0, h: 0, thigh: 0, knee: 0 }
    })
    var arms = []
    for (var i = 0; i < (d.arms || 0); i++) arms.push({ swing: 0, raise: 0, elbow: 0.2 })
    var upright = d.lean == null ? 1 : d.lean
    var m = { legs: legs, arms: arms, hip: legs.length ? legs[0].R : (d.H || 1) * 0.3, t: 0, cycle: 0, speed: 0, vy: 0,
      air: 0, walk: 0, crouch: 0, bodyY: 0, lean: 0, roll: 0, pitch: 0, headYaw: 0, headPitch: 0,
      breath: 0, blink: 1, hop: 0, hopY: 0, squash: 1, flap: 0 }
    var airTime = 0, fall = 0, dip = 0, run = 0, rise = 0, nextBlink = 2, seed = frac((d.seed || 0) * 0.618 + 0.1), acts = {}, offsets = {}
    // act plays a move on a named joint for seconds: fn(k), k 0..1, returns
    // [x, y, z] radians added to the pose, faded in and out. A new act on
    // the same joint replaces the old one.
    m.act = function (name, seconds, fn) { acts[name] = { t: 0, len: seconds, fn: fn } }
    m.offset = function (name) { return offsets[name] || ZERO }
    m.update = function (dt, s) {
      dt = clamp(dt || 0, 0, 0.1)
      s = s || {}
      if (dt <= 0) return m
      m.t += dt
      var speed = s.speed || 0, vy = s.vy || 0, turn = s.turn || 0, grounded = s.grounded !== false, R = m.hip
      var accel = clamp((speed - m.speed) / dt, -60 * R, 60 * R)
      m.speed = speed; m.vy = vy
      if (!grounded) { airTime += dt; fall = Math.min(fall, vy) } else {
        if (airTime > 0.15) dip = Math.max(dip, clamp(-fall / (12 * R), 0.25, 1))
        airTime = 0; fall = 0
      }
      dip = toward(dip, 0, 6, dt)
      m.crouch = toward(m.crouch, dip, 14, dt)  // landing sinks in fast, then rises slowly
      m.air = toward(m.air, grounded ? 0 : 1, grounded ? 12 : 8, dt)
      var pace = Math.abs(speed) / R  // leg lengths per second
      m.walk = toward(m.walk, smooth(pace / 1.5), 10, dt)
      var w = m.walk * (1 - m.air)
      run = toward(run, smooth(pace / 3 - 1), 6, dt)
      // D is the ground one step cycle covers, and stance its share with a
      // foot down: a half when walking, less when running, so both feet leave
      // the ground and the stride can grow instead of the legs blurring. Slow
      // walks take shorter steps, not sliding ones; only the slowest shuffle
      // blends toward standing.
      var stance = 0.5 - 0.2 * run, g = Math.max(w, 0.35)
      var D = R * Math.min(1.05 / stance, Math.max(1.3 + 0.12 * pace, pace / 3)) * g
      if (grounded) m.cycle = frac(m.cycle + speed * dt / D)
      rise = toward(rise, vy > 0 ? 1 : 0, 8, dt)
      var A = D * stance / 2, lift = 0.1 * R + 0.15 * A, yb = 0, stepW = w / g
      legs.forEach(function (l) {
        // Stance: the foot moves back exactly as fast as the body moves
        // forward, so it stays planted. Swing: it lifts and returns.
        var p = frac(m.cycle + l.phase), u, h
        if (p < stance) { u = A * (1 - 2 * p / stance); h = 0 } else {
          var q = (p - stance) / (1 - stance)
          u = A * (2 * smooth(q) - 1); h = lift * Math.sin(Math.PI * q)
        }
        var ua = l.R * (0.12 + 0.12 * rise) * (l.phase < 0.5 ? 1 : 0.4), ha = l.R * (0.12 + 0.35 * rise)
        l.u = u * stepW + ua * m.air; l.h = h * stepW + ha * m.air
        // The body sinks just enough for the farthest-reaching foot.
        yb = Math.min(yb, Math.sqrt(Math.max(0, 0.96 * l.L * l.L - l.u * l.u - l.side * l.side)) + l.h - l.R)
      })
      m.bodyY = yb - m.crouch * 0.25 * R
      legs.forEach(function (l) {  // two equal bones reaching for (side, -down, u)
        var down = l.R + m.bodyY - l.h, plane = Math.sqrt(down * down + l.side * l.side)
        l.spread = Math.atan2(l.side, down)
        var r = clamp(Math.sqrt(l.u * l.u + plane * plane), 0.3 * l.L, 0.999 * l.L)
        var k = 2 * Math.acos(r / l.L), a = Math.atan2(l.u, plane)
        l.thigh = a + l.bend * k / 2; l.knee = l.bend * k
      })
      arms.forEach(function (a, i) {
        var leg = legs[i]
        a.swing = (leg ? -leg.u / R * 1.2 : 0) * w + m.air * (0.7 * rise - 0.2)
        a.raise = 0.08 + m.air * (0.5 + 0.4 * rise)
        a.elbow = 0.2 + 0.9 * run * w + 0.3 * m.air
      })
      m.lean = toward(m.lean, clamp(0.03 * pace + 0.02 * accel / R, -0.15, 0.35) * upright, 6, dt)
      m.roll = toward(m.roll, clamp(-0.05 * turn * pace, -0.4, 0.4) * upright, 6, dt)
      m.pitch = toward(m.pitch, clamp(-0.015 * accel / R, -0.15, 0.15), 6, dt)
      var gy = 0, gp = 0
      if (s.aim) { gy = clamp(s.aim.yaw || 0, -1.1, 1.1); gp = clamp(s.aim.pitch || 0, -0.6, 0.6) } else if (d.glance !== false) {
        gy = 0.4 * Math.sin(m.t * 0.45 + seed * TAU) * Math.sin(m.t * 0.17) * (1 - w)
        gp = 0.05 * Math.sin(m.t * 0.3)
      }
      m.headYaw = toward(m.headYaw, gy, 8, dt); m.headPitch = toward(m.headPitch, gp, 8, dt)
      m.breath = 0.025 * Math.sin(m.t * 2.4) * (1 - w)
      m.blink = 1
      if (m.t > nextBlink) {
        m.blink = 0.1
        if (m.t > nextBlink + 0.12) { m.blink = 1; nextBlink = m.t + 2 + 3 * frac(nextBlink * 0.618 + seed) }
      }
      m.squash = 1 - 0.3 * m.crouch
      if (grounded) m.hop += Math.abs(speed) * dt / (R * 2.5)
      m.hopY = Math.sin(Math.PI * frac(m.hop)) * R * 0.5 * w
      m.flap = Math.sin(m.t * TAU * (vy > 0.5 ? 6 : 3.5))
      offsets = {}
      for (var name in acts) {
        var act = acts[name]
        act.t += dt
        if (act.t >= act.len) { delete acts[name]; continue }
        var k = act.t / act.len, fade = smooth(Math.min(1, k / 0.15, (1 - k) / 0.15)), v = act.fn(k) || ZERO
        offsets[name] = [v[0] * fade, v[1] * fade, v[2] * fade]
      }
      return m
    }
    return m
  }

  // look picks colors and extras from a seed, such as a visitor id, so
  // everyone looks different and the same on every screen. o.palette and
  // o.skins are color lists; o.extras the extras it may pick from.
  var PALETTE = ['#d9483b', '#2f6fd6', '#f2b233', '#3fa66b', '#8a4fd0', '#e46aa0', '#26a3b5', '#e97b2c']
  var SKINS = ['#f6d7bd', '#eec09b', '#d39a6c', '#a86c45', '#6f4429']
  function look(seed, o) {
    o = o || {}
    var r = rng(seed), pal = o.palette || PALETTE, main = r.pick(pal), second = main
    for (var i = 0; i < 8 && second === main && pal.length > 1; i++) second = r.pick(pal)
    var out = { colors: { main: main, second: second, skin: r.pick(o.skins || SKINS) }, build: r() }
    if (o.extras) out.extras = o.extras.filter(function () { return r() < (o.chance == null ? 0.4 : o.chance) })
    return out
  }

  var DEFAULTS = { plan: 'biped', height: 1.8, build: 0.5, head: 0.15, shade: 'toon', outline: true }
  var COLORS = { main: '#d9483b', second: '#2f3d6b', skin: '#f1c9a5', eye: '#17151f', outline: '#17151f', horn: '#efe6cf' }
  var PLAN_EXTRAS = { quadruped: ['ears', 'tail'] }
  var geos = {}, mats = {}, gradient = null, made = 0

  function shape(kind, a, b, c) {
    var key = kind + ':' + a.toFixed(4) + ':' + (b || 0).toFixed(4) + ':' + (c || 0).toFixed(4)
    if (geos[key]) return geos[key]
    var g = kind === 'ball' ? new THREE.SphereGeometry(a, 18, 14)
      : kind === 'cap' ? new THREE.CapsuleGeometry(a, b, 4, 12)
      : kind === 'box' ? new THREE.BoxGeometry(a, b, c)
      : kind === 'cone' ? new THREE.ConeGeometry(a, b, 12)
      : kind === 'cyl' ? new THREE.CylinderGeometry(a, a, b, 18)
      : new THREE.PlaneGeometry(a, b, 1, 4)
    g.computeBoundingBox()
    return (geos[key] = g)
  }
  function material(color, shade, two) {
    var key = color + shade + (two ? '2' : '')
    if (mats[key]) return mats[key]
    var side = two ? THREE.DoubleSide : THREE.FrontSide
    if (shade === 'outline') return (mats[key] = new THREE.MeshBasicMaterial({ color: color, side: THREE.BackSide }))
    if (shade === 'flat') return (mats[key] = new THREE.MeshBasicMaterial({ color: color, side: side }))
    if (shade === 'smooth') return (mats[key] = new THREE.MeshStandardMaterial({ color: color, roughness: 0.8, side: side }))
    if (!gradient) {
      gradient = new THREE.DataTexture(new Uint8Array([90, 170, 255]), 3, 1, THREE.RedFormat)
      gradient.minFilter = gradient.magFilter = THREE.NearestFilter
      gradient.needsUpdate = true
    }
    return (mats[key] = new THREE.MeshToonMaterial({ color: color, gradientMap: gradient, side: side }))
  }

  // rig builds a figure. spec: plan (biped, quadruped, bug, flyer, blob,
  // vehicle), height (ground to top), build 0 slim .. 1 chunky, head (head
  // radius as a share of height), colors { main, second, skin, eye,
  // outline, horn }, extras (cape, hat, ears, horns, tail, wings), shade
  // (toon, smooth, flat), outline, face, legs (a bug's count, 0 for a
  // hovering flyer), length (quadruped, vehicle), shapes ({ part: 'box',
  // 'ball', 'cyl' or 'cone' }). Parts: torso, head, neck, snout, upperArm,
  // forearm, hand, thigh, shin, foot, wing, tail, wheel, cabin, hat, ears,
  // horns, cape. Put rig.root in the scene,
  // set its position (the feet) and rotation.y (the controls skill's yaw:
  // forward is -sin yaw, -cos yaw), then call rig.pose(dt) every frame.
  function rig(spec) {
    var s = {}, k
    spec = spec || {}
    for (k in DEFAULTS) s[k] = DEFAULTS[k]
    for (k in spec) if (spec[k] != null) s[k] = spec[k]
    s.colors = {}
    for (k in COLORS) s.colors[k] = COLORS[k]
    for (k in spec.colors || {}) s.colors[k] = spec.colors[k]
    var H = s.height, b = s.build, C = s.colors, extras = s.extras || PLAN_EXTRAS[s.plan] || []
    var root = new THREE.Group(), turned = new THREE.Group()
    turned.rotation.y = Math.PI  // built facing +z, so yaw 0 faces -z
    root.add(turned)
    var body = pivot(turned), chest = pivot(body)
    var R = { root: root, joints: { body: body, chest: chest }, legs: [], arms: [], wings: [],
      tail: [], wheels: [], eyes: [], parts: {}, base: 0, travel: 0 }, legDesc = []

    function pivot(parent, x, y, z) { var g = new THREE.Group(); g.position.set(x || 0, y || 0, z || 0); parent.add(g); return g }
    // part makes one named mesh; r.parts[name] lists every mesh of that name.
    function part(name, parent, kind, a, b2, c, color, x, y, z, two) {
      var g = shape(kind, a, b2, c), mesh = new THREE.Mesh(g, material(color, s.shade, two))
      mesh.position.set(x || 0, y || 0, z || 0)
      parent.add(mesh)
      ;(R.parts[name] = R.parts[name] || []).push(mesh)
      if (s.outline && kind !== 'plane') {
        var size = g.boundingBox.getSize(new THREE.Vector3()), t = 2 * H * 0.016
        var line = new THREE.Mesh(g, material(C.outline, 'outline'))
        line.scale.set(1 + t / Math.max(size.x, 1e-3), 1 + t / Math.max(size.y, 1e-3), 1 + t / Math.max(size.z, 1e-3))
        mesh.add(line)
      }
      return mesh
    }
    function bone(name, parent, from, to, r, color) {  // a capsule from one point to another
      var dir = new THREE.Vector3(to[0] - from[0], to[1] - from[1], to[2] - from[2]), len = dir.length()
      var mesh = part(name, parent, 'cap', r, Math.max(0.001, len), 0, color,
        (from[0] + to[0]) / 2, (from[1] + to[1]) / 2, (from[2] + to[2]) / 2)
      mesh.quaternion.setFromUnitVectors(new THREE.Vector3(0, 1, 0), dir.normalize())
      return mesh
    }
    function leg(x, z, L, r, o) {
      var hip = pivot(body, x, 0, z), sp = pivot(hip), thigh = pivot(sp), l = L / 2
      part('thigh', thigh, 'cap', r, l, 0, o.color, 0, -l / 2, 0)
      var knee = pivot(thigh, 0, -l, 0)
      part('shin', knee, 'cap', r * 0.9, Math.max(0.001, l - 1.8 * r), 0, o.color, 0, -l / 2, 0)
      // The ankle stays level and one radius above the ground; the sole is
      // straight under it.
      var ankle = pivot(knee, 0, -l, 0)
      if (o.foot) part('foot', ankle, 'box', r * 2.2, r * 1.6, r * 3.4, o.foot, 0, -r * 0.2, r * 0.8)
      else part('foot', ankle, 'ball', r, 0, 0, o.color, 0, 0, 0)
      R.legs.push({ hip: hip, splay: sp, thigh: thigh, knee: knee, ankle: ankle, sole: pivot(ankle, 0, -r, 0), side: o.side })
      legDesc.push({ L: L, phase: o.phase || 0, bend: o.bend || 1, splay: o.splay || 0, rest: o.rest })
    }
    function arm(x, y, len, r, side) {
      var sh = pivot(chest, x, y, 0), l = len / 2
      part('upperArm', sh, 'cap', r, l, 0, C.main, 0, -l / 2, 0)
      var el = pivot(sh, 0, -l, 0)
      part('forearm', el, 'cap', r * 0.9, l, 0, C.main, 0, -l / 2, 0)
      var hand = pivot(el, 0, -l - r * 0.3, 0)
      part('hand', hand, 'ball', r * 1.45, 0, 0, C.skin, 0, 0, 0)
      R.arms.push({ shoulder: sh, elbow: el, hand: hand, side: side })
    }
    function head(parent, x, y, z, hr, center, color) {
      var hd = pivot(parent, x, y, z)
      R.joints.head = hd; R.headR = hr; R.headC = center
      if (color) part('head', hd, 'ball', hr, 0, 0, color, center[0], center[1], center[2])
      if (s.face !== false) [-1, 1].forEach(function (side) {  // white backs, so eyes read on any color
        var e = new THREE.Mesh(shape('ball', hr * 0.19), material('#ffffff', 'flat'))
        e.position.set(center[0] + side * hr * 0.36, center[1] + hr * 0.12, center[2] + hr * 0.9)
        e.scale.set(1, 1.3, 0.55)
        var pupil = new THREE.Mesh(shape('ball', hr * 0.12), material(C.eye, 'flat'))
        pupil.position.set(0, -hr * 0.02, hr * 0.12)
        e.add(pupil)
        hd.add(e); R.eyes.push(e)
      })
      return hd
    }
    function wings(parent, x, y, z, span, chord) {
      [-1, 1].forEach(function (side) {
        var p = pivot(parent, side * x, y, z)
        p.rotation.order = 'YXZ'
        part('wing', p, 'ball', span / 2, 0, 0, C.second, side * span / 2, 0, 0).scale.set(1, 0.1, chord / span)
        R.wings.push({ pivot: p, side: side })
      })
    }

    var plan = s.plan
    if (plan === 'biped') {
      var hr = H * s.head, L = H * 0.36, lr = H * (0.04 + 0.03 * b), tw = H * (0.2 + 0.1 * b)
      var th = Math.max(H * 0.15, H - 0.97 * L - lr - 2 * hr - 0.02 * H), ar = lr * 0.85
      R.base = 0.97 * L + lr
      ;[1, -1].forEach(function (side) { leg(side * tw * 0.27, 0, L, lr, { side: side, phase: side > 0 ? 0 : 0.5, color: C.second, foot: C.main }) })
      part('torso', chest, 'cap', tw / 2, Math.max(0.001, th - tw), 0, C.main, 0, th / 2, 0).scale.z = 0.72
      head(chest, 0, th + 0.02 * H, 0, hr, [0, hr, 0], C.skin)
      ;[1, -1].forEach(function (side) { arm(side * (tw / 2 + ar * 0.6), th - ar * 1.2, H * 0.25, ar, side) })
      R.back = { parent: chest, at: [0, th - ar, -tw * 0.36], w: tw * 1.05, len: H * 0.48 }
      R.tailAt = { parent: body, at: [0, 0, -tw * 0.3], len: H * 0.4, r: lr * 0.8 }
    } else if (plan === 'quadruped') {
      var qL = H * 0.5, qr = H * (0.045 + 0.03 * b), rb = H * (0.15 + 0.06 * b), len = s.length || H * 1.1
      var qh = H * s.head * 1.05, by = rb * 0.55
      R.base = 0.95 * qL + qr
      ;[[1, 1, 0], [-1, 1, 0.5], [1, -1, 0.5], [-1, -1, 0]].forEach(function (q) {
        leg(q[0] * rb * 0.62, q[1] * len * 0.34, qL, qr, { side: q[0], phase: q[2], bend: q[1], rest: 0.95, color: C.main, foot: C.second })
      })
      part('torso', chest, 'cap', rb, Math.max(0.001, len - 2 * rb), 0, C.main, 0, by, 0).rotation.x = Math.PI / 2
      var hy = H - R.base - 1.3 * qh, hz = len / 2 + qh * 0.3
      bone('neck', chest, [0, by, len / 2 - rb * 0.6], [0, hy, hz], rb * 0.5, C.main)
      var qhd = head(chest, 0, hy, hz, qh, [0, qh * 0.3, qh * 0.35], C.main)
      part('snout', qhd, 'ball', qh * 0.5, 0, 0, C.skin, 0, qh * 0.05, qh * 1.15)
      R.back = { parent: chest, at: [0, by + rb, len * 0.2], w: rb * 1.6, len: len * 0.45 }
      R.tailAt = { parent: chest, at: [0, by + rb * 0.3, -len / 2], len: len * 0.55, r: rb * 0.28 }
    } else if (plan === 'bug') {
      var n = Math.max(2, Math.round((s.legs || 6) / 2)), bL = H * 1.05, br = H * 0.035, brb = H * 0.36
      R.base = 0.7 * bL * Math.cos(0.95) + br
      for (var i = 0; i < n; i++) [1, -1].forEach(function (side) {
        leg(side * brb * 0.55, brb * 1.1 * (0.6 - 1.2 * i / (n - 1)), bL, br,
          { side: side, phase: ((i + (side > 0 ? 0 : 1)) % 2) * 0.5, splay: 0.95, rest: 0.7, color: C.second })
      })
      part('torso', chest, 'ball', brb, 0, 0, C.main, 0, brb * 0.35, 0).scale.set(1, 0.75, 1.35)
      var bh = H * 0.2
      head(chest, 0, brb * 0.4, brb * 1.3, bh, [0, 0, bh * 0.5], C.second)
      R.back = { parent: chest, at: [0, brb, 0], w: brb * 1.4, len: brb * 1.5 }
      R.tailAt = { parent: chest, at: [0, brb * 0.35, -brb * 1.3], len: H * 0.5, r: br * 1.4 }
    } else if (plan === 'flyer') {
      var fr = H * 0.24, fh = H * s.head * 1.1
      if (s.legs === 0) { R.base = H * 0.32; R.hover = true } else {
        var fL = H * 0.24, flr = H * 0.03
        R.base = 0.9 * fL + flr
        ;[1, -1].forEach(function (side) { leg(side * fr * 0.4, 0, fL, flr, { side: side, phase: side > 0 ? 0 : 0.5, rest: 0.9, color: C.second, foot: C.second }) })
      }
      part('torso', chest, 'ball', fr, 0, 0, C.main, 0, fr * 0.9, 0).scale.z = 1.15
      head(chest, 0, fr * 1.55, fr * 0.45, fh, [0, fh * 0.5, 0], C.main)
      wings(chest, fr * 0.85, fr * 1.1, 0, H * 0.8, H * 0.3)
      R.back = { parent: chest, at: [0, fr * 1.6, -fr * 0.8], w: fr * 1.4, len: H * 0.4 }
      R.tailAt = { parent: chest, at: [0, fr * 0.8, -fr * 1.05], len: H * 0.35, r: fr * 0.2 }
    } else if (plan === 'blob') {
      var or = H / 2
      R.blob = part('torso', chest, 'ball', or, 0, 0, C.main, 0, or, 0)
      head(chest, 0, or, 0, or, [0, 0, 0])
      R.back = { parent: chest, at: [0, or * 1.6, -or * 0.6], w: or, len: or }
      R.tailAt = { parent: chest, at: [0, or * 0.4, -or * 0.95], len: or, r: or * 0.15 }
    } else if (plan === 'vehicle') {
      var vl = s.length || H * 2.2, wr = H * 0.22, vw = vl * 0.5
      R.base = wr; R.vehicle = true
      part('torso', chest, 'box', vw, H * 0.36, vl, C.main, 0, H * 0.16, 0)
      if (s.face == null) s.face = false
      var cab = head(chest, 0, H * 0.49, -vl * 0.08, H * 0.18, [0, 0, vl * 0.24 - H * 0.18])
      part('cabin', cab, 'box', vw * 0.82, H * 0.3, vl * 0.48, C.second, 0, 0, 0)
      ;[1, -1].forEach(function (side) {
        [0.32, -0.32].forEach(function (zf) {
          var p = pivot(body, side * vw / 2, 0, zf * vl)
          part('wheel', p, 'cyl', wr, vw * 0.2, 0, C.outline, 0, 0, 0).rotation.z = Math.PI / 2
          R.wheels.push({ pivot: p, r: wr })
        })
      })
    } else throw new Error('characterKit: no plan ' + plan)

    var hd = R.joints.head, hr2 = R.headR, hc = R.headC || [0, 0, 0]
    function has(name) { return extras.indexOf(name) >= 0 }
    if (hd && has('hat')) part('hat', hd, 'cone', hr2 * 0.85, hr2 * 1.3, 0, C.second, hc[0], hc[1] + hr2 * 1.35, hc[2])
    if (hd && has('ears')) [-1, 1].forEach(function (side) {
      part('ears', hd, 'cone', hr2 * 0.28, hr2 * 0.6, 0, C.main, hc[0] + side * hr2 * 0.55, hc[1] + hr2 * 0.95, hc[2]).rotation.z = -side * 0.4
    })
    if (hd && has('horns')) [-1, 1].forEach(function (side) {
      var horn = part('horns', hd, 'cone', hr2 * 0.14, hr2 * 0.7, 0, C.horn, hc[0] + side * hr2 * 0.45, hc[1] + hr2 * 1.0, hc[2] + hr2 * 0.15)
      horn.rotation.set(-0.3, 0, -side * 0.45)
    })
    if (R.back && has('cape')) {
      var cp = pivot(R.back.parent, R.back.at[0], R.back.at[1], R.back.at[2])
      part('cape', cp, 'plane', R.back.w, R.back.len, 0, C.second, 0, -R.back.len / 2, 0, true)
      R.joints.cape = cp
    }
    if (R.back && has('wings') && plan !== 'flyer') wings(R.back.parent, R.back.w * 0.3, R.back.at[1], R.back.at[2], H * 0.45, H * 0.18)
    if (R.tailAt && has('tail')) {
      var tp = R.tailAt.parent, seg = R.tailAt.len / 3, at = R.tailAt.at
      for (var j = 0; j < 3; j++) {
        var tj = pivot(tp, at[0], at[1], at[2])
        bone('tail', tj, [0, 0, 0], [0, seg * 0.3, -seg], R.tailAt.r * (1 - j * 0.25), C.main)
        R.tail.push(tj); tp = tj; at = [0, seg * 0.3, -seg]
      }
    }
    R.legs.forEach(function (l) { if (l.side > 0 && !R.joints.legL) R.joints.legL = l.thigh; if (l.side < 0 && !R.joints.legR) R.joints.legR = l.thigh })
    R.arms.forEach(function (a) {
      R.joints[a.side > 0 ? 'armL' : 'armR'] = a.shoulder
      R.joints[a.side > 0 ? 'handL' : 'handR'] = a.hand
    })
    R.wings.forEach(function (w) { R.joints[w.side > 0 ? 'wingL' : 'wingR'] = w.pivot })
    if (R.tail.length) R.joints.tail = R.tail[0]
    // shapes swaps a part's shape for another of the same size and place.
    for (var pn in s.shapes || {}) {
      var kind = s.shapes[pn]
      if (['box', 'ball', 'cyl', 'cone'].indexOf(kind) < 0) continue
      ;(R.parts[pn] || []).forEach(function (mesh) {
        var size = mesh.geometry.boundingBox.getSize(new THREE.Vector3())
        var g = kind === 'box' ? shape('box', 1, 1, 1) : shape(kind, 0.5, 1)
        mesh.geometry = g; mesh.scale.multiply(size)
        mesh.children.forEach(function (c) { if (c.material.side === THREE.BackSide) c.geometry = g })
      })
    }

    var M = R.motion = motion({ legs: legDesc, arms: R.arms.length, H: H, seed: rng(JSON.stringify(spec) + ':' + made++)(),
      lean: plan === 'biped' || plan === 'vehicle' ? 1 : plan === 'blob' ? 0 : 0.35, glance: !R.vehicle })
    var last = null, sm = { speed: 0, vy: 0, turn: 0 }

    // pose reads how far the root moved since the last call, so a remote
    // player eased toward its rows animates on its own. It counts as on the
    // ground when the root is at o.floor (default 0). o overrides: speed,
    // vy, turn, grounded (pass it when you know: a roof, a platform), floor,
    // aim { yaw, pitch } relative to the body.
    R.pose = function (dt, o) {
      o = o || {}
      var p = root.position, yaw = root.rotation.y
      if (!last) last = { x: p.x, y: p.y, z: p.z, yaw: yaw }
      var vx = 0, vy = 0, vz = 0, turn = 0
      if (dt > 0 && dt < 0.25) { vx = (p.x - last.x) / dt; vy = (p.y - last.y) / dt; vz = (p.z - last.z) / dt; turn = wrap(yaw - last.yaw) / dt }
      if (vx * vx + vy * vy + vz * vz > 1600 * H * H) { vx = vy = vz = turn = 0 }  // a teleport, not a step
      dt = clamp(dt || 0, 0, 0.1)
      last = { x: p.x, y: p.y, z: p.z, yaw: yaw }
      R.travel += -(vx * Math.sin(yaw) + vz * Math.cos(yaw)) * dt  // wheels roll the real distance
      var f = 1 - Math.exp(-14 * dt)
      sm.speed += (-(vx * Math.sin(yaw) + vz * Math.cos(yaw)) - sm.speed) * f
      sm.vy += (vy - sm.vy) * f; sm.turn += (turn - sm.turn) * f
      M.update(dt, {
        speed: o.speed != null ? o.speed : sm.speed, vy: o.vy != null ? o.vy : sm.vy,
        turn: o.turn != null ? o.turn : sm.turn, aim: o.aim,
        grounded: o.grounded != null ? o.grounded : p.y <= (o.floor || 0) + H * 0.02
      })
      apply()
      return R
    }
    // act: see motion.act. Joints: body, chest, head, armL, armR, handL,
    // handR, legL, legR, wingL, wingR, tail, cape. For an arm, -x swings it forward and
    // z lifts it to the side (+ for armL, - for armR).
    R.act = function (name, seconds, fn) { M.act(name, seconds, fn) }
    R.remove = function () { if (root.parent) root.parent.remove(root) }

    function apply() {
      var m = M, wide = 1 + (1 - m.squash) * 0.5
      body.position.y = R.base + m.bodyY
      body.rotation.set(0, 0, 0); body.scale.set(1, 1, 1)
      if (R.vehicle) { chest.rotation.set(m.pitch, 0, -m.roll * 0.4); chest.scale.set(1, 1, 1) } else {
        chest.rotation.set(m.lean, 0, m.roll)
        chest.scale.set(wide, (1 + m.breath) * m.squash, wide)
      }
      if (R.hover) body.position.y += H * 0.05 * Math.sin(m.t * 2.2)
      if (R.blob) {
        var sy = (1 - 0.15 * Math.cos(TAU * m.hop) * m.walk * (1 - m.air)) * m.squash * (1 + m.breath)
        body.position.y += m.hopY
        chest.scale.set(1 / Math.sqrt(sy), sy, 1 / Math.sqrt(sy))
      }
      R.legs.forEach(function (l, i) {
        var g = m.legs[i]
        l.splay.rotation.set(0, 0, l.side * g.spread)
        l.thigh.rotation.set(-g.thigh, 0, 0); l.knee.rotation.set(g.knee, 0, 0); l.ankle.rotation.set(g.thigh - g.knee, 0, -l.side * g.spread)
      })
      R.arms.forEach(function (a, i) {
        var g = m.arms[i]
        a.shoulder.rotation.set(-g.swing, 0, a.side * g.raise); a.elbow.rotation.set(-g.elbow, 0, 0)
      })
      var hd2 = R.joints.head
      if (hd2) {
        if (R.vehicle) hd2.rotation.set(-m.headPitch, m.headYaw, 0)
        else hd2.rotation.set(-m.headPitch - m.lean * 0.6, m.headYaw, -m.roll * 0.5)
      }
      R.eyes.forEach(function (e) { e.scale.y = 1.3 * m.blink })
      if (R.joints.cape) R.joints.cape.rotation.set(0.12 + Math.min(1.2, Math.abs(m.speed) / H * 0.25 + Math.max(0, -m.vy) * 0.04) +
        0.06 * Math.sin(m.t * 9) * m.walk, 0, 0)
      R.tail.forEach(function (t, i) {
        t.rotation.set(0.15 - 0.1 * m.walk, Math.sin(m.t * (3 + 4 * m.walk) - i * 0.7) * (0.25 + 0.2 * m.walk), 0)
      })
      var open = R.hover ? 1 : m.air, beat = m.vy < -1 && !R.hover ? 0.1 + 0.08 * m.flap : 0.15 + 0.75 * m.flap
      // Folded wings stand on edge and lie back along the body; open ones
      // beat up and down.
      R.wings.forEach(function (w) {
        w.pivot.rotation.set(1.4 * (1 - open), w.side * 1.35 * (1 - open), w.side * (-0.25 * (1 - open) + beat * open))
      })
      R.wheels.forEach(function (w) { w.pivot.rotation.set(R.travel / w.r, 0, 0) })
      for (var name in R.joints) {
        var off = m.offset(name)
        if (off === ZERO) continue
        var j = R.joints[name].rotation
        j.set(j.x + off[0], j.y + off[1], j.z + off[2])
      }
    }
    apply()
    return R
  }

  return { rig: rig, motion: motion, look: look, plans: ['biped', 'quadruped', 'bug', 'flyer', 'blob', 'vehicle'] }
}
</script>

DESIGN FIRST: before any code, decide who each character is in this
board's world and what makes them recognizable at a glance: a silhouette,
one or two signature details, colors that fit. Superheroes look like
superheroes (a costume, a mask, an emblem, a cape or none), animals look
like those animals (ears, snout, tail, the right build), robots look built.
The defaults are a starting point, not a style. Two boards should never get
the same figure, and a good character is fun to look at.

- Setup: const kit = characterKit(THREE). One rig per character:
  const r = kit.rig({ plan, height, build, head, colors, extras, shapes }),
  then scene.add(r.root).
- Plans are bodies, not looks: biped (two legs and arms), quadruped (four
  legs; length for a long body), bug (many splayed legs: 8, 6), flyer
  (wings; legs 0 to hover), blob (a ball with a face), vehicle (wheels; the
  head joint is the cabin and turns with aim). Pick the body that moves
  right, then make it look like the character.
- Make it yours, from cheap to rich:
  - height, build (0 slim .. 1 chunky), head (0.1 .. 0.25 of height),
    colors { main, second, skin, eye, outline, horn }, extras (cape, hat,
    ears, horns, tail, wings).
  - shapes swaps a part's shape at the same size: { head: 'box', torso:
    'box' } for a robot, { head: 'cone' } for a wizard's helmet, { foot:
    'ball' } for paws.
  - r.parts[name] lists that part's meshes (torso, head, neck, snout,
    upperArm, forearm, hand, thigh, shin, foot, wing, tail, wheel, cabin,
    hat, ears, horns, cape). Hide one (m.visible = false), scale it, or
    recolor it with a new material.
  - Add your own meshes to r.joints (head, chest, body, handL, handR, armL,
    armR, legL, legR, wingL, wingR, tail, cape): a mask, an emblem on the
    chest, a sword, a mane, antennae, a shell, a jetpack. They move with the
    joint. r.legs[i].sole is where each foot touches.
  Example, a hero: kit.rig({ plan: 'biped', build: 0.8, colors: { main:
  '#1d4fd8', second: '#d62828', skin: '#e9b98f' }, extras: ['cape'] }), a
  flat star on r.joints.chest at the front (+z, chest height), a dark band
  over the eyes on r.joints.head.
- Style follows the page: toon with outlines is the default. Flat with
  outline: false reads as low-poly, smooth as soft. Use the same style for
  every character on a board, and add your own meshes in that style.
- Variety: kit.look(seed, { palette, extras }) gives each visitor or entity
  its own colors and extras from your lists; same seed, same look on every
  screen. Pick the palette and extras to fit the theme. Several kinds of
  enemy: each its own plan, shapes, size and details, never one shape in
  different colors.
- Every frame, after moving: r.root.position is the feet, r.root.rotation.y
  the yaw (set it; never lookAt), then r.pose(dt). Others need nothing: pose
  reads how the root moved, so a remote player eased toward its rows walks,
  runs, jumps and lands by itself. It is on the ground when the root is at
  y 0; pass { floor } for another ground height, or { grounded } when you
  know (your own player on a roof or a platform). The head follows { aim:
  { yaw, pitch } }, relative to the body: yaw = camera yaw - root yaw.
- Materials are shared by every rig with the same color. To flash or recolor
  one character, give its meshes a clone, or show a child mesh.
- Moves: r.act(joint, seconds, fn) for a punch, a wave, a spin or a nod:
  r.act('armR', 0.3, k => [-2.2 * Math.sin(Math.PI * k), 0, 0]). Give each
  character moves that fit it. Game actions belong to the page; the rig only
  shows them.
- Readability: big heads and big hands and feet read best at game
  distance. The player stays the brightest thing on screen. Toon needs
  light: a HemisphereLight plus a DirectionalLight. Use emissive for small
  accents only.
- 2D or a drawn style (pixel art, paper, sprites): skip rig and draw the
  characters your way, with motion for the movement.
  kit.motion({ legs: [{ L: 16, phase: 0 }, { L: 16, phase: 0.5 }], arms:
  2, H: 48 }) gives the same gait with no three.js; m.update(dt, { speed:
  vx * facing, vy: -vy, grounded }) each frame. Hip at x, ground - (m.hip +
  m.bodyY); ctx.scale(facing, 1); per leg ctx.rotate(-leg.thigh), draw the
  thigh down L/2, translate there, ctx.rotate(leg.knee), draw the shin. Arms
  hang from shoulders you place: ctx.rotate(-arm.swing), the upper arm,
  translate, ctx.rotate(-arm.elbow), the forearm. Body squash with
  m.squash, tilt with m.lean, blink with m.blink.
- Many of one thing (a crowd, 200 birds): a rig each costs about 30 meshes.
  Past 40 characters, use simple shapes far away and rigs near the camera.
`
