package webmaster

import (
	"time"

	"github.com/Napam/beaverdream/lib/env"
)

// Model loop: how much one completion may write, and how the loop answers a
// provider that misbehaves.

// maxOutputTokens caps a single completion. Reasoning models count their
// thinking against this cap, so a small one can leave no room for the tool
// call and the turn ends silent with finish_reason "length". The page can be
// larger than one completion, so edits, not set_site, are how a big page
// grows. An unused cap costs nothing. Providers differ, so LLM_MAX_OUTPUT_TOKENS
// overrides it.
var maxOutputTokens = env.GetEnvPositiveInt("LLM_MAX_OUTPUT_TOKENS", 250_000)

// maxBrokenArgumentRounds stops a provider that answers round after round
// with tool calls whose arguments are not JSON. Such a round is not a wrong
// edit the model can see and fix: the call arrived as raw tool markup.
const maxBrokenArgumentRounds = 5

// maxStallStreak ends a turn whose rounds keep stalling or ending without a
// finish reason, one after another. A stall is the provider's fault, so the
// turn stops with an error and the page stays as it was.
const maxStallStreak = 3

// defaultStallPause is the wait before a stalled round is sent again, plus up
// to as much again in jitter, so a retry is not a hammer.
const defaultStallPause = 2 * time.Second

// Prompt, page and state limits.

// CompactAfterTokens is the prompt budget in estimated tokens at four runes
// per token. It covers the whole prompt, transcript plus page, so the server
// compacts the history once the two together reach it. It depends on the
// model's context window, so COMPACT_AT_PROMPT_TOKENS overrides it.
var CompactAfterTokens = env.GetEnvPositiveInt("COMPACT_AT_PROMPT_TOKENS", 200_000)

// compactPageShare is the page's share of the prompt budget, in thirds. The
// page is part of the same prompt as the transcript, so its cap is a slice of
// that budget, not a separate number. Two thirds leaves the rest for history.
const compactPageShare = 2

// maxPageLen caps css plus html together in bytes. It exists to stop a runaway
// write, not to keep the page small. Four bytes per token times the page's
// share of CompactAfterTokens.
var maxPageLen = int(int64(CompactAfterTokens) * 4 * compactPageShare / 3)

// PageBytes is the cap on css plus html together, in bytes.
func PageBytes() int { return maxPageLen }

// roomLogBudget is the backstop above the compaction threshold, in runes. It
// is the whole budget at four runes per token, so the log never drops a turn
// before compaction folds it in. The oldest lines are dropped first: the
// newest conversation is what the next turn has to continue.
var roomLogBudget = 4 * CompactAfterTokens

// Visitor counters are whole numbers. One op moves at most MaxVisitorAmount,
// one visitor gains at most MaxVisitorPerHour of a key in a rolling hour, and
// holds at most MaxVisitorHolding, which stays inside a JS safe integer.
const (
	MaxVisitorAmount  = 1_000_000
	MaxVisitorPerHour = 1_000_000
	MaxVisitorHolding = 1_000_000_000_000
)

// MaxBodyLen caps a post body and an admin instruction in runes. The prompt's
// form example and the seed form use the same number.
const MaxBodyLen = 2000

const (
	maxStateKeys  = 24
	maxStateLabel = 40
	maxRecipeSide = 4
	maxStateName  = 32
	maxStateList  = 100
	maxStateOpts  = 10
	maxStateValue = 200
	// maxReasonLen is the longest done reason kept. The done tool says so, so
	// the model fits its sentence instead of being cut off.
	maxReasonLen = 200
	// maxReplyLen is the longest reply kept. A real answer to a
	// question needs a paragraph or two; a post may be MaxBodyLen.
	maxReplyLen = 1500
	// maxSayLen is the longest say kept: a quick word before the work, not
	// an answer.
	maxSayLen      = 300
	minStateOpts   = 2
	defaultListMax = 20
	maxNotesLen    = 1000
)

// providerToolMarkup is DeepSeek's native tool-call delimiter, U+FF5C on each
// side of DSML. A provider can leak it into the arguments; no call has a
// reason to carry it, and applying it would put the markup in the page.
const providerToolMarkup = "\uff5cDSML\uff5c"

// The two fields the model writes, as edit targets.
const (
	targetCSS  = "css"
	targetHTML = "html"
)

// Room and memory.

// Turn kinds. Posts and changesets alternate; the rest are events the server
// writes so the webmaster's memory matches the page.
const (
	TurnPost      = "post"
	TurnChangeset = "changeset"
	// TurnReply is the webmaster's answer in the room, written by the
	// finish transaction of the turn that said it.
	TurnReply   = "reply"
	TurnHeal    = "heal"
	TurnUndo    = "undo"
	TurnRestore = "restore"
	// TurnJournal is the admin clearing one journal key. The page's data
	// changed under the webmaster, so the room log says so.
	TurnJournal = "journal"
	// TurnState is the admin clearing what visitors hold of one key.
	TurnState = "state"
	// TurnAlert is a browser error the page reported about itself, so the
	// webmaster can see that its own script broke.
	TurnAlert = "alert"
)

// The summarizer's caps. A summary folds one chunk of turns, and one
// compaction can follow a transcript as large as the prompt budget.
// summaryMaxLen is 6000 characters (about 1500 tokens), room for the
// promises, grudges and running jokes of that much conversation, and still
// small next to the page and the log that follow it. The cap applies to the
// text stored per row, and each chained summary starts from the previous one,
// so the memory never grows past it: older detail fades by design.
// summaryMaxTokens leaves headroom over the character cap.
const (
	summaryMaxLen    = 6000
	summaryMaxTokens = summaryMaxLen / 3
)

// MaxIdentityLen caps a display name in runes.
const MaxIdentityLen = 32
