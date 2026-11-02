package webmaster

import (
	"encoding/json"
	"fmt"
	"strings"
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
	site    Site
	reason  string
	done    bool
	dirty   bool
	authors []string
}

func newEditor(site Site) *editor {
	return &editor{site: site}
}

// run applies one tool call and returns the text fed back to the model. It
// never returns a Go error: a rejected argument is a normal result the model
// can correct on the next round.
func (e *editor) run(name, arguments string) string {
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
		if in.CSS != nil {
			css, err := cleanCSS(*in.CSS)
			if err != nil {
				return "rejected: " + err.Error()
			}
			e.site.CSS = css
		}
		regions := []struct {
			name   string
			value  *string
			target *string
		}{
			{RegionHeader, in.Header, &e.site.Header},
			{RegionBoard, in.Board, &e.site.Board},
			{RegionFooter, in.Footer, &e.site.Footer},
		}
		for _, region := range regions {
			if region.value == nil {
				continue
			}
			cleaned, err := cleanRegion(region.name, *region.value)
			if err != nil {
				return "rejected: " + err.Error()
			}
			*region.target = cleaned
		}
		e.dirty = true
		return "ok"

	case "set_css":
		var in struct {
			CSS string `json:"css"`
		}
		if err := json.Unmarshal([]byte(arguments), &in); err != nil {
			return "rejected: arguments are not valid JSON"
		}
		css, err := cleanCSS(in.CSS)
		if err != nil {
			return "rejected: " + err.Error()
		}
		e.site.CSS = css
		e.dirty = true
		return "ok"

	case "set_region":
		var in struct {
			Region string `json:"region"`
			HTML   string `json:"html"`
		}
		if err := json.Unmarshal([]byte(arguments), &in); err != nil {
			return "rejected: arguments are not valid JSON"
		}
		region, err := cleanRegion(in.Region, in.HTML)
		if err != nil {
			return "rejected: " + err.Error()
		}
		switch in.Region {
		case RegionHeader:
			e.site.Header = region
		case RegionBoard:
			e.site.Board = region
		case RegionFooter:
			e.site.Footer = region
		}
		e.dirty = true
		return "ok"

	case "set_user_override":
		var in struct {
			Name   string  `json:"name"`
			CSS    *string `json:"css"`
			Header *string `json:"header"`
			Board  *string `json:"board"`
			Footer *string `json:"footer"`
			Clear  bool    `json:"clear"`
		}
		if err := json.Unmarshal([]byte(arguments), &in); err != nil {
			return "rejected: arguments are not valid JSON"
		}
		names, err := e.overrideTargets(in.Name)
		if err != nil {
			return "rejected: " + err.Error()
		}
		if in.Clear {
			for _, name := range names {
				key := OverrideKey(name)
				if _, ok := e.site.Overrides[key]; ok {
					delete(e.site.Overrides, key)
					e.dirty = true
				}
			}
			return "ok"
		}
		override, err := cleanOverride(in.CSS, in.Header, in.Board, in.Footer)
		if err != nil {
			return "rejected: " + err.Error()
		}
		if len(override.FieldNames()) == 0 {
			return "rejected: set_user_override needs a field to change, or clear=true"
		}
		if e.site.Overrides == nil {
			e.site.Overrides = make(map[string]Override, len(names))
		}
		grown := 0
		for _, name := range names {
			if _, exists := e.site.Overrides[OverrideKey(name)]; !exists {
				grown++
			}
		}
		if len(e.site.Overrides)+grown > maxOverrides {
			return fmt.Sprintf("rejected: at most %d visitor overrides", maxOverrides)
		}
		for _, name := range names {
			key := OverrideKey(name)
			merged := MergeOverride(e.site.Overrides[key], override)
			merged.Name = name
			e.site.Overrides[key] = merged
		}
		e.dirty = true
		return "ok"

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
			Reason string `json:"reason"`
		}
		_ = json.Unmarshal([]byte(arguments), &in)
		e.done = true
		e.reason = truncateRunes(strings.TrimSpace(in.Reason), 200)
		return "ok"
	}

	return fmt.Sprintf("rejected: unknown tool %q", name)
}

// overrideTargets resolves the name a tool call targets. "poster" is a shortcut
// for the people whose posts triggered this rewrite, which is the case the
// model reaches for most.
func (e *editor) overrideTargets(name string) ([]string, error) {
	name = CleanIdentity(name)
	if name == "" {
		return nil, fmt.Errorf("name is empty")
	}
	if !strings.EqualFold(name, "poster") {
		return []string{name}, nil
	}

	names := make([]string, 0, len(e.authors))
	for _, author := range e.authors {
		// Admin instructions use the same author name as the reserved admin
		// visitor, and neither has a page worth overriding.
		if author == "" || strings.EqualFold(author, "admin") {
			continue
		}
		names = append(names, author)
	}
	if len(names) == 0 {
		return nil, fmt.Errorf(`no poster to target; use a visitor name, not "poster"`)
	}
	return names, nil
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
