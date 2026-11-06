package webmaster

// canvasReference is the canvas skill: sharp 2D drawing with a camera, a
// cached tile layer, autotiling, sprites from text, parallax, y sorting and
// a light layer. The model reads it with load_skill. The script block is
// run by canvas_test.go, which checks the pixel ratio, that the camera's
// drawing transform and toWorld agree, the visible range, the chunk cache
// and its eviction, autotile masks, sprite pixels, parallax copies and the
// light layer; change both together.
const canvasReference = `CANVAS REFERENCE:
Use this for a game, a world or a scene drawn on a 2D canvas: platformers,
top-down games, roguelikes, sims, pixel art. The other skills decide where
things are; this one draws them sharp and cheap. This core is tested. Copy
it whole.

<script>
var draw2d = (function () {
  function surface(w, h) {
    var s = typeof OffscreenCanvas !== 'undefined' ? new OffscreenCanvas(w, h) : document.createElement('canvas')
    s.width = w; s.height = h
    return s
  }

  // view fits a canvas to its CSS box times the screen's pixel ratio, now
  // and on every resize, so nothing is blurry. With pixel, pixel art stays
  // crisp. Draw in CSS px: call v.begin() first every frame.
  function view(el, pixel) {
    var ctx = el.getContext('2d'), v = { el: el, ctx: ctx, w: 0, h: 0, ratio: 1 }
    if (pixel) el.style.imageRendering = 'pixelated'
    v.fit = function () {
      var box = el.getBoundingClientRect()
      v.ratio = (typeof devicePixelRatio === 'number' && devicePixelRatio) || 1
      v.w = box.width; v.h = box.height
      var bw = Math.round(v.w * v.ratio), bh = Math.round(v.h * v.ratio)
      if (el.width !== bw || el.height !== bh) { el.width = bw; el.height = bh }
    }
    v.begin = function () {
      ctx.setTransform(v.ratio, 0, 0, v.ratio, 0, 0)
      ctx.imageSmoothingEnabled = !pixel  // a resize resets it
      ctx.clearRect(0, 0, v.w, v.h)
    }
    v.fit()
    addEventListener('resize', v.fit)
    return v
  }

  // camera looks at the world point x, y, which sits at the screen's
  // center. zoom is CSS px per world unit. shakeX, shakeY take the feel
  // skill's shake in px. With snap, the world lands on whole device pixels.
  function camera(v, snap) {
    var c = { x: 0, y: 0, zoom: 1, shakeX: 0, shakeY: 0, snap: !!snap }
    function offset() {
      var tx = v.w / 2 + c.shakeX - c.x * c.zoom, ty = v.h / 2 + c.shakeY - c.y * c.zoom
      if (c.snap) { tx = Math.round(tx * v.ratio) / v.ratio; ty = Math.round(ty * v.ratio) / v.ratio }
      return [tx, ty]
    }
    // Draw the world between begin and end, the HUD after end.
    c.begin = function () {
      var o = offset()
      v.ctx.save(); v.ctx.translate(o[0], o[1]); v.ctx.scale(c.zoom, c.zoom)
    }
    c.end = function () { v.ctx.restore() }
    // toWorld turns a mouse position on the canvas (e.offsetX, e.offsetY)
    // into the world point under it; toScreen goes back.
    c.toWorld = function (sx, sy) { var o = offset(); return { x: (sx - o[0]) / c.zoom, y: (sy - o[1]) / c.zoom } }
    c.toScreen = function (x, y) { var o = offset(); return { x: x * c.zoom + o[0], y: y * c.zoom + o[1] } }
    // visible is the range of cells of size world units on screen, both
    // ends included. Loop over it instead of the whole map.
    c.visible = function (size) {
      var a = c.toWorld(0, 0), b = c.toWorld(v.w, v.h)
      return { x0: Math.floor(a.x / size), y0: Math.floor(a.y / size), x1: Math.floor(b.x / size), y1: Math.floor(b.y / size) }
    }
    return c
  }

  // layer caches a w by h tile map in chunks of 8 by 8 tiles, each tile
  // size world units, drawn at res cache px per world unit. drawTile(ctx, x, y)
  // draws one tile at x * size, y * size, the same code as without a cache.
  // Chunks are drawn when first seen and again after mark; far ones are
  // dropped past MAX, so a big map costs only what is near.
  function layer(w, h, size, drawTile, res) {
    var N = 8, MAX = 96
    res = res || 2
    var cw = Math.ceil(w / N), ch = Math.ceil(h / N), px = Math.ceil(N * size * res)
    var chunks = [], used = [], dirty = new Uint8Array(cw * ch).fill(1), built = [], frame = 0
    var L = { builds: 0 }
    function build(i) {
      var cx = i % cw, cy = Math.floor(i / cw)
      if (!chunks[i]) { chunks[i] = surface(px, px); built.push(i) }
      var g = chunks[i].getContext('2d')
      g.setTransform(1, 0, 0, 1, 0, 0)
      g.clearRect(0, 0, px, px)
      g.setTransform(res, 0, 0, res, -cx * N * size * res, -cy * N * size * res)
      for (var y = cy * N; y < Math.min(h, cy * N + N); y++)
        for (var x = cx * N; x < Math.min(w, cx * N + N); x++) drawTile(g, x, y)
      dirty[i] = 0
      L.builds++
    }
    // mark redraws the tiles from x0, y0 to x1, y1 and their neighbors,
    // whose edges may change with them.
    L.mark = function (x0, y0, x1, y1) {
      if (x1 == null) { x1 = x0; y1 = y0 }
      for (var cy = Math.max(0, Math.floor((y0 - 1) / N)); cy <= Math.min(ch - 1, Math.floor((y1 + 1) / N)); cy++)
        for (var cx = Math.max(0, Math.floor((x0 - 1) / N)); cx <= Math.min(cw - 1, Math.floor((x1 + 1) / N)); cx++)
          dirty[cy * cw + cx] = 1
    }
    // draw goes between cam.begin() and cam.end().
    L.draw = function (ctx, cam) {
      frame++
      var r = cam.visible(N * size)
      for (var cy = Math.max(0, r.y0); cy <= Math.min(ch - 1, r.y1); cy++)
        for (var cx = Math.max(0, r.x0); cx <= Math.min(cw - 1, r.x1); cx++) {
          var i = cy * cw + cx
          if (!chunks[i] || dirty[i]) build(i)
          used[i] = frame
          ctx.drawImage(chunks[i], cx * N * size, cy * N * size, N * size, N * size)
        }
      if (built.length > MAX) {
        built.sort(function (a, b) { return used[a] - used[b] })
        while (built.length > MAX && used[built[0]] !== frame) chunks[built.shift()] = null
      }
    }
    L.cached = function () { return built.length }
    return L
  }

  // autotile tells which sides of tile x, y join a tile of the same kind:
  // 1 up, 2 right, 4 down, 8 left, added. same(x, y) is true for the kind.
  // A side not in the mask is an edge: draw the rim, the grass, the outline.
  function autotile(same, x, y) {
    return (same(x, y - 1) ? 1 : 0) | (same(x + 1, y) ? 2 : 0) | (same(x, y + 1) ? 4 : 0) | (same(x - 1, y) ? 8 : 0)
  }

  // sprite draws rows of text once into an image: each character is one
  // pixel of palette[character], and a character not in the palette is
  // clear. scale is a whole number. mirror faces it the other way.
  function sprite(rows, palette, scale, mirror) {
    scale = scale || 1
    var w = 0
    for (var r = 0; r < rows.length; r++) w = Math.max(w, rows[r].length)
    var s = surface(w * scale, rows.length * scale), g = s.getContext('2d')
    for (var y = 0; y < rows.length; y++)
      for (var x = 0; x < rows[y].length; x++) {
        var color = palette[rows[y][x]]
        if (!color) continue
        g.fillStyle = color
        g.fillRect((mirror ? w - 1 - x : x) * scale, y * scale, scale, scale)
      }
    return s
  }
  // solid is a palette with every color replaced, for a white hit flash
  // or a shadow from the same rows.
  function solid(palette, color) {
    var out = {}
    for (var k in palette) out[k] = color
    return out
  }

  // parallax draws layers behind the world, before cam.begin(). Each layer
  // is { depth, width, draw(ctx, x, y) }: depth 0 stays put, 1 moves with
  // the world. draw paints one copy with its left edge at screen x, shifted
  // down by y; copies repeat every width px to fill the screen.
  function parallax(v, cam, layers) {
    for (var i = 0; i < layers.length; i++) {
      var l = layers[i], W = l.width
      var shift = -cam.x * cam.zoom * l.depth, y = -cam.y * cam.zoom * l.depth
      for (var x = ((shift % W) + W) % W - W; x < v.w; x += W) l.draw(v.ctx, x, y)
    }
  }

  // ysort orders things so the lower on screen is drawn later, in front.
  // Keep each thing's y at its feet.
  function ysort(list) { return list.sort(function (a, b) { return a.y - b.y }) }

  // light is a darkness layer at half resolution. Each frame: begin(dark),
  // with dark 0 for full day and about 0.85 for night; glow(x, y, r) in
  // screen px for every lamp, torch or blast; then draw() after the world.
  function light(v, color) {
    var s = surface(1, 1), g = s.getContext('2d'), dark = 0, HALF = 0.5
    var soft = surface(64, 64), sg = soft.getContext('2d')
    var grad = sg.createRadialGradient(32, 32, 0, 32, 32, 32)
    grad.addColorStop(0, 'rgba(0,0,0,1)'); grad.addColorStop(1, 'rgba(0,0,0,0)')
    sg.fillStyle = grad; sg.fillRect(0, 0, 64, 64)
    var L = {}
    L.begin = function (amount) {
      dark = amount
      var w = Math.max(1, Math.ceil(v.w * HALF)), h = Math.max(1, Math.ceil(v.h * HALF))
      if (s.width !== w || s.height !== h) { s.width = w; s.height = h }
      g.setTransform(1, 0, 0, 1, 0, 0)
      g.globalCompositeOperation = 'source-over'
      g.clearRect(0, 0, w, h)
      g.globalAlpha = dark; g.fillStyle = color || '#000'; g.fillRect(0, 0, w, h)
      g.globalCompositeOperation = 'destination-out'
    }
    L.glow = function (x, y, r, strength) {
      if (dark <= 0) return
      g.globalAlpha = strength == null ? 1 : strength
      g.drawImage(soft, (x - r) * HALF, (y - r) * HALF, 2 * r * HALF, 2 * r * HALF)
    }
    L.draw = function () {
      if (dark <= 0) return
      v.ctx.drawImage(s, 0, 0, v.w, v.h)
    }
    return L
  }

  return {
    view: view, camera: camera, layer: layer, autotile: autotile, sprite: sprite, solid: solid,
    parallax: parallax, ysort: ysort, light: light
  }
})()
</script>

THE RULE: the world is drawn in world units between cam.begin() and
cam.end(); the HUD is drawn in screen px after it. Mixing the two is most 2D
bugs: aim that misses, a HUD that scrolls away, sprites that drift on zoom.

- Frame: v.begin(), the sky, draw2d.parallax, cam.begin(), layer.draw,
  things in ysort order, effects, cam.end(), the light, the HUD.
- Setup: var v = draw2d.view(canvasEl, true) and var cam = draw2d.camera(v,
  true) for pixel art; pass false to both for smooth shapes. Give the canvas
  its size in CSS (width:100%; height:100%), never with width="" alone.
- Camera: move cam.x, cam.y to the controls skill's camera goal, eased and
  clamped there. cam.zoom whole numbers for pixel art. Put the feel skill's
  fx.shake.x, y into cam.shakeX, shakeY.
- Mouse: cam.toWorld(e.offsetX, e.offsetY), then Math.floor(p.x / size) is
  the tile under the cursor. Lamps on screen: cam.toScreen(x, y), radius
  r * cam.zoom.
- Tiles: one layer per map. drawTile reads the map and draws with
  draw2d.autotile(same, x, y): a solid block with a lighter top strip where
  mask & 1 is 0, a dark rim on the other open sides. With the space skill,
  after a carve: g.takeDirty().forEach(function (k) { var p = k.split(',');
  layer.mark(p[0] * 8, p[1] * 8, p[0] * 8 + 7, p[1] * 8 + 7) }).
- Without a layer (a small map), loop only over cam.visible(size).
- Sprites: rows of text and a palette, made once at load, never per frame.
  Make the mirrored copy too, and draw by facing. Animation is a list of
  sprites switched by time. A hit flash is the same rows with
  draw2d.solid(palette, '#fff'), shown 0.08s.
- Palette: pick 6 to 12 colors once and use only those. The player is the
  brightest thing, enemies get their own hue, the background is the dullest.
  One dark outline color for everything.
- Pixel art: art at 1 world unit per pixel, cam.zoom 2 to 4, res of the
  layer equal to cam.zoom times v.ratio.
- Depth: top-down worlds ysort everything that stands. Side views get two
  or three parallax layers: far hills at depth 0.2, near ones at 0.5.
- Light: night, caves and torches. A day cycle sets dark from 0 to 0.85.
  Glows are cheap; use one for every shot and blast too.
- Never create canvases, gradients, sprites or big arrays inside the frame.
`
