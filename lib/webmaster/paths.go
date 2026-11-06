package webmaster

// pathsReference is the paths skill: A* and a flow field over a grid of open
// cells, path following and spreading apart. The model reads it with
// load_skill. The script block is run by paths_test.go, which checks the
// heap, that A* finds the shortest way and agrees with the flow field on a
// random maze, the closest cell when the goal is shut in, corner cutting,
// the node budget, walkers arriving without entering a wall, and spreading;
// change both together.
const pathsReference = `PATHS REFERENCE:
Use this when enemies, units, pets or anything else must find its way around
walls to a target, instead of walking straight into the nearest wall. This
core is tested. Copy it whole.

<script>
var paths = (function () {
  var D = Math.SQRT2
  // Eight ways to step from a cell, with the cost of each.
  var DIRS = [[1, 0, 1], [-1, 0, 1], [0, 1, 1], [0, -1, 1], [1, 1, D], [1, -1, D], [-1, 1, D], [-1, -1, D]]

  // heap is a priority queue: pop returns the id pushed with the smallest key.
  function heap() {
    var ids = [], keys = []
    return {
      size: function () { return ids.length },
      clear: function () { ids.length = 0; keys.length = 0 },
      push: function (id, key) {
        var i = ids.length
        ids.push(id); keys.push(key)
        while (i > 0) {
          var p = (i - 1) >> 1
          if (keys[p] <= key) break
          ids[i] = ids[p]; keys[i] = keys[p]; i = p
        }
        ids[i] = id; keys[i] = key
      },
      pop: function () {
        var top = ids[0], id = ids.pop(), key = keys.pop(), n = ids.length
        if (n) {
          var i = 0
          for (;;) {
            var c = 2 * i + 1
            if (c >= n) break
            if (c + 1 < n && keys[c + 1] < keys[c]) c++
            if (keys[c] >= key) break
            ids[i] = ids[c]; keys[i] = keys[c]; i = c
          }
          ids[i] = id; keys[i] = key
        }
        return top
      }
    }
  }

  // map is w by h cells, each size world units wide. open(cx, cy) says
  // whether a walker may stand in that cell; it is read live, so a broken
  // wall opens the way at once. Positions in and out are world units.
  function map(w, h, size, open) {
    var m = { w: w, h: h, size: size }
    var cost = new Float64Array(w * h), from = new Int32Array(w * h)
    var seen = new Uint32Array(w * h), shut = new Uint32Array(w * h), stamp = 0
    var q = heap()
    function cell(v) { return Math.floor(v / size) }
    function ok(x, y) { return x >= 0 && y >= 0 && x < w && y < h && !!open(x, y) }
    // A diagonal step needs both side cells open, so nothing cuts a corner.
    function can(x, y, d) {
      if (!ok(x + d[0], y + d[1])) return false
      return !(d[0] && d[1]) || (ok(x + d[0], y) && ok(x, y + d[1]))
    }
    function center(i) { return { x: (i % w + 0.5) * size, y: (Math.floor(i / w) + 0.5) * size } }
    function guess(x, y, tx, ty) {  // octile distance, never more than the real cost
      var dx = Math.abs(x - tx), dy = Math.abs(y - ty)
      return dx + dy + (D - 2) * Math.min(dx, dy)
    }

    // find returns the cell centers from the start to the goal, start
    // excluded, with path.reached true. When the goal is shut in or the
    // budget of cells runs out, it leads to the closest cell it found and
    // reached is false. null means the start is inside a wall.
    m.find = function (ax, ay, bx, by, budget) {
      var sx = cell(ax), sy = cell(ay), tx = cell(bx), ty = cell(by)
      if (!ok(sx, sy)) return null
      budget = budget || 3000
      stamp++
      q.clear()
      var start = sy * w + sx, goal = ty * w + tx, best = start, bestGuess = guess(sx, sy, tx, ty)
      cost[start] = 0; from[start] = -1; seen[start] = stamp
      q.push(start, bestGuess)
      for (var n = 0; q.size() && n < budget;) {
        var cur = q.pop()
        if (shut[cur] === stamp) continue
        shut[cur] = stamp; n++
        if (cur === goal) { best = cur; break }
        var x = cur % w, y = Math.floor(cur / w), left = guess(x, y, tx, ty)
        if (left < bestGuess) { best = cur; bestGuess = left }
        for (var k = 0; k < 8; k++) {
          var d = DIRS[k]
          if (!can(x, y, d)) continue
          var nb = cur + d[1] * w + d[0], c = cost[cur] + d[2]
          if (shut[nb] === stamp || (seen[nb] === stamp && c >= cost[nb])) continue
          seen[nb] = stamp; cost[nb] = c; from[nb] = cur
          q.push(nb, c + guess(nb % w, Math.floor(nb / w), tx, ty))
        }
      }
      var path = []
      for (var i = best; i !== start; i = from[i]) path.push(center(i))
      path.reverse()
      path.reached = best === goal
      return path
    }

    // flow measures every open cell's walking distance to the nearest of
    // some targets at once, so any number of walkers can ask it the way.
    m.flow = function () {
      var dist = new Float64Array(w * h), done = new Uint8Array(w * h), at = {}
      var f = {}
      // build takes the targets as points with x and y, and forgets the old ones.
      f.build = function (targets) {
        dist.fill(Infinity); done.fill(0); at = {}
        q.clear()
        for (var t = 0; t < targets.length; t++) {
          var tx = cell(targets[t].x), ty = cell(targets[t].y)
          if (!ok(tx, ty)) continue
          var i = ty * w + tx
          dist[i] = 0; at[i] = { x: targets[t].x, y: targets[t].y }
          q.push(i, 0)
        }
        while (q.size()) {
          var cur = q.pop()
          if (done[cur]) continue
          done[cur] = 1
          var x = cur % w, y = Math.floor(cur / w)
          for (var k = 0; k < 8; k++) {
            var d = DIRS[k]
            if (!can(x, y, d)) continue
            var nb = cur + d[1] * w + d[0], c = dist[cur] + d[2]
            if (c < dist[nb]) { dist[nb] = c; q.push(nb, c) }
          }
        }
      }
      // dist is how many cells of walking lie between a point and the nearest
      // target: 0 in a target's cell, Infinity when no target can be reached.
      f.dist = function (x, y) {
        var cx = cell(x), cy = cell(y)
        return cx >= 0 && cy >= 0 && cx < w && cy < h ? dist[cy * w + cx] : Infinity
      }
      // toward returns the point to head for next: the center of the
      // neighbor closest to a target, or the target itself in its own cell.
      // null when no target can be reached from here.
      f.toward = function (x, y) {
        var cx = cell(x), cy = cell(y), i = cy * w + cx, here = f.dist(x, y)
        if (!(here < Infinity)) return null
        if (here === 0) return at[i]
        var best = -1, low = here
        for (var k = 0; k < 8; k++) {
          var d = DIRS[k]
          if (!can(cx, cy, d)) continue
          var nb = i + d[1] * w + d[0]
          if (dist[nb] < low) { low = dist[nb]; best = nb }
        }
        return best < 0 ? null : center(best)
      }
      return f
    }
    return m
  }

  // follow returns the point to head for along a path from find, dropping
  // the points already within reach. It keeps the last point, so a walker
  // stops at the goal. null when the path is empty.
  function follow(path, x, y, reach) {
    while (path.length > 1 && Math.hypot(path[0].x - x, path[0].y - y) <= reach) path.shift()
    return path[0] || null
  }

  // apart returns a push away from every thing in list that is closer than
  // both radii plus gap, stronger the deeper the overlap. Add it to the
  // walker's direction so a crowd spreads out instead of stacking. e itself
  // may be in the list.
  function apart(e, list, gap) {
    var px = 0, py = 0
    for (var i = 0; i < list.length; i++) {
      var o = list[i]
      if (o === e) continue
      var room = (e.r || 0) + (o.r || 0) + (gap || 0)
      var dx = e.x - o.x, dy = e.y - o.y, d = Math.hypot(dx, dy)
      if (d >= room) continue
      var push = (room - d) / room
      if (!d) { var a = Math.random() * 2 * Math.PI; dx = Math.cos(a); dy = Math.sin(a); d = 1 }
      px += dx / d * push; py += dy / d * push
    }
    return { x: px, y: py }
  }

  return { heap: heap, map: map, follow: follow, apart: apart }
})()
</script>

THE RULE: one map of the same cells the walls use. A walker that heads for a
target in a straight line gets stuck on the first wall; a walker that asks the
map never does, and a broken wall changes the way at once.

- Build: paths.map(w, h, size, open). With the space skill in 2D:
  paths.map(g.w, g.h, g.size, function (x, y) { return !g.get(x, y) }).
  On a tile map, open reads the tile. A walker wider than a cell needs open
  to test every cell it covers, or it scrapes corners.
- Which one. A few seekers with their own goals (a pet, a guard, a unit you
  clicked a spot for): m.find. A crowd after the same targets (enemies after
  the players, a tower defense wave to the exit): one m.flow for all of them.
- find: path = m.find(e.x, e.y, goal.x, goal.y), then every step
  p = paths.follow(path, e.x, e.y, 0.3 * size) and steer at p. Find again
  every 0.5s, not every frame, spread over the walkers by their index, so no
  frame runs more than a few finds. If !path.reached the goal is shut in:
  the walker goes as close as it can.
- flow: f = m.flow(), then f.build(players) when a target enters a new cell
  or a wall breaks, at most every 0.25s. Each walker steers at
  f.toward(e.x, e.y). Many targets in one build means each walker goes to
  its nearest one. f.dist tells how far, for "close enough to attack".
- Steer, with the entities skill: in the brain, take p, set
  e.mx = (p.x - e.x) / d + s.x and e.my = (p.y - e.y) / d + s.y, where d is
  the distance to p and s = paths.apart(e, crowd, gap); e.face follows. Fill
  crowd from the space hash's near(), not every thing.
- When toward or find gives null, or the target is in sight (the space
  skill's g.sees), walk straight at the target instead.
- Placing walls a player builds (tower defense, maze building): build a flow
  from the goal with the new wall in, and refuse the wall if f.dist at the
  spawn became Infinity.
- 3D: x and y here are the ground, x and z in three.js. open tests the floor
  layer's cells: function (x, z) { return !g.get(x, FEET, z) && !g.get(x, FEET + 1, z) }.
  Flying things need no map: they steer straight.
- Size: a flow build visits every open cell once. Keep maps under 250 by 250
  cells, or path on a coarser map than the walls.
- Multiplayer: each page runs the paths for the enemies it owns. Paths are
  never rows; positions are.
- paths.heap() is a general priority queue, for anything else that needs the
  smallest next: events in time order, the closest of many.
`
