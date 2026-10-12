package navigation

import (
	"strings"
	"unicode"
	"unicode/utf8"
)

// normalize folds a string into the single form every comparison in this
// package uses: half width, lower case, punctuation turned into spaces,
// whitespace collapsed.
//
// Underscore and hyphen survive on purpose. They are word-internal in both the
// manifest (user_code, TRC20, 出金审核-风控) and in what operators type, so
// splitting on them would break exactly the identifiers worth matching.
func normalize(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	pendingSpace := false
	wrote := false
	for _, r := range s {
		switch {
		case r >= 0xFF01 && r <= 0xFF5E: // full-width ASCII
			r -= 0xFEE0
		case r == 0x3000: // ideographic space
			r = ' '
		}
		if isSeparator(r) {
			pendingSpace = wrote
			continue
		}
		if pendingSpace {
			b.WriteRune(' ')
			pendingSpace = false
		}
		b.WriteRune(unicode.ToLower(r))
		wrote = true
	}
	return b.String()
}

func isSeparator(r rune) bool {
	if r == '_' || r == '-' {
		return false
	}
	return unicode.IsSpace(r) || unicode.IsPunct(r) || unicode.IsSymbol(r)
}

// isIdeographic reports whether a rune belongs to a script written without
// spaces, which is what decides between "treat the segment as one token" and
// "break it into character bigrams".
func isIdeographic(r rune) bool {
	return unicode.In(r, unicode.Han, unicode.Hiragana, unicode.Katakana, unicode.Hangul)
}

func hasIdeographic(s string) bool {
	for _, r := range s {
		if isIdeographic(r) {
			return true
		}
	}
	return false
}

func runeLen(s string) int {
	return utf8.RuneCountInString(s)
}

func normalizeAll(values []string) []string {
	result := make([]string, 0, len(values))
	for _, value := range values {
		if normalized := normalize(value); normalized != "" {
			result = append(result, normalized)
		}
	}
	return result
}

func containsAny(haystacks []string, needle string) bool {
	for _, haystack := range haystacks {
		if strings.Contains(haystack, needle) {
			return true
		}
	}
	return false
}
