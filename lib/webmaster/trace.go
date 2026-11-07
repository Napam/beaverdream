package webmaster

import (
	"errors"
	"sync"
	"time"

	"github.com/Napam/beaverdream/lib/env"
)

// errStalled is the cause a round's context carries when its stream went
// quiet for longer than the idle limit for its phase.
var errStalled = errors.New("the model stream went quiet")

// IdleLimits is how long a round's stream may add nothing before it counts as
// stalled. The timer resets whenever the stream adds text, reasoning or tool
// call bytes, so a slow stream that keeps moving is never cut. A limit of zero
// turns that phase's check off. The numbers are generous on purpose: a long
// reasoning pass on a big prompt can go quiet for a while.
type IdleLimits struct {
	// First covers the wait from sending the request to the first event.
	First time.Duration
	// Think covers reasoning and prose, up to the first tool call.
	Think time.Duration
	// Call covers the stretch after a tool call's name has arrived.
	Call time.Duration
}

// DefaultIdleLimits are the limits a ChangesetRunner starts with. LLM_IDLE_TIMEOUT sets
// all three phases to one value, for a provider that is slower than these; a
// zero turns the checks off.
var DefaultIdleLimits = idleLimitsFromEnv()

func idleLimitsFromEnv() IdleLimits {
	limit := env.GetEnvDuration("LLM_IDLE_TIMEOUT", -1)
	if limit < 0 {
		return IdleLimits{First: 3 * time.Minute, Think: 3 * time.Minute, Call: 2 * time.Minute}
	}
	return IdleLimits{First: limit, Think: limit, Call: limit}
}

// RoundTrace is what one round's stream looked like from the client, kept so a
// stall can be diagnosed from the admin panel. It is stored beside the round's
// stats and never in the message text: Reasoning goes back to the provider and
// Content can become the turn's reason, so raw provider text must stay out of
// both.
type RoundTrace struct {
	// Attempts counts HTTP attempts, so a retry before the stream is visible.
	Attempts int `json:"attempts,omitempty"`
	Status   int `json:"status,omitempty"`
	// Headers is the time from the request to the response headers.
	Headers   time.Duration `json:"headers_ns,omitempty"`
	RequestID string        `json:"request_id,omitempty"`
	// Model, Fingerprint and ChunkID come from the stream's chunks. A change in
	// Model or Fingerprint between rounds means another backend answered.
	Model       string `json:"model,omitempty"`
	Fingerprint string `json:"fingerprint,omitempty"`
	ChunkID     string `json:"chunk_id,omitempty"`
	// ReasoningTokens is the part of the completion tokens spent thinking.
	// Completion minus this is what the model wrote outside its thinking.
	ReasoningTokens int64 `json:"reasoning_tokens,omitempty"`
	// Events counts chunks by what they carried.
	Events TraceEvents `json:"events"`
	// OtherChoices, FunctionCalls and UnknownFields count what complete does
	// not read, so a provider that put arguments somewhere else shows up.
	OtherChoices  int      `json:"other_choices,omitempty"`
	FunctionCalls int      `json:"function_calls,omitempty"`
	UnknownFields []string `json:"unknown_fields,omitempty"`
	// FirstEvent and LastEvent are offsets from the start of the request. Name
	// is when the first tool call's name arrived.
	FirstEvent time.Duration `json:"first_event_ns,omitempty"`
	LastEvent  time.Duration `json:"last_event_ns,omitempty"`
	Name       time.Duration `json:"name_ns,omitempty"`
	// Total is the offset at which the stream ended.
	Total time.Duration `json:"total_ns,omitempty"`
	// LongestGap is the longest stretch with no growth, including the one
	// between the last event and the end of the stream. GapPhase names what
	// the stream was doing when that gap began: waiting, reasoning, content or
	// call. The silence before the stream closed counts as a gap too.
	LongestGap time.Duration `json:"longest_gap_ns,omitempty"`
	GapPhase   string        `json:"gap_phase,omitempty"`
	// Finished is true when a finish reason arrived.
	Finished bool `json:"finished"`
	// Stalled is set when the watchdog cut the round, with the phase it was
	// in and the limit it ran past.
	Stalled    bool          `json:"stalled,omitempty"`
	StallPhase string        `json:"stall_phase,omitempty"`
	StallLimit time.Duration `json:"stall_limit_ns,omitempty"`
	// PromptRunes is the size of the request's messages, so prompt tokens over
	// runes shows the real ratio.
	PromptRunes int `json:"prompt_runes,omitempty"`
	// Head and Tail are the first two and last twenty raw chunks, clipped.
	Head []string `json:"head,omitempty"`
	Tail []string `json:"tail,omitempty"`
}

// TraceEvents counts the chunks of one round by what they carried.
type TraceEvents struct {
	Reasoning int `json:"reasoning"`
	Content   int `json:"content"`
	ToolName  int `json:"tool_name"`
	ToolArgs  int `json:"tool_args"`
	UsageOnly int `json:"usage_only"`
	Empty     int `json:"empty"`
}

const (
	traceHeadChunks = 2
	traceTailChunks = 20
	traceChunkRunes = 360
)

// keep files one raw chunk: the first few and a rolling tail.
func (t *RoundTrace) keep(seen int, raw string) {
	raw = clip(raw, traceChunkRunes)
	if seen < traceHeadChunks {
		t.Head = append(t.Head, raw)
		return
	}
	if len(t.Tail) == traceTailChunks {
		copy(t.Tail, t.Tail[1:])
		t.Tail = t.Tail[:traceTailChunks-1]
	}
	t.Tail = append(t.Tail, raw)
}

// watchdog calls fire once the timer runs out. reset restarts it with a new
// limit; stop ends it for good.
type watchdog struct {
	mu      sync.Mutex
	timer   *time.Timer
	fire    func()
	stopped bool
}

func newWatchdog(fire func()) *watchdog { return &watchdog{fire: fire} }

// reset restarts the timer with a new limit. A limit of zero turns the check
// off, including a timer an earlier phase left running.
func (w *watchdog) reset(limit time.Duration) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.stopped {
		return
	}
	if limit <= 0 {
		if w.timer != nil {
			w.timer.Stop()
		}
		return
	}
	if w.timer == nil {
		w.timer = time.AfterFunc(limit, w.fire)
		return
	}
	w.timer.Reset(limit)
}

func (w *watchdog) stop() {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.stopped = true
	if w.timer != nil {
		w.timer.Stop()
	}
}
