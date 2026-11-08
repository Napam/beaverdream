package skills

// randomReference is the random skill: a seeded generator, seeded noise and
// generators for mazes, dungeons, caves and scattered points. The model reads
// it with load_skill. The script block is run by random_test.go, which checks
// that the same seed gives the same world, the draws stay in range, forks are
// independent, every maze, dungeon and cave is one connected region, and
// scattered points keep their gap; change both together.
const randomReference = `RANDOM REFERENCE:
Use this when a page generates anything: a level, a maze, a dungeon, terrain,
a daily puzzle, loot, a deck, stars, art from a post's text. Everyone must see
the same thing, and the same thing again after a reload. This core is
tested. Reuse helpers already on the page, or copy the functions you need
with their dependencies intact.

<script>
var seeded = (function () {
  // hash turns any text or number into a 32-bit seed.
  function hash(text) {
    text = String(text)
    var h = 2166136261
    for (var i = 0; i < text.length; i++) { h ^= text.charCodeAt(i); h = Math.imul(h, 16777619) }
    h ^= h >>> 16; h = Math.imul(h, 2246822507); h ^= h >>> 13; h = Math.imul(h, 3266489909); h ^= h >>> 16
    return h >>> 0
  }

  // rng returns r: r() is a number in [0, 1), the same series for the same
  // seed in every browser. The seed is any text or number.
  function rng(seed) {
    var base = hash(seed), s = base
    function r() {
      s = (s + 0x6D2B79F5) | 0
      var t = Math.imul(s ^ (s >>> 15), 1 | s)
      t = (t + Math.imul(t ^ (t >>> 7), 61 | t)) ^ t
      return ((t ^ (t >>> 14)) >>> 0) / 4294967296
    }
    r.int = function (lo, hi) { return lo + Math.floor(r() * (hi - lo + 1)) }  // lo..hi, both included
    r.range = function (lo, hi) { return lo + r() * (hi - lo) }
    r.chance = function (p) { return r() < p }
    r.pick = function (list) { return list[Math.floor(r() * list.length)] }
    // weighted takes { name: weight } and returns a name, or undefined when
    // no weight is above 0.
    r.weighted = function (table) {
      var total = 0, k
      for (k in table) total += Math.max(0, table[k])
      if (!(total > 0)) return undefined
      var x = r() * total, last
      for (k in table) {
        if (!(table[k] > 0)) continue
        last = k; x -= table[k]
        if (x < 0) return k
      }
      return last
    }
    r.shuffle = function (list) {  // in place, and returns it
      for (var i = list.length - 1; i > 0; i--) {
        var j = Math.floor(r() * (i + 1)), t = list[i]
        list[i] = list[j]; list[j] = t
      }
      return list
    }
    // fork gives a separate generator for one purpose. It depends only on
    // this seed and the name, so new draws for the loot never move the walls.
    r.fork = function (name) { return rng(base + ':' + name) }
    return r
  }

  // day is today's date in UTC as YYYY-MM-DD, the same for every visitor.
  // offset in days: -1 is yesterday.
  function day(offset) { return new Date(Date.now() + (offset || 0) * 86400000).toISOString().slice(0, 10) }

  // noise returns n(x, y, octaves): smooth values in -1..1 that change
  // slowly with x and y. One octave is soft hills; more add finer detail.
  function noise(seed) {
    var r = rng(seed), p = new Uint8Array(512), i
    for (i = 0; i < 256; i++) p[i] = i
    r.shuffle(p.subarray(0, 256))
    for (i = 0; i < 256; i++) p[i + 256] = p[i]
    function grad(g, x, y) { return ((g & 1) ? -x : x) + ((g & 2) ? -y : y) }
    function fade(t) { return t * t * t * (t * (t * 6 - 15) + 10) }
    function lerp(a, b, t) { return a + (b - a) * t }
    function one(x, y) {
      var X = Math.floor(x), Y = Math.floor(y)
      x -= X; y -= Y; X &= 255; Y &= 255
      var u = fade(x), v = fade(y), a = p[X] + Y, b = p[X + 1] + Y
      return lerp(
        lerp(grad(p[a], x, y), grad(p[b], x - 1, y), u),
        lerp(grad(p[a + 1], x, y - 1), grad(p[b + 1], x - 1, y - 1), u), v)
    }
    return function (x, y, octaves) {
      var sum = 0, amp = 1, norm = 0
      for (var o = 0; o < (octaves || 1); o++) {
        sum += amp * one(x, y); norm += amp
        amp *= 0.5; x = x * 2 + 17.17; y = y * 2 + 31.31
      }
      return sum / norm
    }
  }

  // An area is w by h cells, cells[y * w + x] 1 for wall and 0 for open.
  // wall(x, y) is also true outside it.
  function area(w, h, cells) {
    return { w: w, h: h, cells: cells, wall: function (x, y) {
      return x < 0 || y < 0 || x >= w || y >= h || cells[y * w + x] === 1
    } }
  }
  var FOUR = [[1, 0], [-1, 0], [0, 1], [0, -1]]

  // maze carves w by h rooms into a (2w+1) by (2h+1) area. Every room is
  // reachable and, with loops 0, by exactly one way. loops (0..1) opens that
  // share of the other walls between rooms, so there are shortcuts.
  function maze(w, h, r, loops) {
    var W = 2 * w + 1, H = 2 * h + 1, cells = new Uint8Array(W * H).fill(1)
    var seen = new Uint8Array(w * h), stack = [[0, 0]]
    seen[0] = 1; cells[W + 1] = 0
    while (stack.length) {
      var c = stack[stack.length - 1], next = []
      for (var k = 0; k < 4; k++) {
        var nx = c[0] + FOUR[k][0], ny = c[1] + FOUR[k][1]
        if (nx >= 0 && ny >= 0 && nx < w && ny < h && !seen[ny * w + nx]) next.push([nx, ny])
      }
      if (!next.length) { stack.pop(); continue }
      var n = r.pick(next)
      seen[n[1] * w + n[0]] = 1
      cells[(2 * n[1] + 1) * W + 2 * n[0] + 1] = 0
      cells[(c[1] + n[1] + 1) * W + c[0] + n[0] + 1] = 0  // the wall between them
      stack.push(n)
    }
    if (loops) for (var y = 1; y < H - 1; y++) for (var x = 1; x < W - 1; x++)
      if ((x + y) % 2 && cells[y * W + x] && r.chance(loops)) cells[y * W + x] = 0
    return area(W, H, cells)
  }

  // rooms places up to count rooms, each min to max cells wide, apart from
  // each other, and joins each one to the closest earlier room by a
  // corridor, so every room is reachable. The area's rooms list has each
  // room's x, y, w, h and its center cx, cy.
  function rooms(w, h, r, count, min, max) {
    var cells = new Uint8Array(w * h).fill(1), list = []
    function dig(x, y) { cells[y * w + x] = 0 }
    function line(x0, y0, x1, y1) {  // a straight corridor, across or down
      for (var y = Math.min(y0, y1); y <= Math.max(y0, y1); y++)
        for (var x = Math.min(x0, x1); x <= Math.max(x0, x1); x++) dig(x, y)
    }
    for (var tries = 0; list.length < count && tries < count * 30; tries++) {
      var rw = r.int(min, max), rh = r.int(min, max)
      if (rw > w - 2 || rh > h - 2) continue
      var x = r.int(1, w - rw - 1), y = r.int(1, h - rh - 1), clear = true
      for (var i = 0; i < list.length && clear; i++) {
        var o = list[i]
        clear = x > o.x + o.w || o.x > x + rw || y > o.y + o.h || o.y > y + rh  // one wall between
      }
      if (!clear) continue
      list.push({ x: x, y: y, w: rw, h: rh, cx: x + (rw >> 1), cy: y + (rh >> 1) })
      for (var yy = y; yy < y + rh; yy++) for (var xx = x; xx < x + rw; xx++) dig(xx, yy)
    }
    for (var a = 1; a < list.length; a++) {
      var from = list[a], to = list[0]
      for (var b = 1; b < a; b++)
        if (Math.abs(list[b].cx - from.cx) + Math.abs(list[b].cy - from.cy) <
            Math.abs(to.cx - from.cx) + Math.abs(to.cy - from.cy)) to = list[b]
      var across = r.chance(0.5)  // turn the corner after going across, or after going down
      var bx = across ? to.cx : from.cx, by = across ? from.cy : to.cy
      line(from.cx, from.cy, bx, by); line(bx, by, to.cx, to.cy)
    }
    var out = area(w, h, cells)
    out.rooms = list
    return out
  }

  // cave grows open ground from a random fill: fill (0.4..0.5) is the share
  // that starts as wall, and steps smooth it. Only the largest open region is
  // kept, so every open cell is reachable. The edge is always wall.
  function cave(w, h, r, fill, steps) {
    fill = fill == null ? 0.45 : fill
    steps = steps == null ? 4 : steps
    var cells = new Uint8Array(w * h), x, y, i
    function edge(x, y) { return x === 0 || y === 0 || x === w - 1 || y === h - 1 }
    for (y = 0; y < h; y++) for (x = 0; x < w; x++) cells[y * w + x] = edge(x, y) || r.chance(fill) ? 1 : 0
    for (var s = 0; s < steps; s++) {
      var next = new Uint8Array(w * h)
      for (y = 0; y < h; y++) for (x = 0; x < w; x++) {
        var walls = 0
        for (var dy = -1; dy <= 1; dy++) for (var dx = -1; dx <= 1; dx++) {
          if (!dx && !dy) continue
          var ax = x + dx, ay = y + dy
          if (ax < 0 || ay < 0 || ax >= w || ay >= h || cells[ay * w + ax]) walls++
        }
        next[y * w + x] = edge(x, y) || walls > 4 ? 1 : walls < 4 ? 0 : cells[y * w + x]
      }
      cells = next
    }
    var label = new Int32Array(w * h), best = 0, bestSize = 0, id = 0
    for (i = 0; i < w * h; i++) {
      if (cells[i] || label[i]) continue
      var queue = [i], size = 0
      label[i] = ++id
      while (queue.length) {
        var c = queue.pop(), cx = c % w, cy = Math.floor(c / w)
        size++
        for (var k = 0; k < 4; k++) {
          var nx = cx + FOUR[k][0], ny = cy + FOUR[k][1], ni = ny * w + nx
          if (nx >= 0 && ny >= 0 && nx < w && ny < h && !cells[ni] && !label[ni]) { label[ni] = id; queue.push(ni) }
        }
      }
      if (size > bestSize) { bestSize = size; best = id }
    }
    for (i = 0; i < w * h; i++) if (!cells[i] && label[i] !== best) cells[i] = 1
    return area(w, h, cells)
  }

  // scatter places points over w by h with no two closer than gap and no
  // big empty holes: trees, rocks, stars, pickups. ok(x, y), if given,
  // refuses a spot, such as one inside a wall.
  function scatter(w, h, gap, r, ok) {
    var size = gap / Math.SQRT2, gw = Math.ceil(w / size), gh = Math.ceil(h / size)
    var grid = new Int32Array(gw * gh).fill(-1), points = [], active = [], gap2 = gap * gap
    function free(x, y) {
      if (x < 0 || y < 0 || x >= w || y >= h || (ok && !ok(x, y))) return false
      var gx = Math.floor(x / size), gy = Math.floor(y / size)
      for (var j = Math.max(0, gy - 2); j <= Math.min(gh - 1, gy + 2); j++)
        for (var i = Math.max(0, gx - 2); i <= Math.min(gw - 1, gx + 2); i++) {
          var n = grid[j * gw + i]
          if (n < 0) continue
          var dx = points[n].x - x, dy = points[n].y - y
          if (dx * dx + dy * dy < gap2) return false
        }
      return true
    }
    function add(x, y) {
      grid[Math.floor(y / size) * gw + Math.floor(x / size)] = points.length
      active.push(points.length)
      points.push({ x: x, y: y })
    }
    // Grow from a random free spot until nothing fits, then try new spots,
    // so places ok cuts apart get filled too. 30 misses in a row ends it.
    for (var misses = 0; misses < 30;) {
      var sx = r.range(0, w), sy = r.range(0, h)
      if (!free(sx, sy)) { misses++; continue }
      misses = 0
      add(sx, sy)
      while (active.length) {
        var k = r.int(0, active.length - 1), p = points[active[k]], placed = false
        for (var n = 0; n < 30 && !placed; n++) {
          var dx = r.range(-2, 2) * gap, dy = r.range(-2, 2) * gap, d2 = dx * dx + dy * dy
          if (d2 < gap2 || d2 > 4 * gap2) continue  // only the ring from gap to 2 gap
          if (free(p.x + dx, p.y + dy)) { add(p.x + dx, p.y + dy); placed = true }
        }
        if (!placed) { active[k] = active[active.length - 1]; active.pop() }
      }
    }
    return points
  }

  return { hash: hash, rng: rng, day: day, noise: noise, maze: maze, rooms: rooms, cave: cave, scatter: scatter }
})()
</script>

THE RULE: use a seeded generator for anything each page rebuilds independently
and must agree on, including after a reload. Math.random is fine for local
sparks and shake, or a sole simulator's choices when it publishes the results
and restores them from rows rather than generating them again.

- Seed. Everyone must start from the same seed, so take it from what every
  page sees: seeded.day() for a daily puzzle, the journal's match number for
  a new world each round, a post's id or text for art made from it, a value
  in shared state for a world the admin or a vote can reroll. Never seed from
  the time or Math.random.
- One fork per purpose: var r = seeded.rng('world:' + match), then
  r.fork('walls'), r.fork('loot'), r.fork('enemies'). Adding a draw to the
  loot then never moves the walls.
- Generate with plain math. Math.sin, Math.cos, Math.pow, Math.exp and
  Math.hypot may differ in the last digit between browsers, and one
  different digit can grow into a different world. The core uses only
  + - * / and floor; keep generation that way.
- Generate once at load, from the seed, before anything is drawn. Rows in
  the journal (a carve, a door opened) apply on top of that world. If the
  page changes how the world is generated, old rows describe a different
  world: bump the journal rules.
- Areas: maze, rooms and cave give an area with wall(x, y). Fill the space
  skill's grid from it: if (a.wall(x, y)) g.set(x, y, 0, 1). The paths skill
  takes function (x, y) { return !a.wall(x, y) }. Put the start in
  a.rooms[0] or any open cell, the exit in the farthest room or the cell
  with the largest paths flow dist from the start.
- Terrain: var n = seeded.noise(seed); height = n(x / 40, z / 40, 4). Divide
  by a scale: bigger is wider hills. Water below a level, rock above
  another. For a 2D cave game, wall where n(x / 12, y / 12, 3) > 0.1.
- Placing things: seeded.scatter(w, h, gap, r.fork('trees'), ok) for even
  spread without clumps, ok refusing walls and the spawn. r.weighted({
  common: 70, rare: 25, epic: 5 }) for what each spot holds.
- Decks and bags: r.shuffle(list) once per round, then deal in order. A
  "bag" of the seven pieces, shuffled and refilled when empty, is fairer
  than picking each one at random.
- Text: seeded.rng(post.body) gives every post its own stable colors,
  shapes or creature. The same text always gives the same result.
- Daily: seed with seeded.day(), show the date, and store results in the
  journal with the day in the payload, so scores compare.
- Size: generation runs on every visitor's page. Keep areas under 500 by
  500 cells and scatter under 5000 points.
`
