package location

import (
	"strings"
	"unicode"
)

// fold maps accented Latin letters (after lower-casing) to ASCII, and the
// Luganda eng (ŋ) to "ng", so "Église" and "eglise" match the same alias.
var fold = map[rune]string{
	'à': "a", 'á': "a", 'â': "a", 'ã': "a", 'ä': "a", 'å': "a",
	'ç': "c", 'è': "e", 'é': "e", 'ê': "e", 'ë': "e",
	'ì': "i", 'í': "i", 'î': "i", 'ï': "i", 'ñ': "n",
	'ò': "o", 'ó': "o", 'ô': "o", 'õ': "o", 'ö': "o",
	'ù': "u", 'ú': "u", 'û': "u", 'ü': "u", 'ý': "y", 'ÿ': "y", 'ŋ': "ng",
}

// Normalise prepares a place name for fuzzy matching (Build Prompt §12):
// lower case, ASCII-folded, every other character a separator, runs of
// separators collapsed to one space, trimmed.
func Normalise(s string) string {
	var b strings.Builder
	space := false
	for _, r := range s {
		r = unicode.ToLower(r)
		var out string
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			out = string(r)
		default:
			out = fold[r]
		}
		if out == "" {
			space = b.Len() > 0
			continue
		}
		if space {
			b.WriteByte(' ')
			space = false
		}
		b.WriteString(out)
	}
	return b.String()
}
