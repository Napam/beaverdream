package skills

import (
	"encoding/json"
	"testing"

	"github.com/Napam/beaverdream/lib/webmaster/internal/jsrun"
)

// audioHarness runs the core from audioReference against a fake AudioContext
// and a fake interval timer, and reports what it scheduled.
const audioHarness = `
const fs = require('fs')
const vm = require('vm')
const src = fs.readFileSync(process.argv[2], 'utf8')
const out = {}

let handlers = {}, intervals = [], oscillators = [], sources = [], gains = [], contexts = []
function fresh() {
  handlers = {}; intervals = []; oscillators = []; sources = []; gains = []; contexts = []
  const param = () => ({ value: 0, setValueAtTime() {}, linearRampToValueAtTime() {}, exponentialRampToValueAtTime() {},
    setTargetAtTime(v) { this.target = v } })
  const node = () => ({ connect() {}, frequency: param(), gain: param() })
  const sandbox = {
    addEventListener: (type, fn) => { handlers[type] = fn },
    setInterval: (fn) => { intervals.push(fn); return intervals.length },
    clearInterval: (id) => { intervals[id - 1] = null },
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

let music = fresh()

// Pitch: every note of a song's pad, bass and lead is in its scale.
let inScale = true, seeds = 0
for (let seed = 1; seed <= 40; seed++) {
  const song = music.compose(seed)
  seeds++
  for (let step = 0; step < 64; step++) {
    for (const e of music.events(song, step)) {
      for (const m of e.midi) {
        const k = (((m - song.root) % 12) + 12) % 12
        if (song.scale.indexOf(k) < 0) inScale = false
      }
    }
  }
}
out.inScale = inScale

// Seeds: the same seed writes the same song, another seed another one, and the
// options are honored.
out.sameSeed = JSON.stringify(music.compose(7)) === JSON.stringify(music.compose(7))
out.otherSeed = JSON.stringify(music.compose(7)) !== JSON.stringify(music.compose(8))
const fixed = music.compose(3, { scale: 'dorian', bpm: 100 })
out.options = [fixed.name, fixed.bpm]

// Lead: rests at least 30%, a breath at the end of every bar, two octaves at most.
let restsOk = true, breathOk = true, rangeOk = true
for (let seed = 1; seed <= 40; seed++) {
  const song = music.compose(seed)
  const rests = song.lead.filter((l) => !l).length / song.lead.length
  if (rests < 0.3) restsOk = false
  song.lead.forEach((l, i) => { if (i % 8 === 7 && l) breathOk = false })
  const notes = song.lead.filter(Boolean).map((l) => music.pitch(song.root + 24, song.scale, l.d))
  if (Math.max.apply(null, notes) - Math.min.apply(null, notes) > 24) rangeOk = false
}
out.leadRests = restsOk
out.leadBreath = breathOk
out.leadRange = rangeOk

// Nothing sounds before a gesture, and a play before one starts at the first.
music.play(music.compose(5))
out.beforeGesture = [contexts.length, live(), oscillators.length]
handlers.pointerdown()
out.afterGesture = [contexts.length, live(), oscillators.length > 0]

// Scheduler: one loop however often play is called, every step started once
// and exactly one step apart, nothing scheduled past the lookahead.
music.play(music.compose(5)); music.play(music.compose(5))
out.oneLoop = live()
const song = music.compose(5, { bpm: 120 })
music = fresh(); handlers.keydown()
music.play(song)
const ctx = contexts[0]
const stepLen = 60 / 120 / 2
for (let t = 0; t < 8; t += 0.05) { ctx.currentTime = t; intervals.filter(Boolean)[0]() }
const hats = sources.map((s) => s.at).filter((v, i, a) => a.indexOf(v) === i).sort((a, b) => a - b)
const gaps = new Set()
for (let i = 1; i < hats.length; i++) gaps.add(Math.round((hats[i] - hats[i - 1]) * 1e6))
out.steps = hats.length
out.gaps = [...gaps]
out.stepLen = Math.round(stepLen * 1e6)
out.ahead = Math.max.apply(null, hats) <= ctx.currentTime + 0.4 + 1e-9

// A hidden tab parks the timer. Coming back must not start the missed steps.
oscillators.length = 0; sources.length = 0
ctx.currentTime = 20
intervals.filter(Boolean)[0]()
out.caughtUp = Math.min.apply(null, sources.map((s) => s.at)) >= 20 - 1e-9

// Mood: a pad and a bass alone at 0, a lead at 0.4, drums at 1.
function voices(mood) {
  music = fresh(); handlers.keydown()
  music.mood = mood
  music.play(music.compose(5, { bpm: 120 }))
  const c = contexts[0]
  for (let t = 0; t < 8; t += 0.05) { c.currentTime = t; intervals.filter(Boolean)[0]() }
  return [oscillators.length, sources.length]
}
const calm = voices(0), mid = voices(0.4), full = voices(1)
out.mood = [calm[1], mid[1], full[1], calm[0] < mid[0], mid[0] < full[0]]

// Mute: the bus goes to zero, and back to the volume.
music = fresh(); handlers.keydown(); music.play(music.compose(5))
music.muted = true; music.level()
const bus = gains[0]
out.muted = bus.gain.target
music.muted = false; music.level()
out.unmuted = bus.gain.target

// Stop: the loop is gone, and play starts a fresh one.
music.stop()
out.stopped = live()
music.play()
out.restarted = live()

console.log(JSON.stringify(out))
`

func TestAudioReferenceCore(t *testing.T) {
	out := jsrun.Run(t, audioReference, audioHarness, "")
	t.Logf("%s", out)
	var got struct {
		Globals                          []string
		InScale, SameSeed, OtherSeed     bool
		Options                          []any
		LeadRests, LeadBreath, LeadRange bool
		BeforeGesture                    []int
		AfterGesture                     []any
		OneLoop, Steps                   int
		Gaps                             []int
		StepLen                          int
		Ahead, CaughtUp                  bool
		Mood                             []any
		Muted, Unmuted                   float64
		Stopped, Restarted               int
	}
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatalf("decode: %v\n%s", err, out)
	}
	if len(got.Globals) != 1 || got.Globals[0] != "music" {
		t.Errorf("the script leaks globals %v, want only music", got.Globals)
	}
	if !got.InScale {
		t.Error("a pad, bass or lead note is outside its scale")
	}
	if !got.SameSeed || !got.OtherSeed {
		t.Errorf(
			"same seed repeats %v, another seed differs %v; want both",
			got.SameSeed,
			got.OtherSeed,
		)
	}
	if len(got.Options) != 2 || got.Options[0] != "dorian" || got.Options[1] != 100.0 {
		t.Errorf("options gave %v, want dorian at 100", got.Options)
	}
	if !got.LeadRests || !got.LeadBreath || !got.LeadRange {
		t.Errorf("lead: 30%% rests %v, a rest ends every bar %v, within two octaves %v",
			got.LeadRests, got.LeadBreath, got.LeadRange)
	}
	if len(got.BeforeGesture) != 3 || got.BeforeGesture[0] != 0 || got.BeforeGesture[1] != 0 ||
		got.BeforeGesture[2] != 0 {
		t.Errorf("before a gesture: contexts, loops, voices %v, want all zero", got.BeforeGesture)
	}
	if len(got.AfterGesture) != 3 || got.AfterGesture[0] != 1.0 || got.AfterGesture[1] != 1.0 ||
		got.AfterGesture[2] != true {
		t.Errorf("after a gesture: contexts, loops, sounding %v, want 1, 1, true", got.AfterGesture)
	}
	if got.OneLoop != 1 {
		t.Errorf("play three times left %d loops, want 1", got.OneLoop)
	}
	if !got.CaughtUp {
		t.Error("after a hidden tab, a step was scheduled in the past instead of dropped")
	}
	if got.Steps < 12 || len(got.Gaps) != 1 || got.Gaps[0] != got.StepLen || !got.Ahead {
		t.Errorf("steps %d, gaps between them %v (want only %d), within the lookahead %v",
			got.Steps, got.Gaps, got.StepLen, got.Ahead)
	}
	if len(got.Mood) != 5 || got.Mood[0] != 0.0 || got.Mood[1] != 0.0 || got.Mood[2] == 0.0 ||
		got.Mood[3] != true || got.Mood[4] != true {
		t.Errorf(
			"mood: noise sources at 0, 0.4, 1 and voice growth %v, want 0, 0, more, true, true",
			got.Mood,
		)
	}
	if got.Muted != 0 || got.Unmuted <= 0 {
		t.Errorf(
			"mute set the bus to %v, unmute to %v; want 0 and the volume",
			got.Muted,
			got.Unmuted,
		)
	}
	if got.Stopped != 0 || got.Restarted != 1 {
		t.Errorf("after stop %d loops, after play %d; want 0 and 1", got.Stopped, got.Restarted)
	}
}
