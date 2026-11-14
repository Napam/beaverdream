package skills

// audioReference is the audio skill: a player for songs the model writes as
// data, and the fundamentals to write them with. The model reads it with
// load_skill. The script block is run by audio_test.go, which checks the
// pattern notation (degrees, chord tones, close chords, holds, accidentals,
// accents), meters and forms, that every table entry is a valid part, that a
// broken song still plays and reports each problem once, that the scheduler
// starts every step once and on the beat, swings, never schedules into the
// past after a hidden tab and survives a bad bpm, that a new song or a cue waits for the bar
// line, that playing the same song again changes nothing, that nothing sounds
// before a gesture or when muted, and that mood picks the tracks; change both
// together.
const audioReference = `AUDIO REFERENCE:
Use this when a page wants music. One-shot sounds (a coin, a hit, an
explosion) belong to the feel skill and its zzfx synth. The core below is a
tested player: you write the song as data, and it keeps time, waits for a
gesture, never stacks two loops and mixes under a limiter. Reuse the one
already on the page, or copy it whole. Its tables (SCALES, PROGRESSIONS,
GROOVES, SOUNDS) are vocabulary to mix, change and add to. They are not
songs: write the parts yourself.

<script>
var music = (function () {
  var AHEAD = 0.4, TICK = 25  // seconds scheduled ahead, ms between checks

  // Semitones above the key. The seven-note scales make real chords. The
  // pentatonic ones have no two notes that clash, and their chords are open.
  var SCALES = {
    major: [0, 2, 4, 5, 7, 9, 11],       // bright, home, cheerful
    minor: [0, 2, 3, 5, 7, 8, 10],       // sad, serious, epic
    dorian: [0, 2, 3, 5, 7, 9, 10],      // minor with hope: quests, roads, grooves
    mixolydian: [0, 2, 4, 5, 7, 9, 10],  // major with grit: rock, adventure, folk
    lydian: [0, 2, 4, 6, 7, 9, 11],      // floating: wonder, flight, dreams
    phrygian: [0, 1, 3, 5, 7, 8, 10],    // dark: menace, deserts, the underworld
    harmonic: [0, 2, 3, 5, 7, 8, 11],    // harmonic minor: drama, villains, a pull home
    majorPent: [0, 2, 4, 7, 9],          // folk, arcade, children's tunes
    minorPent: [0, 3, 5, 7, 10],         // rock and blues leads
    blues: [0, 3, 5, 6, 7, 10]
  }
  // Chord degrees, one a bar. 0 is home. The same degrees sound different in
  // another scale: bright and nostalgic share the major scale, sad is the
  // nostalgic progression in minor.
  var PROGRESSIONS = {
    bright: { scale: 'major', chords: [0, 4, 5, 3] },      // I V vi IV
    nostalgic: { scale: 'major', chords: [0, 5, 3, 4] },   // I vi IV V
    calm: { scale: 'major', chords: [0, 3] },              // I IV, a slow sway
    sad: { scale: 'minor', chords: [0, 5, 3, 4] },         // i VI iv v
    epic: { scale: 'minor', chords: [0, 5, 2, 6] },        // i VI III VII
    heroic: { scale: 'minor', chords: [0, 6, 5, 6] },      // i VII VI VII, a climb
    drama: { scale: 'harmonic', chords: [0, 3, 4, 0] },    // i iv V i, a cadence
    groove: { scale: 'dorian', chords: [0, 3] },           // i IV vamp
    rock: { scale: 'mixolydian', chords: [0, 6, 3, 0] },   // I bVII IV I
    wonder: { scale: 'lydian', chords: [0, 1] },           // I II, never lands
    menace: { scale: 'phrygian', chords: [0, 1] }          // i bII, a creeping half step
  }
  // Drum parts for one bar, with the meter they are written in: beat is
  // steps per beat and bar is beats per bar.
  var GROOVES = {
    rock: { beat: 2, bar: 4, kick: 'x . . . x x . .', snare: '. . x . . . x .', hat: 'x x x x x x x x' },
    four: { beat: 4, bar: 4, kick: 'x . . . x . . . x . . . x . . .', snare: '. . . . x . . . . . . . x . . .', hat: '. . x . . . x . . . x . . . x .' },
    halftime: { beat: 4, bar: 4, kick: 'x . . . . . . . . . x . . . . .', snare: '. . . . . . . . x! . . . . . . .', hat: 'x . x~ . x . x~ . x . x~ . x . x~ .' },
    breakbeat: { beat: 4, bar: 4, kick: 'x . x . . . . . . . x x . . . .', snare: '. . . . x! . . x~ . x~ . . x! . . x~', hat: 'x . x . x . x . x . x . x . x .' },
    march: { beat: 4, bar: 4, kick: 'x . . . . . . . x . . . . . . .', snare: 'x! . x~ x~ x . x~ . x! . x~ x~ x . x x', hat: '' },
    waltz: { beat: 2, bar: 3, kick: 'x . . . . .', snare: '. . x~ . x~ .', hat: 'x . x . x .' },
    shuffle: { beat: 3, bar: 4, kick: 'x . . . . . x . . . . .', snare: '. . . x! . . . . . x! . .', hat: 'x . x x . x x . x x . x' },
    bossa: { beat: 4, bar: 4, kick: 'x . . x x . . x x . . x x . . x', snare: 'x~ . . x~ . . x~ . . . x~ . . x~ . .', hat: 'x~ x~ x x~ x~ x~ x x~ x~ x~ x x~ x~ x~ x x~' }
  }
  // An instrument is a row: wave (sine, triangle, sawtooth, square), cutoff
  // (a low-pass in Hz, lower is softer), attack, decay and release in s,
  // sustain 0..1 of the peak, gain, detune (cents, a second voice for width),
  // hz and to (a fixed pitch that sweeps, for drums), noise (a high-pass in
  // Hz over hiss instead of a wave).
  var SOUNDS = {
    sub: { wave: 'sine', gain: 0.45, attack: 0.01, decay: 0.3, sustain: 0.8, release: 0.08 },
    bass: { wave: 'triangle', cutoff: 700, gain: 0.4, attack: 0.01, decay: 0.2, sustain: 0.6, release: 0.08 },
    pluck: { wave: 'sawtooth', cutoff: 1400, gain: 0.14, attack: 0.005, decay: 0.25, sustain: 0, release: 0.1 },
    keys: { wave: 'triangle', cutoff: 2200, gain: 0.16, attack: 0.01, decay: 0.6, sustain: 0.3, release: 0.3 },
    pad: { wave: 'sawtooth', cutoff: 900, gain: 0.05, attack: 0.4, decay: 1, sustain: 0.8, release: 1, detune: 8 },
    lead: { wave: 'square', cutoff: 1800, gain: 0.07, attack: 0.02, decay: 0.2, sustain: 0.7, release: 0.15 },
    flute: { wave: 'sine', gain: 0.18, attack: 0.08, decay: 0.3, sustain: 0.8, release: 0.2 },
    bell: { wave: 'sine', gain: 0.12, attack: 0.002, decay: 1.2, sustain: 0, release: 0.5, detune: 1200 },
    kick: { wave: 'sine', hz: 150, to: 42, gain: 0.7, attack: 0.002, decay: 0.12, sustain: 0, release: 0.05 },
    tom: { wave: 'sine', hz: 190, to: 90, gain: 0.4, attack: 0.002, decay: 0.15, sustain: 0, release: 0.05 },
    snare: { noise: 1500, gain: 0.3, attack: 0.001, decay: 0.08, sustain: 0, release: 0.05 },
    hat: { noise: 7000, gain: 0.08, attack: 0.001, decay: 0.03, sustain: 0, release: 0.02 }
  }
  var LETTERS = { C: 0, D: 2, E: 4, F: 5, G: 7, A: 9, B: 11 }
  var HOLD = {}, MAX_NOTES = 6

  function mod(a, n) { return ((a % n) + n) % n }
  function hz(midi) { return 440 * Math.pow(2, (midi - 69) / 12) }
  function whole(v, lo, hi, fallback) { v = Math.round(+v); return v >= lo && v <= hi ? v : fallback }
  // pitch is degree d of the scale above base, any integer, wrapping octaves.
  function pitch(base, scale, d) { return base + 12 * Math.floor(d / scale.length) + scale[mod(d, scale.length)] }
  function keyOf(k) {
    if (k == null) return 0
    if (typeof k === 'number') return isFinite(k) ? mod(Math.round(k), 12) : null
    var m = /^([A-Ga-g])([#b]?)$/.exec(String(k))
    return m ? mod(LETTERS[m[1].toUpperCase()] + (m[2] === '#' ? 1 : m[2] === 'b' ? -1 : 0), 12) : null
  }

  // parse reads one pattern into a slot per step: null is a rest, HOLD holds.
  function parse(text, where, problems) {
    var slots = []
    String(text).replace(/\|/g, ' ').split(/\s+/).forEach(function (tok) {
      if (!tok) return
      if (tok === '.') return slots.push(null)
      if (tok === '-') return slots.push(HOLD)
      var vel = 0.7, end = tok.charAt(tok.length - 1)
      if (end === '!' || end === '~') { vel = end === '!' ? 1 : 0.35; tok = tok.slice(0, -1) }
      var notes = []
      tok.split('+').forEach(function (part) {
        var m = /^(?:(x)|C(7?)|c(-?\d+)|(-?\d+))([#b]*)$/.exec(part)
        if (!m) return problems.push(where + ': unknown note ' + part)
        var shift = 0
        for (var i = 0; i < m[5].length; i++) shift += m[5].charAt(i) === '#' ? 1 : -1
        if (m[2] != null) for (var k = 0; k < (m[2] ? 4 : 3); k++) notes.push({ stack: k, shift: shift })
        else if (m[1]) notes.push({ tone: 0, shift: shift })
        else if (m[3] != null) notes.push({ tone: +m[3], shift: shift })
        else notes.push({ degree: +m[4], shift: shift })
      })
      if (notes.length > MAX_NOTES) { problems.push(where + ': more than ' + MAX_NOTES + ' notes at once'); notes.length = MAX_NOTES }
      slots.push(notes.length ? { notes: notes, vel: vel } : null)
    })
    return slots
  }

  // compile checks a song and lays its form out in steps. A broken part
  // becomes silence or a fallback and a line in problems; it never throws.
  function compile(song) {
    var s = song || {}, problems = []
    var scale = Array.isArray(s.scale) ? s.scale : SCALES[s.scale == null ? 'major' : s.scale]
    if (!scale || !scale.length) { problems.push('unknown scale ' + s.scale); scale = SCALES.major }
    var key = keyOf(s.key)
    if (key == null) { problems.push('unknown key ' + s.key); key = 0 }
    var beat = whole(s.beat, 1, 8, 4), bar = beat * whole(s.bar, 1, 16, 4)
    var tracks = [], byName = {}, name
    for (name in s.tracks || {}) {
      var t = s.tracks[name] || {}, label = t.sound == null ? name : t.sound
      var spec = typeof label === 'object' ? label : SOUNDS[label]
      if (!spec) problems.push('track ' + name + ': unknown sound ' + label)
      byName[name] = { name: name, spec: spec || SOUNDS.keys, base: 12 * (whole(t.octave, 0, 8, 4) + 1) + key,
        mood: +t.mood || 0, gain: t.gain == null ? 1 : Math.max(0, +t.gain || 0) }
      tracks.push(byName[name])
    }
    var sections = s.sections || {}
    var form = s.form == null ? Object.keys(sections) : typeof s.form === 'string' ? s.form.split('') : s.form
    var parts = [], length = 0
    for (var i = 0; i < form.length; i++) {
      var sec = sections[form[i]], where = 'section ' + form[i]
      if (!sec) { problems.push('form: no section ' + form[i]); continue }
      var chords = (sec.chords && sec.chords.length ? sec.chords : [0]).map(function (c) {
        if (c === Math.round(c)) return c
        problems.push(where + ': chord ' + c + ' is not a scale degree')
        return 0
      })
      var part = { name: form[i], start: length, chords: chords, patterns: {} }, longest = 0
      for (name in sec) {
        if (name === 'chords' || name === 'bars') continue
        if (!byName[name]) { problems.push(where + ': no track ' + name); continue }
        var slots = parse(sec[name], where + ' ' + name, problems)
        if (slots.length) { part.patterns[name] = slots; longest = Math.max(longest, slots.length) }
      }
      part.length = bar * whole(sec.bars, 1, 256, Math.max(chords.length, Math.ceil(longest / bar)))
      length += part.length
      parts.push(part)
    }
    if (!length) problems.push('no sections to play')
    problems = problems.filter(function (p, i) { return problems.indexOf(p) === i })
    return { problems: problems, scale: scale, beat: beat, bar: bar, tracks: tracks, byName: byName, parts: parts, length: length }
  }

  var cached = null, cache = null
  function compiled(song) {
    if (song !== cached) { cache = compile(song); cached = song }
    return cache
  }
  // note is the midi number of one parsed note: a degree, a chord tone (root,
  // third and fifth, repeating by octaves) or a stacked chord note folded
  // into the octave around the track's base, so chords move by small steps.
  function note(scale, base, chord, n) {
    var m
    if (n.degree != null) m = pitch(base, scale, n.degree)
    else if (n.tone != null) m = pitch(base, scale, chord + 2 * mod(n.tone, 3)) + 12 * Math.floor(n.tone / 3)
    else m = base - 5 + mod(pitch(base, scale, chord + 2 * n.stack) - base + 5, 12)
    return m + n.shift
  }
  // events lists what starts on one step of a song. Pure: no audio.
  function events(song, step) {
    var c = compiled(song), out = []
    if (!c.length) return out
    var s = mod(step, c.length), part = c.parts[0]
    for (var i = 1; i < c.parts.length; i++) if (s >= c.parts[i].start) part = c.parts[i]
    var local = s - part.start, chord = part.chords[Math.floor(local / c.bar) % part.chords.length]
    c.tracks.forEach(function (t) {
      var pat = part.patterns[t.name]
      if (!pat) return
      var at = local % pat.length, slot = pat[at], steps = 1
      if (!slot || slot === HOLD) return
      while (at + steps < pat.length && local + steps < part.length && pat[at + steps] === HOLD) steps++
      var midi = slot.notes.map(function (n) { return note(c.scale, t.base, chord, n) })
      out.push({ track: t.name, steps: steps, vel: slot.vel, midi: midi.sort(function (a, b) { return a - b }) })
    })
    return out
  }

  var api = {
    muted: false, volume: 0.35, mood: 1, song: null,
    SCALES: SCALES, PROGRESSIONS: PROGRESSIONS, GROOVES: GROOVES, SOUNDS: SOUNDS,
    events: events, hz: hz,
    check: function (song) { return compile(song).problems }
  }
  var ctx = null, bus = null, noise = null, timer = null
  var next = 0, step = 0, wanted = false, pending = null, told = {}

  // tell reports each problem once as a page error, without stopping the
  // script that called play.
  function tell(problems) {
    problems.forEach(function (p) {
      if (told[p] || typeof setTimeout !== 'function') return
      told[p] = true
      setTimeout(function () { throw new Error('music: ' + p) })
    })
  }

  function ear() {  // browsers start audio only after a gesture
    if (!ctx && typeof AudioContext !== 'undefined') build()
    if (ctx && ctx.state === 'suspended') ctx.resume()
    if (wanted) run()
  }
  addEventListener('pointerdown', ear)
  addEventListener('keydown', ear)

  function build() {
    ctx = new AudioContext()
    bus = ctx.createGain()
    var limit = ctx.createDynamicsCompressor()  // a loud mix cannot clip
    bus.connect(limit); limit.connect(ctx.destination)
    noise = ctx.createBuffer(1, ctx.sampleRate, ctx.sampleRate)
    var data = noise.getChannelData(0)
    for (var i = 0; i < data.length; i++) data[i] = Math.random() * 2 - 1
    api.level()
  }
  // level applies volume and muted: call it after changing either.
  api.level = function () {
    if (bus) bus.gain.setTargetAtTime(api.muted ? 0 : api.volume, ctx.currentTime, 0.05)
  }
  // play starts a song. The song already playing goes on untouched; another
  // takes over at the next bar line. Before a gesture it waits.
  api.play = function (song) {
    song = song || api.song
    if (!song) return
    if (timer != null) {
      if (song !== api.song) pending = { song: song }
      else if (pending) pending.song = null
      return
    }
    tell(compile(song).problems)
    api.song = song; step = 0; pending = null; wanted = true
    if (ctx) run()
  }
  // cue moves to the first section with this name at the next bar line.
  api.cue = function (name) {
    if (!api.song) return
    if (timer == null) { var at = start(api.song, name); if (at != null) step = at; return }
    pending = { song: pending && pending.song, section: name }
  }
  api.stop = function () {
    wanted = false; pending = null
    if (timer != null) clearInterval(timer)
    timer = null
  }
  function start(song, name) {
    var parts = compiled(song).parts
    for (var i = 0; i < parts.length; i++) if (parts[i].name === name) return parts[i].start
    tell(['cue: no section ' + name])
    return null
  }
  function run() {
    if (!ctx || timer != null || !api.song) return
    next = ctx.currentTime + 0.1
    timer = setInterval(pump, TICK)
    pump()
  }
  function stepLength(c) {  // a bad bpm must not loop forever
    return 60 / Math.min(300, Math.max(30, +api.song.bpm || 100)) / c.beat
  }
  function pump() {
    var c = compiled(api.song), len = stepLength(c)
    // A hidden tab parks its timer and the clock runs on. Go on from now,
    // instead of starting every missed step at once.
    if (next < ctx.currentTime) next = ctx.currentTime
    while (next < ctx.currentTime + AHEAD) {
      if (pending && mod(step, c.bar) === 0) {
        if (pending.song) { tell(compile(pending.song).problems); api.song = pending.song; step = 0 }
        if (pending.section != null) { var at = start(api.song, pending.section); if (at != null) step = at }
        pending = null
        c = compiled(api.song); len = stepLength(c)
      }
      var swing = step % 2 ? Math.min(0.5, Math.max(0, +api.song.swing || 0)) * len : 0
      var list = events(api.song, step)
      for (var i = 0; i < list.length; i++) {
        var t = c.byName[list[i].track]
        if (api.mood >= t.mood) voice(t, list[i], next + swing, len)
      }
      next += len
      step++
    }
  }

  function voice(t, e, at, len) {
    var dur = e.steps * len, vol = e.vel * t.gain
    if (t.spec.hz || t.spec.noise) return sound(t.spec, 0, at, dur, vol)
    for (var i = 0; i < e.midi.length; i++) sound(t.spec, e.midi[i], at, dur, vol)
  }
  function sound(spec, midi, t, dur, vol) {
    var a = Math.max(0.001, +spec.attack || 0.005), d = Math.max(0.001, +spec.decay || 0.1)
    var r = Math.max(0.001, +spec.release || 0.05), sus = spec.sustain == null ? 1 : +spec.sustain
    var cents = spec.detune ? [0, +spec.detune] : [0]
    var peak = (spec.gain == null ? 0.2 : +spec.gain) * vol / cents.length
    var end = t + Math.max(dur, a), stop = end + 3 * r
    var g = ctx.createGain(), into = g
    g.gain.setValueAtTime(0, t)
    g.gain.linearRampToValueAtTime(peak, t + a)
    g.gain.setTargetAtTime(peak * sus, t + a, d / 3)
    g.gain.setTargetAtTime(0, end, r / 3)
    g.connect(bus)
    if (spec.cutoff) {
      into = ctx.createBiquadFilter()
      into.type = 'lowpass'; into.frequency.value = spec.cutoff
      into.connect(g)
    }
    if (spec.noise) {
      var s = ctx.createBufferSource(), hp = ctx.createBiquadFilter()
      s.buffer = noise; s.loop = true
      hp.type = 'highpass'; hp.frequency.value = spec.noise
      s.connect(hp); hp.connect(into); s.start(t); s.stop(stop)
      return
    }
    cents.forEach(function (c) {
      var o = ctx.createOscillator()
      o.type = spec.wave || 'triangle'
      if (spec.hz) {
        o.frequency.setValueAtTime(spec.hz, t)
        if (spec.to) o.frequency.exponentialRampToValueAtTime(spec.to, t + d)
      } else o.frequency.value = hz(midi)
      o.detune.value = c
      o.connect(into); o.start(t); o.stop(stop)
    })
  }
  return api
})()
</script>

A SONG is plain data. This one is an example of the shape, not a sound to
reuse:
  var SONG = {
    key: 'D', scale: 'dorian', bpm: 100,
    beat: 2, bar: 4,   // steps per beat (2 eighths, 4 sixteenths, 3 triplets), beats per bar
    swing: 0,          // 0 to 0.5 pushes every second step late: 0.2 is a lazy shuffle
    tracks: {
      bass: { sound: 'bass', octave: 2 },
      keys: { sound: 'pad', octave: 3 },
      lead: { sound: 'pluck', octave: 4, mood: 0.3 },
      kick: { mood: 0.6 }, snare: { mood: 0.6 }, hat: { mood: 0.6 }  // the sound is the track's name
    },
    sections: {
      A: { chords: [0, 3, 4, 0],
        bass: 'c0 . . c0 c2 . c0 .',
        keys: 'C - - - - - - -',
        lead: 'c0 . c1 c2 c1 - . . | c0 . c1 c2 c3 - c2 . | c0 . c1 c2 c1 - c0 . | c2 c1 c0 - c-1 - c0 .',
        kick: 'x . . . x x . .', snare: '. . x . . . x .', hat: 'x x~ x x~ x x~ x x~' },
      B: { chords: [2, 6, 2, 4],
        bass: 'c0 - - - c0 - c2 -',
        keys: 'C - - - C - - -',
        lead: '4 - - 3 2 - . . | 1 - 2 - 3 - . . | 4 - - 5 6 - 7 - | 6 - - - . . . .',
        hat: 'x . x~ . x . x~ .' }
    },
    form: 'AABA'   // single letters, or a list: ['intro', 'A', 'A', 'B', 'A']
  }

PATTERNS: one token per step, spaces between. | is ignored, so use it to mark
bars.
  .            rest
  -            hold the note before for one more step
  0 2 -1 9     a scale degree from the track's octave (7 is an octave up in a
               seven-note scale, -1 the note below)
  c0 c1 c2     this bar's chord: root, third, fifth. They repeat by octaves:
               c3 is the root above, c4 the third above, c-1 the fifth below
  C  C7        the whole chord, or it with its seventh, voiced close around
               the track's octave
  x            a drum hit, or the chord root on a pitched track
  a+b          notes at once: c0+c2, 0+4+7
  # b after    a semitone up or down: 4#, c1b
  ! ~ last     loud or soft: x!, x~ (a ghost note), c0!
A pattern loops inside its section: one bar repeats every bar, four bars run
once. A track with no pattern in a section is silent there. A section lasts
as many bars as it has chords, or as its longest pattern, or bars: n. chords
lists scale degrees, one a bar. The scale decides each chord's color: in major
0, 3 and 4 are major and 1, 2 and 5 minor; in minor 2, 5 and 6 are major and
4 is minor; harmonic minor makes 4 major, which pulls hard back to 0.

WIRING:
- Write the song as data in the page and call music.play(SONG) once after the
  page builds. It stays silent until the visitor presses a key or clicks,
  then starts by itself. Calling play with the song already playing does
  nothing, so it is safe from a render. Another song takes over at the next
  bar line; stop() then play() starts over.
- music.cue('B') moves to that section at the next bar line: a boss arrives,
  a room clears, a timer runs low. A scene with its own feel gets its own
  song: music.play(BOSS).
- Mood is 0 to 1. A track with mood: 0.6 plays only while music.mood is at
  least 0.6. Give the base layers 0 and the drums or the lead a threshold,
  then set music.mood from what is happening; the next step uses it.
- A broken song still plays: an unknown note rests, an unknown sound falls
  back to keys, and each problem is reported once as a page error, so it
  reaches you. music.check(SONG) returns the list.
- music.events(SONG, step) lists the notes that start on a step, without
  sound: use it to light things on the beat.
- The mix is quiet on purpose (volume 0.35) and passes a compressor. Leave
  room for the sound effects: they sit on top of the music.
- A mute button and the M key flip music.muted, then call music.level(). Keep
  it with bd.save('muted', ...) and restore it on load, with fx.muted if the
  feel skill is on the page, so one switch silences both. Skip the key while
  the visitor types in the post form or any other field.
- Music is on by default only if the page is a game that wants it. A quiet
  page stays quiet; never autoplay over a form or reading.
- For generated worlds, build songs with the random skill's seeded rng: pick
  the key, scale, progression, groove and sounds from the tables and write
  motifs in chord tones, so everyone gets the same song from the same seed.

WRITING MUSIC: work in this order, and listen to the feel the page asks for.
- Feel first: scale, tempo and meter. 60 to 80 bpm is calm, 90 to 110
  walking, 120 to 140 action, 150 and up a chase. bar: 3 sways (a waltz, a
  lullaby), beat: 3 gallops or shuffles. Rarer choices (phrygian, lydian, 3/4,
  swing) make a world memorable.
- Harmony: one chord a bar, two for faster motion. A two-chord vamp suits
  music a player hears for minutes; four chords tell more of a story. End a
  section on 0 to rest or on 4 to lean into the next.
- Bass: the chord root on beat one, locked to the kick. Fifths (c2, c-1),
  octaves (c3) and a step into the next bar's root give it motion.
- Melody last. A motif of 2 to 5 notes with a clear rhythm, then repeat it and
  vary it: change the last note, move it a step, turn it upside down. Bars 1
  and 2 ask, bars 3 and 4 answer and land on the root. A motif in chord tones
  follows the chords by itself. Put chord tones on strong beats and steps
  between them. Move mostly by step; after a leap, step back the other way.
  One high point a phrase. Rests are half of a melody.
- Rhythm sets the genre more than the notes do. Start from a groove and
  change it. Push a note one step before the beat for drive. Soft ghost notes
  (x~) and loud accents (x!) make a part breathe; a machine plays every hit
  the same.
- Arrangement: one register a part, bass in octave 1 or 2, chords in 3 or 4,
  the lead in 4 or 5. Three or four parts at once is plenty. Sections should
  differ: B drops the drums, thins out, or climbs higher. A form of 8 to 16
  bars before it repeats; a four-bar loop wears out in a minute. An intro is
  one or two parts.
- Sound: copy a SOUNDS row and change one number at a time. Sine and triangle
  are soft; sawtooth and square need a cutoff. Pads attack slowly, plucks and
  bells have sustain 0. The bass and the kick are the loudest parts, hats the
  quietest.
- Every world gets its own song. Do not fall back on the same scale, the same
  progression and the same backbeat each time; the tables are a place to
  start, not the answer.
`
