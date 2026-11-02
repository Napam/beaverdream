package webmaster

import (
	"encoding/json"
	"fmt"
	"strings"
)

const maxOverrides = 12

// Override changes parts of the page for one visitor. A nil field is left
// alone. CSS is added after the site stylesheet, so an override can be one
// extra rule; the regions replace the global ones for that visitor.
type Override struct {
	Name   string  `json:"name"`
	CSS    *string `json:"css,omitempty"`
	Header *string `json:"header,omitempty"`
	Board  *string `json:"board,omitempty"`
	Footer *string `json:"footer,omitempty"`
}

// OverrideKey is how a name is matched. The name goes through the same cleaner
// as the cookie, so "Dave!" and "dave" are the same visitor. Two visitors who
// pick the same name share the same page, which is the joke and also a hole:
// there are no accounts to tell them apart.
func OverrideKey(name string) string {
	return strings.ToLower(CleanIdentity(name))
}

// FieldNames lists the parts an override changes, in a stable order.
func (o Override) FieldNames() []string {
	var names []string
	for _, field := range []struct {
		name  string
		value *string
	}{
		{"css", o.CSS},
		{"header", o.Header},
		{"board", o.Board},
		{"footer", o.Footer},
	} {
		if field.value != nil {
			names = append(names, field.name)
		}
	}
	return names
}

// Preview renders the override as the text the admin page shows.
func (o Override) Preview() string {
	var b strings.Builder
	for _, field := range []struct {
		label string
		value *string
	}{
		{"CSS", o.CSS},
		{"HEADER", o.Header},
		{"BOARD", o.Board},
		{"FOOTER", o.Footer},
	} {
		if field.value == nil {
			continue
		}
		b.WriteString(field.label)
		b.WriteString(":\n")
		b.WriteString(*field.value)
		b.WriteString("\n\n")
	}
	return strings.TrimSpace(b.String())
}

// MergeOverride keeps the parts the new override does not mention, so the model
// can tweak one field without resending the visitor's whole page.
func MergeOverride(existing, next Override) Override {
	if next.CSS == nil {
		next.CSS = existing.CSS
	}
	if next.Header == nil {
		next.Header = existing.Header
	}
	if next.Board == nil {
		next.Board = existing.Board
	}
	if next.Footer == nil {
		next.Footer = existing.Footer
	}
	return next
}

// EncodeOverride stores an override as the JSON text the version row keeps.
func EncodeOverride(o Override) (string, error) {
	data, err := json.Marshal(o)
	if err != nil {
		return "", fmt.Errorf("encode visitor override: %w", err)
	}
	return string(data), nil
}

// DecodeOverride reads the stored JSON. It does not validate the markup; the
// editor did that before the row was written.
func DecodeOverride(data string) (Override, error) {
	var o Override
	if err := json.Unmarshal([]byte(data), &o); err != nil {
		return Override{}, fmt.Errorf("decode visitor override: %w", err)
	}
	return o, nil
}

// ApplyOverride folds one visitor's changes into a site. The CSS is appended so
// the global theme still applies underneath it.
func ApplyOverride(site Site, o *Override) Site {
	if o == nil {
		return site
	}
	if o.CSS != nil {
		css := strings.TrimSpace(*o.CSS)
		if css != "" {
			if strings.TrimSpace(site.CSS) == "" {
				site.CSS = css
			} else {
				site.CSS += "\n" + css
			}
		}
	}
	if o.Header != nil {
		site.Header = *o.Header
	}
	if o.Board != nil {
		site.Board = *o.Board
	}
	if o.Footer != nil {
		site.Footer = *o.Footer
	}
	return site
}

// OverrideMarkupChanged reports whether two overrides differ in a way only a
// reload can show. CSS can be swapped in place.
func OverrideMarkupChanged(a, b *Override) bool {
	var empty Override
	if a == nil {
		a = &empty
	}
	if b == nil {
		b = &empty
	}
	return !sameOptional(a.Header, b.Header) ||
		!sameOptional(a.Board, b.Board) ||
		!sameOptional(a.Footer, b.Footer)
}

// OverrideCSS returns the stylesheet an override adds, or an empty string.
func OverrideCSS(o *Override) string {
	if o == nil || o.CSS == nil {
		return ""
	}
	return *o.CSS
}

func sameOptional(a, b *string) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return *a == *b
}

// cleanOverride turns the tool arguments into an Override, running each field
// through the same limits as the global page.
func cleanOverride(
	css *string,
	header *string,
	board *string,
	footer *string,
) (Override, error) {
	var o Override
	if css != nil {
		cleaned, err := cleanCSS(*css)
		if err != nil {
			return Override{}, err
		}
		o.CSS = &cleaned
	}
	for _, region := range []struct {
		name   string
		value  *string
		target **string
	}{
		{RegionHeader, header, &o.Header},
		{RegionBoard, board, &o.Board},
		{RegionFooter, footer, &o.Footer},
	} {
		if region.value == nil {
			continue
		}
		cleaned, err := cleanRegion(region.name, *region.value)
		if err != nil {
			return Override{}, err
		}
		*region.target = &cleaned
	}
	return o, nil
}
