package fabrica

import (
	"strings"
	"unicode"

	"golang.org/x/text/runes"
	"golang.org/x/text/transform"
	"golang.org/x/text/unicode/norm"
)

// Slugify converts "Small CRM Dashboard v2" into "small-crm-dashboard-v2".
// Accents are folded ("Fábrica" -> "fabrica") so slugs stay URL-safe. It is
// used to derive project slugs when the client does not send one.
func Slugify(input string) string {
	var b strings.Builder
	prevDash := true // avoid a leading dash
	for _, r := range strings.ToLower(strings.TrimSpace(deaccent(input))) {
		switch {
		case unicode.IsLetter(r) || unicode.IsDigit(r):
			b.WriteRune(r)
			prevDash = false
		case r == '_' || r == '-' || r == '.' || unicode.IsSpace(r):
			if !prevDash {
				b.WriteByte('-')
				prevDash = true
			}
		default:
			// drop punctuation
		}
	}
	return strings.Trim(b.String(), "-")
}

// deaccent strips combining marks (NFD decomposition, marks removed, NFC).
func deaccent(input string) string {
	chain := transform.Chain(norm.NFD, runes.Remove(runes.In(unicode.Mn)), norm.NFC)
	folded, _, err := transform.String(chain, input)
	if err != nil {
		return input
	}
	return folded
}
