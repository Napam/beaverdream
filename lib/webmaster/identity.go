package webmaster

import (
	"strings"
	"unicode"
)

// MaxIdentityLen caps a display name in runes.
const MaxIdentityLen = 32

// CleanIdentity keeps a display name to letters, digits, spaces and a few
// harmless punctuation marks, collapses runs of whitespace, and caps the
// result. The handlers run every cookie value through it, so a name on a post
// is always a clean one.
func CleanIdentity(value string) string {
	value = strings.Map(func(r rune) rune {
		switch {
		case unicode.IsLetter(r), unicode.IsDigit(r):
			return r
		case r == ' ', r == '_', r == '-', r == '.', r == '\'':
			return r
		default:
			return -1
		}
	}, value)
	value = strings.Join(strings.Fields(value), " ")
	return truncateRunes(value, MaxIdentityLen)
}
