package webmaster

import "time"

// Model loop: how much one completion may write, and how the loop answers a
// provider that misbehaves.

// maxOutputTokens caps a single completion. Reasoning models count their
// thinking against this cap, so a small one can leave no room for the tool
// call and the turn ends silent with finish_reason "length". A page or a
// stylesheet needs much less, and an unused cap costs nothing.
const maxOutputTokens = 131072

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

// Page and state limits.

// The size caps exist to stop a runaway write, not to keep the page small. A
// page with a game in it passes 20k bytes, so the cap leaves room for the
// page to keep growing before a whole rewrite is refused.
const (
	maxCSSLen     = 64000
	maxHTMLLen    = 192000
	maxStateKeys  = 8
	maxStateName  = 32
	maxStateList  = 100
	maxStateOpts  = 10
	maxStateValue = 200
	// maxReasonLen is the longest done reason kept. The done tool says so, so
	// the model fits its sentence instead of being cut off.
	maxReasonLen   = 200
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

// Turn kinds. Posts and webmaster turns alternate; the rest are events the
// server writes so the webmaster's memory matches the page.
const (
	TurnPost      = "post"
	TurnWebmaster = "webmaster"
	TurnHeal      = "heal"
	TurnUndo      = "undo"
	TurnRestore   = "restore"
	// TurnJournal is the admin clearing one journal key. The page's data
	// changed under the webmaster, so the room log says so.
	TurnJournal = "journal"
	// TurnAlert is a browser error the page reported about itself, so the
	// webmaster can see that its own script broke.
	TurnAlert = "alert"
)

// roomLogBudget bounds the room log in runes. It matches the 128k-token
// compaction threshold at four runes per token. The oldest lines are dropped
// first: the newest conversation is what the next turn has to continue.
const roomLogBudget = 512_000

// The summarizer's caps. A summary folds up to a chunk of 1000 turns, and one
// compaction can follow a transcript of 128k tokens. 6000 characters (about
// 1500 tokens) is room for the promises, grudges and running jokes of that
// much conversation, and still small next to the page and the log that follow
// it. The cap applies to the text stored per row, and each chained summary
// starts from the previous one, so the memory never grows past it: older
// detail fades by design. summaryMaxTokens leaves headroom over the character
// cap.
const (
	summaryMaxTokens = 2000
	summaryMaxLen    = 6000
)

// MaxIdentityLen caps a display name in runes.
const MaxIdentityLen = 32
