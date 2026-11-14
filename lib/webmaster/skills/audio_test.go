package skills

import (
	"encoding/json"
	"testing"

	"github.com/Napam/beaverdream/lib/webmaster/internal/jsrun"
)

// audioHarness runs the core from audioReference against a fake AudioContext,
// a fake interval timer and a fake setTimeout. Each check pushes a line to
// fails when the player does something else than it should.
const audioHarness = `
const fs = require('fs')
const vm = require('vm')
const src = fs.readFileSync(process.argv[2], 'utf8')
const fails = []
const out = { fails }
function expect(name, got, want) {
  if (JSON.stringify(got) !== JSON.stringify(want)) fails.push(name + ': got ' + JSON.stringify(got) + ', want ' + JSON.stringify(want))
}
function ok(name, cond, detail) { if (!cond) fails.push(name + (detail === undefined ? '' : ': ' + JSON.stringify(detail))) }

let handlers, intervals, oscillators, sources, gains, contexts, timeouts
function fresh() {
  handlers = {}; intervals = []; oscillators = []; sources = []; gains = []; contexts = []; timeouts = []
  const param = () => ({ value: 0, setValueAtTime() {}, linearRampToValueAtTime() {}, exponentialRampToValueAtTime() {},
    setTargetAtTime(v) { this.target = v } })
  const node = () => ({ connect() {}, frequency: param(), gain: param(), detune: param() })
  const sandbox = {
    addEventListener: (type, fn) => { handlers[type] = fn },
    setInterval: (fn) => { intervals.push(fn); return intervals.length },
    clearInterval: (id) => { intervals[id - 1] = null },
    setTimeout: (fn) => { timeouts.push(fn) },
    AudioContext: function () {
      this.state = 'suspended'; this.currentTime = 0; this.sampleRate = 8000; this.destination = {}
      this.resume = () => { this.state = 'running' }
      this.createGain = () => { const g = node(); gains.push(g); return g }
      this.createDynamicsCompressor = node
      this.createBiquadFilter = node
      this.createOscillator = () => { const o = node(); o.start = (t) => { o.at = t }; o.stop = () => {}; oscillators.push(o); return o }
      this.createBuffer = (c, n) => { const d = new Float32Array(n); return { getChannelData: () => d } }
      this.createBufferSource = () => { const s = node(); s.start = (t) => { s.at = t }; s.stop = () => {}; sources.push(s); return s }
      contexts.push(this)
    },
  }
  const before = Object.keys(sandbox)
  vm.createContext(sandbox)
  vm.runInContext(src, sandbox)
  out.globals = Object.keys(sandbox).filter((k) => before.indexOf(k) < 0)
  return sandbox.music
}
const live = () => intervals.filter(Boolean).length
// run moves the clock from the context's time to end, pumping as a timer would.
function run(end, at) {
  const ctx = contexts[0]
  for (let t = ctx.currentTime; t <= end + 1e-9; t += 0.05) {
    ctx.currentTime = t
    if (at && Math.abs(t - at.t) < 0.025) at.fn()
    const loop = intervals.filter(Boolean)[0]
    if (loop) loop()
  }
}
const times = (list) => list.map((n) => n.at).sort((a, b) => a - b)
const midiOf = (f) => Math.round(69 + 12 * Math.log2(f / 440))
const S = (o) => Object.assign({ key: 'C', scale: 'major', bpm: 120, beat: 2, bar: 4 }, o)
const at = (music, song, step, track) => music.events(song, step).filter((e) => e.track === track)

let music = fresh()

// Notation: chord tones follow the bar's chord, C is a close chord, holds,
// accidentals, notes at once and accents.
const notes = S({
  tracks: { bass: { octave: 2 }, keys: { sound: 'pad', octave: 4 }, lead: { sound: 'pluck', octave: 4 } },
  sections: { A: { chords: [0, 3, 4, 5],
    bass: 'c0 . c2 . c1 . c-1 .',
    keys: 'C - - - - - - -',
    lead: '0 - - . 4# 2+4 c0! 7~ | . . . . . . c0 -' } }
})
expect('no problems in a valid song', music.check(notes), [])
expect('bass chord tones over I', [0, 2, 4, 6].map((s) => at(music, notes, s, 'bass')[0].midi[0]), [36, 43, 40, 31])
expect('bass chord tones over IV', [8, 10, 12, 14].map((s) => at(music, notes, s, 'bass')[0].midi[0]), [41, 48, 45, 36])
expect('a close C, IV and V', [0, 8, 16].map((s) => at(music, notes, s, 'keys')[0].midi), [[55, 60, 64], [57, 60, 65], [55, 59, 62]])
const seventh = S({ tracks: { keys: { sound: 'pad' } }, sections: { A: { chords: [4], keys: 'C7' } } })
expect('a close C7 on V', music.events(seventh, 0)[0].midi, [55, 59, 62, 65])
const high = S({ tracks: { lead: {} }, sections: { A: { chords: [0], lead: 'c3 c4 c5 c-2 c-3' } } })
expect('chord tones repeat by octaves', [0, 1, 2, 3, 4].map((s) => music.events(high, s)[0].midi[0]), [72, 76, 79, 52, 48])
expect('a held chord lasts the bar', at(music, notes, 0, 'keys')[0].steps, 8)
expect('nothing starts on a hold', [1, 2, 3].map((s) => at(music, notes, s, 'keys').length), [0, 0, 0])
const lead = (s) => at(music, notes, s, 'lead')
expect('a degree with two holds', [lead(0)[0].midi, lead(0)[0].steps, lead(1).length, lead(3).length], [[60], 3, 0, 0])
expect('a sharp', lead(4)[0].midi, [68])
expect('notes at once', lead(5)[0].midi, [64, 67])
expect('accent, normal and soft', [lead(6)[0].vel, lead(5)[0].vel, lead(7)[0].vel], [1, 0.7, 0.35])
expect('a chord tone in the pattern\'s second bar follows that bar\'s chord', [lead(14)[0].midi, lead(14)[0].steps], [[65], 2])
expect('a two-bar pattern loops over four bars', lead(16)[0].midi, [60])
const cut = S({ tracks: { bass: {} }, sections: { A: { bass: '. . . . . . . 0 | - - . . . . . .', bars: 1 } } })
expect('a hold stops at the end of the section', music.events(cut, 7)[0].steps, 1)
const wrap = S({ tracks: { bass: {} }, sections: { A: { bass: '- . . . . . . 0', bars: 2 } } })
expect('a hold does not wrap around its pattern', music.events(wrap, 7)[0].steps, 1)

// Meter and form: a bar is beat times bar steps, sections run in order and
// last their chords, their longest pattern or bars, and the form loops.
const waltz = S({ beat: 3, bar: 2, tracks: { bass: { octave: 2 } }, sections: { A: { chords: [0, 4], bass: 'c0' } } })
expect('6/8 changes chord every six steps', [0, 5, 6, 11, 12].map((s) => at(music, waltz, s, 'bass')[0].midi[0]), [36, 36, 43, 43, 36])
const form = S({
  tracks: { bass: { octave: 2 } },
  sections: { A: { bass: '0' }, B: { chords: [0, 0], bass: '4' }, C: { bars: 2 }, L: { bass: '0 . . . . . . . 1 . . . . . . .' } },
  form: 'ABACL'
})
const formBass = (s) => at(music, form, s, 'bass').map((e) => e.midi[0])
expect('sections in form order', [0, 7, 8, 23, 24, 31].map(formBass), [[36], [36], [43], [43], [36], [36]])
expect('a section without a pattern is silent', [32, 47].map(formBass), [[], []])
expect('a long pattern sets the section length', [48, 56, 63].map(formBass), [[36], [38], []])
expect('the form loops', formBass(64), [36])
const listed = S({ tracks: { bass: { octave: 2 } }, sections: { intro: { bass: '4' }, A: { bass: '0' } }, form: ['intro', 'A'] })
expect('a form as a list', [0, 8].map((s) => at(music, listed, s, 'bass')[0].midi[0]), [43, 36])

// Tables: every progression stays in its scale, every groove fills one bar of
// its meter, every table entry is a valid part.
for (const [name, scale] of Object.entries(music.SCALES)) {
  ok('scale ' + name + ' rises from 0 within an octave', scale[0] === 0 && scale.every((v, i) => i === 0 || (v > scale[i - 1] && v < 12)), scale)
}
for (const [name, p] of Object.entries(music.PROGRESSIONS)) {
  const song = S({ key: 'E', scale: p.scale, tracks: { bass: { octave: 2 }, keys: { sound: 'pad' }, lead: { sound: 'lead', octave: 5 } },
    sections: { A: { chords: p.chords, bass: 'c0 c1 c2 c3 c4 c-1 . .', keys: 'C', lead: '0 1 2 3 4 5 6 7' } } })
  expect('progression ' + name + ' is valid', music.check(song), [])
  const set = music.SCALES[p.scale]
  let outside = []
  for (let s = 0; s < 8 * p.chords.length; s++) {
    for (const e of music.events(song, s)) for (const m of e.midi) if (set.indexOf(((m - 4) % 12 + 12) % 12) < 0) outside.push(m)
  }
  expect('progression ' + name + ' stays in ' + p.scale, outside, [])
}
for (const [name, g] of Object.entries(music.GROOVES)) {
  const sec = {}
  for (const d of ['kick', 'snare', 'hat']) {
    const steps = g[d].split(/\s+/).filter((t) => t && t !== '|').length
    ok('groove ' + name + ' ' + d + ' is one bar', steps === 0 || steps === g.beat * g.bar, steps)
    sec[d] = g[d]
  }
  expect('groove ' + name + ' is valid', music.check(S({ beat: g.beat, bar: g.bar, tracks: { kick: {}, snare: {}, hat: {} }, sections: { A: sec } })), [])
}
const all = {}, allSec = {}
for (const name of Object.keys(music.SOUNDS)) { all[name] = {}; allSec[name] = '0+2' }
const orchestra = S({ tracks: all, sections: { A: allSec } })
expect('every sound is valid', music.check(orchestra), [])

// A broken song still plays: each problem is listed, and reported once as an
// error thrown outside the caller.
const bad = S({ scale: 'nope', key: 'H', tracks: { a: { sound: 'kazoo' } },
  sections: { A: { chords: ['ii'], a: '0 q 1', ghost: 'x' } }, form: 'AZ' })
const problems = music.check(bad)
expect('every kind of problem is listed once', problems.length, 7)
expect('a broken song still has notes', music.events(bad, 0).length, 1)
handlers.keydown()
music.play(bad)
expect('each problem is reported', timeouts.length, 7)
let thrown = 0
for (const fn of timeouts) { try { fn() } catch (e) { thrown++ } }
expect('each report throws', thrown, 7)
ok('the broken song plays', oscillators.length > 0)
music.stop(); music.play(bad)
expect('a problem is reported once', timeouts.length, 7)
expect('an empty song has a problem', music.check({}).length, 1)
expect('an empty song has no notes', music.events({}, 3), [])
music.stop(); music.play({}); run(1)
expect('an empty song keeps one quiet loop', live(), 1)

// Gesture: nothing sounds before one, and a play before one starts at the first.
const hats = S({ tracks: { hat: {} }, sections: { A: { hat: 'x' } } })
music = fresh()
music.play(hats)
expect('before a gesture: contexts, loops, voices', [contexts.length, live(), sources.length], [0, 0, 0])
handlers.pointerdown()
expect('after a gesture: contexts, loops', [contexts.length, live()], [1, 1])
ok('after a gesture it sounds', sources.length > 0)
music.play(hats); music.play(hats)
expect('play again keeps one loop', live(), 1)

// Scheduler: every step once, one step apart, within the lookahead.
music = fresh(); handlers.keydown()
music.play(hats)
run(8)
const ticks = times(sources)
const gaps = new Set()
for (let i = 1; i < ticks.length; i++) gaps.add(Math.round((ticks[i] - ticks[i - 1]) * 1e6))
ok('steps scheduled', ticks.length > 12, ticks.length)
expect('one step apart', [...gaps], [250000])
ok('within the lookahead', ticks[ticks.length - 1] <= contexts[0].currentTime + 0.4 + 1e-9)

// A hidden tab parks the timer. Coming back must not start the missed steps.
sources.length = 0
contexts[0].currentTime = 20
intervals.filter(Boolean)[0]()
ok('missed steps are dropped', Math.min.apply(null, times(sources)) >= 20 - 1e-9)

// Swing pushes every second step late, and a pair still takes two steps.
music = fresh(); handlers.keydown()
music.play(Object.assign({}, hats, { swing: 0.25 }))
run(4)
const swung = times(sources), pairs = new Set()
for (let i = 1; i < swung.length; i++) pairs.add(Math.round((swung[i] - swung[i - 1]) * 1e6))
expect('swung gaps', [...pairs].sort(), [187500, 312500])

// A bpm that is no tempo still plays, and the loop ends.
for (const bpm of [-120, Infinity, 'fast']) {
  music = fresh(); handlers.keydown()
  music.play(Object.assign({}, hats, { bpm }))
  ok('bpm ' + bpm + ' plays a bounded number of steps', sources.length > 0 && sources.length < 50, sources.length)
}

// Another song waits for the bar line; the same song goes on.
const kicks = S({ tracks: { kick: {} }, sections: { A: { kick: 'x' } } })
music = fresh(); handlers.keydown()
music.play(hats)
const first = times(sources)[0]
run(6, { t: 1.0, fn: () => music.play(kicks) })
const swapAt = times(oscillators)[0], lastHat = times(sources).pop()
ok('the new song starts on a bar line', Math.abs(((swapAt - first) / 2 + 1e-9) % 1) < 1e-6, swapAt - first)
ok('the old song stops where the new one starts', lastHat < swapAt, [lastHat, swapAt])
expect('one loop after a swap', live(), 1)
const scale = S({ tracks: { lead: { sound: 'pluck' } }, sections: { A: { lead: '0 1 2 3 4 5 6 7' } } })
music = fresh(); handlers.keydown()
music.play(scale)
run(5, { t: 1.3, fn: () => music.play(scale) })
const order = oscillators.slice().sort((a, b) => a.at - b.at).map((o) => [60, 62, 64, 65, 67, 69, 71, 72].indexOf(midiOf(o.frequency.value)))
ok('playing the same song again does not restart it', order.every((v, i) => i === 0 || v === (order[i - 1] + 1) % 8), order)

// A cue moves to a section at the next bar line, not where it would start.
const cued = S({ tracks: { hat: {}, kick: {} }, sections: { A: { hat: 'x', bars: 2 }, B: { kick: 'x' } }, form: 'AB' })
music = fresh(); handlers.keydown()
music.play(cued)
const start = times(sources)[0]
run(3, { t: 1.0, fn: () => music.cue('B') })
expect('the cue lands on the next bar line', Math.round((times(oscillators)[0] - start) * 1e6), 2000000)

// Mood: a track plays only while music.mood reaches its threshold.
const layered = S({ tracks: { bass: {}, hat: { mood: 0.5 }, snare: { mood: 0.9 } }, sections: { A: { bass: 'x', hat: 'x', snare: 'x' } } })
function voices(mood) {
  music = fresh(); handlers.keydown()
  music.mood = mood
  music.play(layered)
  run(4)
  return [oscillators.length > 0, sources.length]
}
const calm = voices(0), mid = voices(0.6), full = voices(1)
ok('calm: the base layer alone', calm[0] && calm[1] === 0, calm)
ok('mid: one more layer', mid[0] && mid[1] > 0, mid)
expect('full: every layer', full[1], 2 * mid[1])

// Every sound plays.
music = fresh(); handlers.keydown()
music.play(orchestra)
ok('every sound makes a voice', oscillators.length > 0 && sources.length > 0)

// Mute: the bus goes to zero, and back to the volume.
const bus = gains[0]
music.muted = true; music.level()
expect('muted', bus.gain.target, 0)
music.muted = false; music.level()
ok('unmuted', bus.gain.target > 0, bus.gain.target)

// Stop: the loop is gone, and play starts a fresh one.
music.stop()
expect('stopped', live(), 0)
music.play()
expect('restarted', live(), 1)

console.log(JSON.stringify(out))
`

func TestAudioReferenceCore(t *testing.T) {
	out := jsrun.Run(t, audioReference, audioHarness, "")
	var got struct {
		Fails   []string
		Globals []string
	}
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatalf("decode: %v\n%s", err, out)
	}
	if len(got.Globals) != 1 || got.Globals[0] != "music" {
		t.Errorf("the script leaks globals %v, want only music", got.Globals)
	}
	for _, f := range got.Fails {
		t.Error(f)
	}
}
