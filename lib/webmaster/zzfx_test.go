package webmaster

import (
	"encoding/json"
	"path/filepath"
	"testing"

	"github.com/Napam/beaverdream/lib/webmaster/internal/jsrun"
)

// zzfxHarness imports the vendored ZzFX module with a fake AudioContext and
// counts what it makes and plays.
const zzfxHarness = `
const { pathToFileURL } = require('url')
;(async () => {
  const out = {}
  let contexts = 0, started = 0, resumed = 0
  const handlers = {}
  globalThis.addEventListener = (type, fn) => { handlers[type] = fn }
  globalThis.AudioContext = class {
    constructor() { contexts++; this.state = 'suspended'; this.destination = {} }
    resume() { resumed++; this.state = 'running' }
    createBuffer(ch, n) { const d = new Float32Array(n); return { getChannelData: () => d } }
    createBufferSource() { return { connect() {}, start() { started++ } } }
  }
  const z = await import(pathToFileURL(process.argv[3]).href)
  out.atImport = contexts

  z.zzfx(1, .05, 925)
  out.beforeGesture = [contexts, started]

  handlers.pointerdown()
  out.afterGesture = [contexts, resumed]
  z.zzfx(1, .05, 925)
  z.zzfx(1, .05, 440)
  out.played = [contexts, started]

  z.setVolume(0)
  out.exports = Object.keys(z).sort()
  console.log(JSON.stringify(out))
})().catch((e) => { console.error(e); process.exit(1) })
`

// The vendored synth makes no AudioContext at import, drops a call that comes
// before any gesture instead of queueing it, makes one context on the first
// gesture and reuses it.
func TestZzfxModuleWaitsForAGesture(t *testing.T) {
	path, err := filepath.Abs("../../web/vendor/zzfx-v1.4.0.module.js")
	if err != nil {
		t.Fatal(err)
	}
	out := jsrun.Run(t, "<script></script>", zzfxHarness, path)
	var got struct {
		AtImport      int
		BeforeGesture [2]int
		AfterGesture  [2]int
		Played        [2]int
		Exports       []string
	}
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatalf("decode: %v\n%s", err, out)
	}
	if got.AtImport != 0 || got.BeforeGesture != [2]int{0, 0} {
		t.Errorf("contexts at import %d, before a gesture %v; want none and nothing played",
			got.AtImport, got.BeforeGesture)
	}
	if got.AfterGesture != [2]int{1, 1} || got.Played != [2]int{1, 2} {
		t.Errorf(
			"after a gesture contexts and resumes %v, then contexts and plays %v; want [1 1] and [1 2]",
			got.AfterGesture,
			got.Played,
		)
	}
	if len(got.Exports) < 3 {
		t.Errorf("exports %v, want zzfx, ctx and setVolume", got.Exports)
	}
}
