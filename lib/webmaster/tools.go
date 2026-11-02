package webmaster

import (
	"encoding/json"
	"fmt"
	"strings"
)

const (
	maxCSSLen    = 20000
	maxRegionLen = 20000
	maxNoteLen   = 280
	minRegionLen = 8
)

// editor applies tool calls to a copy of the site. Nothing reaches the
// database until the whole mutation finishes, so a failed round cannot leave a
// half-written page behind.
type editor struct {
	site   Site
	reason string
	done   bool
	dirty  bool
}

func newEditor(site Site) *editor {
	return &editor{site: site}
}

// run applies one tool call and returns the text fed back to the model. It
// never returns a Go error: a rejected argument is a normal result the model
// can correct on the next round.
func (e *editor) run(name, arguments string) string {
	switch name {
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

	case "set_note":
		var in struct {
			Text string `json:"text"`
		}
		if err := json.Unmarshal([]byte(arguments), &in); err != nil {
			return "rejected: arguments are not valid JSON"
		}
		e.site.Note = truncateRunes(strings.TrimSpace(in.Text), maxNoteLen)
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
