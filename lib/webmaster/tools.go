package webmaster

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"unicode"
)

const (
	maxCSSLen      = 20000
	maxRegionLen   = 20000
	minRegionLen   = 8
	maxStateKeys   = 8
	maxStateName   = 32
	maxStateList   = 100
	maxStateOpts   = 10
	maxStateValue  = 200
	minStateOpts   = 2
	defaultListMax = 20
	maxNotesLen    = 1000
)

// StateKey is one model-declared shared value. Op decides how a visitor may
// change it; the handler owns the actual application.
type StateKey struct {
	Op      string   `json:"op"`
	Max     int      `json:"max,omitempty"`
	Options []string `json:"options,omitempty"`
}

type stateKeyInput struct {
	Name    string   `json:"name"`
	Op      string   `json:"op"`
	Max     int      `json:"max"`
	Options []string `json:"options"`
}

// editor applies tool calls to a copy of the site. Nothing reaches the
// database until the whole mutation finishes, so a failed round cannot leave a
// half-written page behind.
type editor struct {
	site   Site
	reason string
	notes  *string
	done   bool
	dirty  bool
	// rejected counts rejected calls. The loop resets it before each
	// response.
	rejected int
}

func newEditor(site Site) *editor {
	return &editor{site: site}
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

func (e *editor) apply(name, arguments string) string {
	switch name {
	case "set_site":
		var in struct {
			CSS    *string `json:"css"`
			Header *string `json:"header"`
			Board  *string `json:"board"`
			Footer *string `json:"footer"`
		}
		if err := json.Unmarshal([]byte(arguments), &in); err != nil {
			return "rejected: arguments are not valid JSON"
		}
		if in.CSS == nil && in.Header == nil && in.Board == nil && in.Footer == nil {
			return "rejected: set_site needs at least one field"
		}
		// Every field is cleaned before any is assigned, so a rejected call
		// leaves the whole page as it was.
		fields := []struct {
			name   string
			value  *string
			target *string
		}{
			{editTargetCSS, in.CSS, &e.site.CSS},
			{RegionHeader, in.Header, &e.site.Header},
			{RegionBoard, in.Board, &e.site.Board},
			{RegionFooter, in.Footer, &e.site.Footer},
		}
		cleaned := make([]string, len(fields))
		for i, field := range fields {
			if field.value == nil {
				continue
			}
			var err error
			if field.name == editTargetCSS {
				cleaned[i], err = cleanCSS(*field.value)
			} else {
				cleaned[i], err = cleanRegion(field.name, *field.value)
			}
			if err != nil {
				return "rejected: " + err.Error() + "; nothing was applied"
			}
		}
		changed := false
		for i, field := range fields {
			if field.value == nil || cleaned[i] == *field.target {
				continue
			}
			*field.target = cleaned[i]
			changed = true
		}
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
			return "rejected: arguments are not valid JSON"
		}
		return e.edit(in.Target, in.Old, in.New)

	case "set_state_schema":
		var in struct {
			Keys []stateKeyInput `json:"keys"`
		}
		if err := json.Unmarshal([]byte(arguments), &in); err != nil {
			return "rejected: arguments are not valid JSON"
		}
		schema, err := cleanStateSchema(in.Keys)
		if err != nil {
			return "rejected: " + err.Error()
		}
		e.site.StateSchema = schema
		e.dirty = true
		return "ok"

	case "done":
		var in struct {
			Reason string  `json:"reason"`
			Notes  *string `json:"notes"`
		}
		_ = json.Unmarshal([]byte(arguments), &in)
		e.done = true
		e.reason = truncateRunes(strings.TrimSpace(in.Reason), 200)
		if in.Notes != nil {
			notes := truncateRunes(strings.TrimSpace(*in.Notes), maxNotesLen)
			e.notes = &notes
		}
		return "ok"
	}

	return fmt.Sprintf("rejected: unknown tool %q", name)
}

// editTargetCSS names the site stylesheet as an edit target. The regions use
// their own names.
const editTargetCSS = "css"

// edit changes part of one field of the shared page. The result goes through
// the same cleaning as a whole-field write, so an edit cannot produce a page
// that set_site would refuse. A rejected edit leaves the field as it was.
func (e *editor) edit(target, old, replacement string) string {
	var field *string
	var section string
	switch target {
	case editTargetCSS:
		field, section = &e.site.CSS, "CURRENT STYLESHEET"
	case RegionHeader:
		field, section = &e.site.Header, "the header in CURRENT REGIONS"
	case RegionBoard:
		field, section = &e.site.Board, "the board in CURRENT REGIONS"
	case RegionFooter:
		field, section = &e.site.Footer, "the footer in CURRENT REGIONS"
	default:
		return fmt.Sprintf("rejected: unknown target %q, use css, header, board or footer", target)
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
	if target == editTargetCSS {
		next, err = cleanCSS(next)
	} else {
		next, err = cleanRegion(target, next)
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

func cleanRegion(name, html string) (string, error) {
	switch name {
	case RegionHeader, RegionBoard, RegionFooter:
	default:
		return "", fmt.Errorf("unknown region %q, use header, board or footer", name)
	}

	html = strings.TrimSpace(html)
	if len(html) < minRegionLen {
		return "", fmt.Errorf("region is too short to be real")
	}
	if len(html) > maxRegionLen {
		return "", fmt.Errorf("region is longer than %d bytes", maxRegionLen)
	}
	if name == RegionBoard && !strings.Contains(html, PostsPlaceholder) {
		return "", fmt.Errorf("board must contain %s, where the posts go", PostsPlaceholder)
	}
	return html, nil
}

// cleanStateSchema validates the model's state keys and stores them as a
// compact JSON object. Only declared keys can be written, so a visitor cannot
// invent state the model did not ask for.
func cleanStateSchema(keys []stateKeyInput) (string, error) {
	if len(keys) > maxStateKeys {
		return "", fmt.Errorf("at most %d state keys", maxStateKeys)
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

		key := StateKey{Op: input.Op}
		switch input.Op {
		case "counter":
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
		default:
			return "", fmt.Errorf("unknown state op %q, use counter, list or choice", input.Op)
		}
		schema[name] = key
	}

	data, err := json.Marshal(schema)
	if err != nil {
		return "", fmt.Errorf("encode state schema: %w", err)
	}
	return string(data), nil
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
	css = strings.TrimSpace(css)
	if len(css) > maxCSSLen {
		return "", fmt.Errorf("stylesheet is longer than %d bytes", maxCSSLen)
	}
	return css, nil
}

func truncateRunes(s string, limit int) string {
	runes := []rune(s)
	if len(runes) <= limit {
		return s
	}
	return string(runes[:limit])
}
