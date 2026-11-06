package skills

// spaceReference is the space skill: a grid world that rays, collision,
// damage and drawing all read, plus a spatial hash. The model reads it with
// load_skill. The script block is run by space_test.go, which checks the
// rays in 2D and 3D, the carve, line of sight, hit points, restore and the
// hash; change both together.
const spaceReference = `SPACE REFERENCE:
Use this for a world whose walls, buildings or ground can be hit, broken, dug,
seen through or collided with, and for many things that must find each other.
This core is tested. Copy it whole.

<script>
var space = (function () {
  // grid is a box of cells, w by h by d, each size world units wide. A cell
  // holds hit points: 0 is empty, 1 to 255 is solid. A 2D world has d = 1.
  // Collision, rays, damage and drawing all read these same cells.
  function grid(w, h, d, size) {
    var CHUNK = 8  // drawing rebuilds changed chunks of 8 by 8 by 8 cells
    var hp = new Uint8Array(w * h * d), base = hp.slice(), dirty = {}
    var g = { w: w, h: h, d: d, size: size }
    function inside(x, y, z) { return x >= 0 && y >= 0 && z >= 0 && x < w && y < h && z < d }
    function at(x, y, z) { return (z * h + y) * w + x }
    function mark(x, y, z) {
      dirty[Math.floor(x / CHUNK) + ',' + Math.floor(y / CHUNK) + ',' + Math.floor(z / CHUNK)] = true
    }
    function cell(v) { return Math.floor(v / size) }

    g.get = function (x, y, z) { return inside(x, y, z || 0) ? hp[at(x, y, z || 0)] : 0 }
    g.set = function (x, y, z, v) {
      z = z || 0
      v = Math.max(0, Math.min(255, v | 0))
      if (!inside(x, y, z) || hp[at(x, y, z)] === v) return
      hp[at(x, y, z)] = v
      mark(x, y, z)
    }
    // fill sets every cell from (x0, y0, z0) to (x1, y1, z1), inclusive.
    g.fill = function (x0, y0, z0, x1, y1, z1, v) {
      for (var z = z0; z <= z1; z++) for (var y = y0; y <= y1; y++) for (var x = x0; x <= x1; x++) g.set(x, y, z, v)
    }
    g.solidAt = function (px, py, pz) { return g.get(cell(px), cell(py), cell(pz || 0)) > 0 }
    // box is the min corner x, y, z and its size w, h, d in world units.
    g.boxHits = function (b) {
      var e = 0.0001, bz = b.z || 0
      for (var z = cell(bz); z <= Math.max(cell(bz), cell(bz + (b.d || 0) - e)); z++)
        for (var y = cell(b.y); y <= cell(b.y + b.h - e); y++)
          for (var x = cell(b.x); x <= cell(b.x + b.w - e); x++)
            if (g.get(x, y, z) > 0) return true
      return false
    }
    // ray walks the cells from (ox, oy, oz) along (dx, dy, dz) and returns the
    // first solid one within max: its cell, the distance t, the hit point and
    // the face normal. null means clear. Without a finite max it goes to the
    // far side of the grid.
    g.ray = function (ox, oy, oz, dx, dy, dz, max) {
      var len = Math.hypot(dx, dy, dz)
      if (!len) return null
      oz = oz || 0
      if (!(max < Infinity)) max = Math.hypot(w, h, d) * size
      dx /= len; dy /= len; dz /= len
      var x = cell(ox), y = cell(oy), z = cell(oz)
      var sx = dx > 0 ? 1 : -1, sy = dy > 0 ? 1 : -1, sz = dz > 0 ? 1 : -1
      var tx = dx ? ((x + (dx > 0)) * size - ox) / dx : Infinity, ix = dx ? size / Math.abs(dx) : Infinity
      var ty = dy ? ((y + (dy > 0)) * size - oy) / dy : Infinity, iy = dy ? size / Math.abs(dy) : Infinity
      var tz = dz ? ((z + (dz > 0)) * size - oz) / dz : Infinity, iz = dz ? size / Math.abs(dz) : Infinity
      var t = 0, n = [0, 0, 0]
      while (t <= max) {
        if (g.get(x, y, z) > 0) return { x: x, y: y, z: z, t: t, px: ox + dx * t, py: oy + dy * t, pz: oz + dz * t, n: n }
        if (tx < ty && tx < tz) { x += sx; t = tx; tx += ix; n = [-sx, 0, 0] }
        else if (ty < tz) { y += sy; t = ty; ty += iy; n = [0, -sy, 0] }
        else { z += sz; t = tz; tz += iz; n = [0, 0, -sz] }
      }
      return null
    }
    // sees is line of sight between two points.
    g.sees = function (a, b) {
      var dz = (b.z || 0) - (a.z || 0), dist = Math.hypot(b.x - a.x, b.y - a.y, dz)
      return !g.ray(a.x, a.y, a.z || 0, b.x - a.x, b.y - a.y, dz, dist - 0.0001)
    }
    // carve takes power hit points from every cell whose center is within r
    // of the point, and returns how many it emptied. Without power it empties.
    g.carve = function (px, py, pz, r, power) {
      var broke = 0, flat = d === 1
      for (var z = cell((pz || 0) - r); z <= cell((pz || 0) + r); z++)
        for (var y = cell(py - r); y <= cell(py + r); y++)
          for (var x = cell(px - r); x <= cell(px + r); x++) {
            var v = g.get(x, y, z)
            if (!v) continue
            var cz = flat ? 0 : (z + 0.5) * size - (pz || 0)
            if (Math.hypot((x + 0.5) * size - px, (y + 0.5) * size - py, cz) > r) continue
            var left = power == null ? 0 : Math.max(0, v - Math.max(0, power))
            g.set(x, y, z, left)
            if (!left) broke++
          }
      return broke
    }
    // save keeps the current cells as the world to restore, for a new round.
    g.save = function () { base = hp.slice() }
    g.restore = function () {
      for (var i = 0; i < hp.length; i++) if (hp[i] !== base[i]) {
        hp[i] = base[i]
        mark(i % w, Math.floor(i / w) % h, Math.floor(i / (w * h)))
      }
    }
    // takeDirty returns the chunks changed since the last call, as
    // "cx,cy,cz" keys, so drawing rebuilds only those.
    g.takeDirty = function () { var k = Object.keys(dirty); dirty = {}; return k }
    return g
  }

  // hash finds things near a point without testing every pair. Clear and
  // add everything once a step, then ask near(). Things have x, y, z and r.
  function hash(size) {
    var buckets = new Map()
    function key(x, y, z) { return x + ',' + y + ',' + z }
    var h = {}
    h.clear = function () { buckets.clear() }
    h.add = function (e) {
      var k = key(Math.floor(e.x / size), Math.floor(e.y / size), Math.floor((e.z || 0) / size))
      if (!buckets.has(k)) buckets.set(k, [])
      buckets.get(k).push(e)
    }
    // near calls fn for every thing whose circle or sphere touches the one at
    // (x, y, z) with radius r, the asker included. It looks one bucket around,
    // so size must be at least r plus the largest e.r.
    h.near = function (x, y, z, r, fn) {
      var cx = Math.floor(x / size), cy = Math.floor(y / size), cz = Math.floor((z || 0) / size)
      for (var k = cz - 1; k <= cz + 1; k++)
        for (var j = cy - 1; j <= cy + 1; j++)
          for (var i = cx - 1; i <= cx + 1; i++) {
            var list = buckets.get(key(i, j, k))
            if (!list) continue
            for (var n = 0; n < list.length; n++) {
              var e = list[n]
              if (Math.hypot(e.x - x, e.y - y, (e.z || 0) - (z || 0)) <= r + (e.r || 0)) fn(e)
            }
          }
    }
    return h
  }
  return { grid: grid, hash: hash }
})()
</script>

THE RULE: the grid is the world. Collision, rays, damage and drawing all read
the same cells, so a hole you see is a hole you can walk and shoot through.
Never fake damage by changing a shared material or by hiding a mesh: that
changes every cell or none.

- Build: space.grid(w, h, d, size), then fill boxes. 2D: d = 1, z = 0, size
  in px. 3D: size 1 meter, y up. A cell's hp is its toughness: 1 breaks on
  any hit, 255 almost never. Call g.save() once it is built; g.restore() is a
  new round or a new day.
- Bodies: test g.boxHits on each axis before you move along it, like the
  controls core. A body stands when the box one unit lower hits.
- Anything fast: never test only where it ends. Move it by g.ray from where
  it was along its velocity, max the distance of this step, and stop at
  hit.t. hit.n is the face it struck, for bounces and sparks.
- Hitscan, lasers, snipers: one g.ray to max range per shot, or every 0.05s
  while held. Draw the beam to the hit point.
- Line of sight: g.sees(a, b) before an enemy shoots or notices you, so
  nothing shoots through walls.
- Damage: g.carve(x, y, z, r, power) where it lands. A dash or a crash carves
  along its path every half cell. Without power the cells go at once; with it
  tough cells take several hits. It returns how many broke: scale the effect
  by that.
- Draw. Canvas: the canvas skill's cached layer, or fillRect each solid
  cell in view for a small grid. 3D: one InstancedMesh per chunk of 8 by 8
  by 8 cells, keyed by g.takeDirty(), rebuilt only when dirty. Skip cells
  with all six neighbors solid. Shade by depth or hp so a hole's inside
  shows.
- Many things: rebuild a space.hash(size) every step, with size at least
  twice the largest r, then ask near() for hits, pickups and crowding instead
  of every pair. near() also returns the asker: skip it.
- Size: keep the grid under 2 million cells. Make cells bigger before you
  make the world bigger.
- Multiplayer: a carve is a row {x, y, z, r, power}. Every page applies the
  rows in order to the saved world, so everyone sees the same holes. A new
  match restores.
`
