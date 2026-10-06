// Package slug builds stable, readable keys from display names.
package slug

import "strings"

var transliterate = strings.NewReplacer(
	"ä", "ae", "ö", "oe", "ü", "ue", "ß", "ss",
	"à", "a", "á", "a", "â", "a",
	"è", "e", "é", "e", "ê", "e", "ë", "e",
	"ì", "i", "í", "i", "î", "i", "ï", "i",
	"ò", "o", "ó", "o", "ô", "o",
	"ù", "u", "ú", "u", "û", "u",
	"ç", "c", "ñ", "n",
)

// Make lower-cases s, transliterates German umlauts and common accents, and
// joins the remaining runs of letters and digits with single dashes:
// "Essen & Trinken" becomes "essen-trinken". The result is empty if s
// contains no letters or digits.
func Make(s string) string {
	s = transliterate.Replace(strings.ToLower(s))
	var b strings.Builder
	gap := false
	for i := 0; i < len(s); i++ {
		c := s[i]
		if (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') {
			if gap && b.Len() > 0 {
				b.WriteByte('-')
			}
			gap = false
			b.WriteByte(c)
		} else {
			gap = true
		}
	}
	return b.String()
}
