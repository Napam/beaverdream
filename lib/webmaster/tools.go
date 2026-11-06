package webmaster

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"unicode"

	"github.com/Napam/beaverdream/lib/webmaster/skills"
)

// StateKey is one model-declared entry in the state schema. Op decides how a
// visitor may change it; the handler owns the actual application.
//
// A counter with Scope "visitor" is a count each visitor owns. Max is then
// the most one visitor can hold and PerHour the most one visitor can gain in
// a rolling hour; 0 means the ceiling. A "recipe" is not a value at all: it
// trades the Cost counts for the Gain counts in one step.
type StateKey struct {
	Op      string           `json:"op"`
	Scope   string           `json:"scope,omitempty"`
	Label   string           `json:"label,omitempty"`
	Max     int64            `json:"max,omitempty"`
	PerHour int64            `json:"per_hour,omitempty"`
	Options []string         `json:"options,omitempty"`
	Cost    map[string]int64 `json:"cost,omitempty"`
	Gain    map[string]int64 `json:"gain,omitempty"`
}

// ScopeVisitor marks a counter that every visitor holds on their own.
const ScopeVisitor = "visitor"

// Visitor reports a per-visitor counter.
func (k StateKey) Visitor() bool { return k.Op == "counter" && k.Scope == ScopeVisitor }

// Global reports a value everyone shares: a counter, list or choice.
func (k StateKey) Global() bool { return !k.Visitor() && k.Op != "recipe" }

// HoldingCap is the most one visitor can hold of a visitor counter.
func (k StateKey) HoldingCap() int64 {
	if k.Max <= 0 || k.Max > MaxVisitorHolding {
		return MaxVisitorHolding
	}
	return k.Max
}

// HourCap is the most one visitor can gain of a visitor counter in an hour.
func (k StateKey) HourCap() int64 {
	if k.PerHour <= 0 || k.PerHour > MaxVisitorPerHour {
		return MaxVisitorPerHour
	}
	return k.PerHour
}

// ParseStateSchema reads a stored state schema. A broken one reads as
// empty, so a bad row cannot open writes to undeclared keys.
func ParseStateSchema(raw string) map[string]StateKey {
	schema := make(map[string]StateKey)
	if strings.TrimSpace(raw) == "" {
		return schema
	}
	if err := json.Unmarshal([]byte(raw), &schema); err != nil {
		return make(map[string]StateKey)
	}
	return schema
}

type stateKeyInput struct {
	Name    string           `json:"name"`
	Op      string           `json:"op"`
	Scope   string           `json:"scope"`
	Label   string           `json:"label"`
	Max     int64            `json:"max"`
	PerHour int64            `json:"per_hour"`
	Options []string         `json:"options"`
	Cost    map[string]int64 `json:"cost"`
	Gain    map[string]int64 `json:"gain"`
}

// editor applies tool calls to a copy of the site. Nothing reaches the
// database until the whole mutation finishes, so a failed round cannot leave a
// half-written page behind.
type editor struct {
	site   Site
	reason string
	reply  string
	notes  *string
	done   bool
	dirty  bool
	// rejected counts rejected calls. The loop resets it before each
	// response.
	rejected int
	// badArguments counts calls whose arguments did not parse, so the loop
	// can tell a round of broken tool markup from a round of wrong edits.
	badArguments int
	// loaded names the skills this turn already returned. Their bodies stay
	// in the conversation, so a second load only points back at them.
	loaded map[string]bool
}

func newEditor(site Site) *editor {
	return &editor{site: site, loaded: map[string]bool{}}
}

// run applies one tool call and returns the text fed back to the model. It
// never returns a Go error: a rejected argument is a normal result the model
// can correct on the next round. Rejections are counted so the loop can tell
// whether a response that called done still has something to fix.
func (e *editor) run(name, arguments string) string {
	result := e.apply(name, arguments)
	if strings.HasPrefix(result, "rejected:") {
		e.rejected++
	}
	return result
}

// argumentsRejected is what a call whose arguments did not parse gets back.
// Broken JSON is a provider failure, so it counts toward the loop's cap: an
// all-bad round is resampled, and a broken call in a mixed round is replayed
// as an empty object. Valid JSON of the wrong shape is a normal model mistake
// the model can fix from the message.
func (e *editor) argumentsRejected(arguments string, err error) string {
	if json.Valid([]byte(arguments)) {
		return "rejected: arguments do not match the schema (" + err.Error() + ")"
	}
	e.badArguments++
	return "rejected: arguments are not valid JSON and were dropped; send them again as one JSON object"
}

func (e *editor) apply(name, arguments string) string {
	if strings.Contains(arguments, providerToolMarkup) {
		e.badArguments++
		return "rejected: the call carried tool markup, not plain JSON, and was dropped; send it again as one JSON object"
	}
	switch name {
	case "set_site":
		var in struct {
			CSS  *string `json:"css"`
			HTML *string `json:"html"`
		}
		if err := json.Unmarshal([]byte(arguments), &in); err != nil {
			return e.argumentsRejected(arguments, err)
		}
		if in.CSS == nil && in.HTML == nil {
			return "rejected: set_site needs css, html or both"
		}
		// Both fields are cleaned before either is assigned, so a rejected
		// call leaves the whole page as it was.
		css, page := e.site.CSS, e.site.HTML
		var err error
		if in.CSS != nil {
			if css, err = cleanCSS(*in.CSS); err != nil {
				return "rejected: " + err.Error() + "; nothing was applied"
			}
		}
		if in.HTML != nil {
			if page, err = cleanHTML(*in.HTML); err != nil {
				return "rejected: " + err.Error() + "; nothing was applied"
			}
		}
		if err := checkPageSize(css, page); err != nil {
			return "rejected: " + err.Error() + "; nothing was applied"
		}
		changed := css != e.site.CSS || page != e.site.HTML
		e.site.CSS, e.site.HTML = css, page
		if !changed {
			return "ok (no change)"
		}
		e.dirty = true
		return "ok"

	case "edit":
		var in struct {
			Target string `json:"target"`
			Old    string `json:"old"`
			New    string `json:"new"`
		}
		if err := json.Unmarshal([]byte(arguments), &in); err != nil {
			return e.argumentsRejected(arguments, err)
		}
		return e.edit(in.Target, in.Old, in.New)

	case "set_state_schema":
		var in struct {
			Keys []stateKeyInput `json:"keys"`
		}
		if err := json.Unmarshal([]byte(arguments), &in); err != nil {
			return e.argumentsRejected(arguments, err)
		}
		schema, err := cleanStateSchema(in.Keys)
		if err != nil {
			return "rejected: " + err.Error()
		}
		e.site.StateSchema = schema
		e.dirty = true
		return "ok"

	case "set_journal_schema":
		var in struct {
			Keys []journalKeyInput `json:"keys"`
		}
		if err := json.Unmarshal([]byte(arguments), &in); err != nil {
			return e.argumentsRejected(arguments, err)
		}
		// An omitted rule or max keeps the declared value. Otherwise adding a
		// second key would silently reset the first one and replay its rows.
		// A new rules number skips past every number stored rows carry, so a
		// restore or a key declared again cannot replay an older reducer's
		// rows.
		current := ParseJournalSchema(e.site.JournalSchema)
		var raised []string
		for i := range in.Keys {
			name := strings.TrimSpace(in.Keys[i].Name)
			old, ok := current[name]
			used := e.site.JournalRulesUsed[name]
			rules, bumped, err := journalRules(in.Keys[i].Rules, old, ok, used)
			if err != nil {
				return fmt.Sprintf("rejected: rules for %q %s", name, err.Error())
			}
			if bumped {
				raised = append(raised, fmt.Sprintf(
					"rules for %q raised to %d, because stored rows already carry %d",
					name, rules, used,
				))
			}
			in.Keys[i].Rules = rules
			if ok && in.Keys[i].Max <= 0 {
				in.Keys[i].Max = old.Max
			}
		}
		schema, err := cleanJournalSchema(in.Keys)
		if err != nil {
			return "rejected: " + err.Error()
		}
		e.site.JournalSchema = schema
		e.dirty = true
		if len(raised) > 0 {
			return "ok (" + strings.Join(raised, "; ") + ")"
		}
		return "ok"

	case "load_skill":
		var in struct {
			Name string `json:"name"`
		}
		if err := json.Unmarshal([]byte(arguments), &in); err != nil {
			return e.argumentsRejected(arguments, err)
		}
		body, ok := skills.Body(in.Name)
		if !ok {
			return fmt.Sprintf(
				"rejected: no skill %q; the skills are %s",
				in.Name, strings.Join(skills.Names(), ", "),
			)
		}
		if e.loaded[in.Name] {
			return fmt.Sprintf(
				"ok: %s was already loaded this turn; use the earlier result",
				in.Name,
			)
		}
		e.loaded[in.Name] = true
		return body

	case "done":
		var in struct {
			Reason string  `json:"reason"`
			Reply  string  `json:"reply"`
			Notes  *string `json:"notes"`
		}
		if err := json.Unmarshal([]byte(arguments), &in); err != nil {
			return e.argumentsRejected(arguments, err)
		}
		e.done = true
		e.reason = truncateRunes(strings.TrimSpace(in.Reason), maxReasonLen)
		// A deferred done may be called again after a fix without repeating
		// its reply, so an empty reply keeps the one already given.
		if reply := strings.TrimSpace(in.Reply); reply != "" {
			e.reply = truncateRunes(reply, maxReplyLen)
		}
		if in.Notes != nil {
			notes := truncateRunes(strings.TrimSpace(*in.Notes), maxNotesLen)
			e.notes = &notes
		}
		return "ok"
	}

	return fmt.Sprintf("rejected: unknown tool %q", name)
}

// edit changes part of one field of the shared page. The result goes through
// the same cleaning as a whole-field write, so an edit cannot produce a page
// that set_site would refuse. A rejected edit leaves the field as it was.
func (e *editor) edit(target, old, replacement string) string {
	var field *string
	var section string
	switch target {
	case targetCSS:
		field, section = &e.site.CSS, "CURRENT STYLESHEET"
	case targetHTML:
		field, section = &e.site.HTML, "CURRENT HTML"
	default:
		return fmt.Sprintf("rejected: unknown target %q, use css or html", target)
	}

	next, matches := applyEdit(*field, old, replacement)
	switch {
	case matches == 0:
		return fmt.Sprintf(
			"rejected: old text not found in %s. Copy it exactly from %s, "+
				`use a shorter piece, or use old="" to append.`,
			target, section,
		)
	case matches > 1:
		return fmt.Sprintf(
			"rejected: old text matches %d places in %s. "+
				"Include more surrounding text so it matches once.",
			matches, target,
		)
	}

	var err error
	if target == targetCSS {
		next, err = cleanCSS(next)
	} else {
		next, err = cleanHTML(next)
	}
	if err == nil {
		if target == targetCSS {
			err = checkPageSize(next, e.site.HTML)
		} else {
			err = checkPageSize(e.site.CSS, next)
		}
	}
	if err != nil {
		return "rejected: " + err.Error() + "; the edit was not applied"
	}
	if next == *field {
		return "ok (no change)"
	}
	*field = next
	e.dirty = true
	return "ok"
}

// applyEdit replaces the one place old appears in field and reports how many
// places matched; the returned text is only meaningful when that is 1. An
// empty old appends replacement on a new line.
//
// Models often copy a section with different indentation, line breaks or
// spacing, so matching tries three steps and stops at the first that finds
// anything: the exact text, then any whitespace between words, then optional
// whitespace around { } : ; , and >. Each step matches a superset of the one
// before, so stopping early never turns an ambiguous match into a unique one.
func applyEdit(field, old, replacement string) (string, int) {
	if strings.TrimSpace(old) == "" {
		if strings.TrimSpace(field) == "" {
			return replacement, 1
		}
		return field + "\n" + replacement, 1
	}

	if matches := strings.Count(field, old); matches > 0 {
		if matches > 1 {
			return field, matches
		}
		return strings.Replace(field, old, replacement, 1), 1
	}

	for _, pattern := range []string{wordPattern(old), punctuationPattern(old)} {
		spans := regexp.MustCompile(pattern).FindAllStringIndex(field, -1)
		switch len(spans) {
		case 0:
			continue
		case 1:
			start, end := spans[0][0], spans[0][1]
			return field[:start] + replacement + field[end:], 1
		default:
			return field, len(spans)
		}
	}
	return field, 0
}

// wordPattern matches old with any run of whitespace between its words.
func wordPattern(old string) string {
	words := strings.Fields(old)
	for i, word := range words {
		words[i] = regexp.QuoteMeta(word)
	}
	return strings.Join(words, `\s+`)
}

// editPunctuation is where CSS and markup spacing is a matter of taste.
const editPunctuation = "{}:;,>"

// punctuationPattern is wordPattern that also lets whitespace appear or vanish
// around editPunctuation, so `a { color: red }` matches `a{color:red}`.
func punctuationPattern(old string) string {
	var b strings.Builder
	space := false
	afterPunct := true
	for _, r := range strings.TrimSpace(old) {
		switch {
		case unicode.IsSpace(r):
			space = true
		case strings.ContainsRune(editPunctuation, r):
			b.WriteString(`\s*`)
			b.WriteString(regexp.QuoteMeta(string(r)))
			b.WriteString(`\s*`)
			space, afterPunct = false, true
		default:
			if space && !afterPunct {
				b.WriteString(`\s+`)
			}
			b.WriteString(regexp.QuoteMeta(string(r)))
			space, afterPunct = false, false
		}
	}
	// Whitespace at the ends belongs to the neighbors, not to the match, or a
	// replaced rule would swallow the line break after it.
	pattern := strings.TrimPrefix(b.String(), `\s*`)
	return strings.TrimSuffix(pattern, `\s*`)
}

// cleanHTML checks the page body. The posts placeholder is the one structural
// rule: everything around it is the model's. It must appear exactly once: a
// second copy would make a second post list with the same ids, and the live
// stream only patches the first.
func cleanHTML(html string) (string, error) {
	html = strings.TrimSpace(html)
	switch strings.Count(html, PostsPlaceholder) {
	case 0:
		return "", fmt.Errorf("html must contain %s, where the posts go", PostsPlaceholder)
	case 1:
	default:
		return "", fmt.Errorf(
			"html must contain %s exactly once, not in a comment or a script string too",
			PostsPlaceholder,
		)
	}
	return html, nil
}

// cleanStateSchema validates the model's state keys and recipes and stores
// them as one compact JSON object. Only declared keys can be written, so a
// visitor cannot invent state the model did not ask for.
func cleanStateSchema(keys []stateKeyInput) (string, error) {
	if len(keys) > maxStateKeys {
		return "", fmt.Errorf("at most %d state keys and recipes together", maxStateKeys)
	}

	schema := make(map[string]StateKey, len(keys))
	for _, input := range keys {
		name := strings.TrimSpace(input.Name)
		if !validStateName(name) {
			return "", fmt.Errorf("bad state key %q: use lowercase letters, digits, _ or -", name)
		}
		if _, exists := schema[name]; exists {
			return "", fmt.Errorf("duplicate state key %q", name)
		}
		scope := strings.TrimSpace(input.Scope)
		if scope != "" && scope != "global" && scope != ScopeVisitor {
			return "", fmt.Errorf("state key %q: scope is global or visitor", name)
		}
		if scope == ScopeVisitor && input.Op != "counter" {
			return "", fmt.Errorf("state key %q: only a counter can have scope visitor", name)
		}

		key := StateKey{Op: input.Op}
		switch input.Op {
		case "counter":
			if scope == ScopeVisitor {
				key.Scope = ScopeVisitor
				key.Label = truncateRunes(CleanStateValue(input.Label), maxStateLabel)
				key.Max = clampPositive(input.Max, MaxVisitorHolding)
				key.PerHour = clampPositive(input.PerHour, MaxVisitorPerHour)
			}
		case "list":
			key.Max = input.Max
			if key.Max <= 0 {
				key.Max = defaultListMax
			}
			if key.Max > maxStateList {
				key.Max = maxStateList
			}
		case "choice":
			if len(input.Options) < minStateOpts || len(input.Options) > maxStateOpts {
				return "", fmt.Errorf("choice %q needs 2 to %d options", name, maxStateOpts)
			}
			for _, option := range input.Options {
				option = CleanStateValue(option)
				if option == "" {
					return "", fmt.Errorf("choice %q has an empty option", name)
				}
				key.Options = append(key.Options, option)
			}
		case "recipe":
			key.Cost = input.Cost
			key.Gain = input.Gain
		default:
			return "", fmt.Errorf(
				"unknown state op %q, use counter, list, choice or recipe", input.Op)
		}
		schema[name] = key
	}

	// Recipes name visitor counters, which may be declared after them.
	for name, key := range schema {
		if key.Op != "recipe" {
			continue
		}
		if err := checkRecipeSide(schema, name, "cost", key.Cost); err != nil {
			return "", err
		}
		if err := checkRecipeSide(schema, name, "gain", key.Gain); err != nil {
			return "", err
		}
		for item := range key.Cost {
			if _, ok := key.Gain[item]; ok {
				return "", fmt.Errorf("recipe %q has %q in both cost and gain", name, item)
			}
		}
	}

	data, err := json.Marshal(schema)
	if err != nil {
		return "", fmt.Errorf("encode state schema: %w", err)
	}
	return string(data), nil
}

func checkRecipeSide(
	schema map[string]StateKey,
	recipe, side string,
	items map[string]int64,
) error {
	if len(items) < 1 || len(items) > maxRecipeSide {
		return fmt.Errorf("recipe %q needs 1 to %d entries in %s", recipe, maxRecipeSide, side)
	}
	for item, amount := range items {
		if !schema[item].Visitor() {
			return fmt.Errorf(
				"recipe %q %s names %q, which is not a counter with scope visitor",
				recipe, side, item)
		}
		if amount < 1 || amount > MaxVisitorAmount {
			return fmt.Errorf(
				"recipe %q %s for %q must be 1 to %d", recipe, side, item, MaxVisitorAmount)
		}
	}
	return nil
}

// clampPositive keeps 0 for "the ceiling" and clamps anything else into
// 1..ceiling.
func clampPositive(value, ceiling int64) int64 {
	if value <= 0 {
		return 0
	}
	return min(value, ceiling)
}

func validStateName(name string) bool {
	if name == "" || len([]rune(name)) > maxStateName {
		return false
	}
	for i, r := range name {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
		case (r == '_' || r == '-') && i > 0:
		default:
			return false
		}
	}
	return true
}

// CleanStateValue keeps visitor state to safe text. The page is trusted to
// display it as text, but stripping angle brackets keeps a careless script
// from turning state into stored HTML.
func CleanStateValue(value string) string {
	value = strings.Map(func(r rune) rune {
		switch r {
		case '<', '>', '"', '\'', '`', 0:
			return -1
		}
		return r
	}, value)
	value = strings.Join(strings.Fields(value), " ")
	return truncateRunes(value, maxStateValue)
}

func cleanCSS(css string) (string, error) {
	return strings.TrimSpace(css), nil
}

// checkPageSize enforces the one cap that covers both fields. The error names
// the overage so the model knows how much to remove instead of guessing.
func checkPageSize(css, html string) error {
	total := len(css) + len(html)
	if total <= maxPageLen {
		return nil
	}
	return fmt.Errorf(
		"page is %d bytes, over the %d cap; remove at least %d bytes",
		total, maxPageLen, total-maxPageLen,
	)
}

func truncateRunes(s string, limit int) string {
	runes := []rune(s)
	if len(runes) <= limit {
		return s
	}
	return string(runes[:limit])
}
