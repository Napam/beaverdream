package skills

// audioReference is the audio skill: background music from a seed. The model
// reads it with load_skill. The script block is run by audio_test.go, which
// checks that every note is in its scale, that a seed, a number or text,
// always writes the same song, that the lead leaves rests and stays in range,
// that the scheduler starts every step once and on the beat, drops the steps
// a hidden tab missed and survives a bad bpm, that a second start does not
// make a second loop, that nothing sounds before a gesture or when muted, and
// that the mood switches the layers; change both together.
const audioReference = `AUDIO REFERENCE:
Use this when a page wants background music. One-shot sounds (a coin, a hit,
an explosion) belong to the feel skill and its zzfx synth. This core is
tested. Reuse the one already on the page, or copy it whole.

<script>
var music = (function () {
  var AHEAD = 0.4, TICK = 25     // seconds scheduled ahead, ms between checks
  // Scale steps above the root, in semitones. Every note of a pentatonic
  // scale sits well with every other, so a melody on one cannot clash.
  var SCALES = { minor: [0, 3, 5, 7, 10], major: [0, 2, 4, 7, 9], dorian: [0, 2, 3, 5, 7, 9, 10] }
  var BAR = 8                    // steps per bar, an eighth note each
  var LAYER = { pad: 0, bass: 0, lead: 0.34, kick: 0.67, snare: 0.67, hat: 0.67 }  // mood needed

  function rng(seed) {
    var a = seed >>> 0
    if (typeof seed !== 'number') {  // text, such as the random skill's seeds
      a = 2166136261
      for (var i = 0; i < String(seed).length; i++) a = Math.imul(a ^ String(seed).charCodeAt(i), 16777619) >>> 0
    }
    return function () {
      a = (a + 0x6D2B79F5) >>> 0
      var t = a
      t = Math.imul(t ^ (t >>> 15), t | 1)
      t ^= t + Math.imul(t ^ (t >>> 7), t | 61)
      return ((t ^ (t >>> 14)) >>> 0) / 4294967296
    }
  }
  function hz(midi) { return 440 * Math.pow(2, (midi - 69) / 12) }
  // pitch is degree d of the scale above a root, any integer, wrapping octaves.
  function pitch(root, scale, d) {
    var n = scale.length, oct = Math.floor(d / n)
    return root + 12 * oct + scale[((d % n) + n) % n]
  }

  // compose writes a whole song from a seed, a number or any text: same seed,
  // same song, so every visitor hears the same one. opt may fix scale (minor,
  // major, dorian) and bpm. The lead is a walk over the scale with rests, the
  // bass a root pulse.
  function compose(seed, opt) {
    var r = rng(seed), o = opt || {}
    var name = SCALES[o.scale] ? o.scale : ['minor', 'major', 'dorian'][Math.floor(r() * 3)]
    var scale = SCALES[name]
    var song = {
      seed: seed, scale: scale, name: name,
      root: 36 + Math.floor(r() * 12),            // C2 to B2
      bpm: o.bpm || 80 + Math.floor(r() * 50),
      prog: [0], bass: [], lead: [],
      kick: [1, 0, 0, 0, 1, 0, r() < 0.5 ? 1 : 0, 0],
      snare: [0, 0, 1, 0, 0, 0, 1, 0],
      hat: [1, 1, 1, 1, 1, 1, 1, 1]
    }
    for (var b = 1; b < 4; b++) song.prog.push(Math.floor(r() * scale.length))
    song.bass = [0, null, r() < 0.5 ? 0 : null, null, 0, null, r() < 0.5 ? (r() < 0.5 ? 0 : 2) : null, null]
    var d = scale.length, top = scale.length * 2
    for (var i = 0; i < BAR * 4; i++) {
      d = Math.max(0, Math.min(top, d + Math.floor(r() * 5) - 2))
      var rest = r() < 0.4 || i % BAR === BAR - 1   // at least 40% silence, and a breath each bar
      song.lead.push(rest ? null : { d: d, len: r() < 0.3 ? 2 : 1 })
    }
    return song
  }

  // events lists what starts on one step. Pure: no audio, only the song.
  function events(song, step) {
    var out = [], i = step % BAR, deg = song.prog[Math.floor(step / BAR) % song.prog.length]
    var scale = song.scale, root = song.root
    if (i === 0) out.push({ voice: 'pad', len: BAR, midi: [0, 2, 4].map(function (k) { return pitch(root + 12, scale, deg + k) }) })
    if (song.bass[i] != null) out.push({ voice: 'bass', len: 1, midi: [pitch(root, scale, deg + song.bass[i])] })
    var l = song.lead[step % song.lead.length]
    if (l) out.push({ voice: 'lead', len: l.len, midi: [pitch(root + 24, scale, l.d)] })
    if (song.kick[i]) out.push({ voice: 'kick', len: 1, midi: [] })
    if (song.snare[i]) out.push({ voice: 'snare', len: 1, midi: [] })
    if (song.hat[i]) out.push({ voice: 'hat', len: 1, midi: [] })
    return out
  }

  var api = { muted: false, volume: 0.35, mood: 1, song: null, compose: compose, events: events, pitch: pitch, hz: hz }
  var ctx = null, bus = null, noise = null, timer = null
  var next = 0, step = 0, wanted = false

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
    noise = ctx.createBuffer(1, Math.floor(ctx.sampleRate * 0.5), ctx.sampleRate)
    var data = noise.getChannelData(0)
    for (var i = 0; i < data.length; i++) data[i] = Math.random() * 2 - 1
    api.level()
  }
  // level applies volume and muted: call it after changing either.
  api.level = function () {
    if (bus) bus.gain.setTargetAtTime(api.muted ? 0 : api.volume, ctx.currentTime, 0.05)
  }
  // play starts a song, or swaps to another without a second loop. Before a
  // gesture it waits and starts on the first one.
  api.play = function (song) {
    api.song = song || api.song || compose(1)
    step = 0
    wanted = true
    if (ctx) run()
  }
  api.stop = function () {
    wanted = false
    if (timer != null) clearInterval(timer)
    timer = null
  }
  function run() {
    if (!ctx || timer != null || !api.song) return
    next = ctx.currentTime + 0.1
    timer = setInterval(pump, TICK)
    pump()
  }
  function pump() {
    var len = 60 / Math.min(300, Math.max(30, +api.song.bpm || 100)) / 2  // a bad bpm must not loop forever
    // A hidden tab parks its timer and the clock runs on. Drop the steps
    // that were missed instead of starting them all at once.
    if (next < ctx.currentTime) {
      step += Math.ceil((ctx.currentTime - next) / len)
      next = ctx.currentTime
    }
    while (next < ctx.currentTime + AHEAD) {
      var list = events(api.song, step)
      for (var i = 0; i < list.length; i++) {
        var e = list[i]
        if (api.mood >= LAYER[e.voice]) voice(e, next, len)
      }
      next += len
      step++
    }
  }

  function tone(type, midi, t, dur, vol, cutoff, attack) {
    var o = ctx.createOscillator(), f = ctx.createBiquadFilter(), g = ctx.createGain()
    o.type = type
    o.frequency.value = hz(midi)
    f.type = 'lowpass'; f.frequency.value = cutoff
    g.gain.setValueAtTime(0.0001, t)
    g.gain.linearRampToValueAtTime(vol, t + attack)
    g.gain.exponentialRampToValueAtTime(0.0001, t + dur)
    o.connect(f); f.connect(g); g.connect(bus)
    o.start(t); o.stop(t + dur + 0.05)
  }
  function hiss(t, dur, vol, cutoff) {
    var s = ctx.createBufferSource(), f = ctx.createBiquadFilter(), g = ctx.createGain()
    s.buffer = noise
    f.type = 'highpass'; f.frequency.value = cutoff
    g.gain.setValueAtTime(vol, t)
    g.gain.exponentialRampToValueAtTime(0.0001, t + dur)
    s.connect(f); f.connect(g); g.connect(bus)
    s.start(t); s.stop(t + dur)
  }
  function voice(e, t, len) {
    var dur = e.len * len
    if (e.voice === 'pad') e.midi.forEach(function (m) { tone('sawtooth', m, t, dur, 0.04, 900, 0.3) })
    else if (e.voice === 'bass') tone('triangle', e.midi[0], t, dur * 1.5, 0.3, 500, 0.01)
    else if (e.voice === 'lead') tone('triangle', e.midi[0], t, dur * 1.5, 0.14, 1800, 0.01)
    else if (e.voice === 'kick') {
      var o = ctx.createOscillator(), g = ctx.createGain()
      o.frequency.setValueAtTime(130, t); o.frequency.exponentialRampToValueAtTime(40, t + 0.12)
      g.gain.setValueAtTime(0.5, t); g.gain.exponentialRampToValueAtTime(0.0001, t + 0.18)
      o.connect(g); g.connect(bus); o.start(t); o.stop(t + 0.2)
    }
    else if (e.voice === 'snare') hiss(t, 0.12, 0.18, 1800)
    else if (e.voice === 'hat') hiss(t, 0.04, 0.06, 7000)
  }
  return api
})()
</script>

WIRING:
- music.play(music.compose(seed)) once, after the page builds. Take the seed
  from something every visitor shares (the world seed from the random skill,
  or a number written in the page), so everyone hears the same song. It stays
  silent until the visitor presses a key or clicks, then starts by itself.
- One loop only: calling play again swaps the song and never stacks a second
  one. Do not start music from a repeating timer of your own.
- Mood is 0 to 1 and picks the layers: a pad and a bass always, a lead from
  0.34, drums from 0.67. Set music.mood from what is happening (calm, danger,
  a fight, a boss) and let it change at any time; the next step uses it.
- The mix is quiet on purpose (volume 0.35) and passes a compressor. Leave
  room for the sound effects: they sit on top of the music, not under it.
- A mute button and the M key flip music.muted, then call music.level(). Keep
  it with bd.save('muted', ...) and restore it on load, with fx.muted if the
  feel skill is on the page, so one switch silences both. Skip the key while
  the visitor types in the post form or any other field.
- Music is on by default only if the page is a game that wants it. A quiet
  page stays quiet; never autoplay over a form or reading.

TASTE: the song is a seed. Ask for another feel by changing the scale
(minor for tense, major for bright, dorian for a road or a quest) and the bpm
(70 calm, 100 walking, 140 chase), not by writing notes. For a different
sound, change a voice in voice(): the waveform and cutoff matter most, a
lower cutoff is softer. Never use a raw sawtooth or square without the
low-pass, and keep every note short or enveloped.
`
