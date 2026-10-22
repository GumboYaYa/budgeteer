// Package categorize predicts the category of a transaction from the
// transactions that were categorized before.
package categorize

import (
	"strings"
	"unicode"

	"github.com/GumboYaYa/budgeteer/internal/store"
)

// The kinds of evidence, in the order they are tried. The first kind that has
// history for a transaction decides.
const (
	BasisName     = "name"     // the whole normalized counterparty name
	BasisCreditor = "creditor" // the creditor id
	BasisName2    = "name2"    // the first two words of the name
	BasisRef      = "ref"      // the counterparty reference (IBAN)
)

var bases = []string{BasisName, BasisCreditor, BasisName2, BasisRef}

// minSure is the number of earlier transactions a unanimous history needs
// before its category is applied without asking.
const minSure = 2

// Prediction is the category history points to for one transaction.
type Prediction struct {
	CategoryID int64
	Basis      string
	// Count is the number of earlier transactions with the same key.
	Count int
	// Sure is set when the category can be applied without asking: all
	// earlier transactions of the same counterparty name or creditor id, at
	// least minSure of them, have this category. The first two words of a
	// name and the reference only ever suggest; card payments share the
	// clearing IBAN of the card issuer.
	Sure bool
}

// RuleID is what an allocation applied from this prediction stores as its
// rule id.
func (p Prediction) RuleID() string {
	return "history:" + p.Basis
}

// noise are words that do not tell counterparties apart: legal forms and
// what is left of web addresses.
var noise = map[string]bool{
	"gmbh": true, "mbh": true, "ag": true, "kg": true, "kgaa": true, "co": true, "se": true, "ug": true,
	"ohg": true, "gbr": true, "eg": true, "ev": true, "e": true, "v": true, "inc": true, "ltd": true,
	"limited": true, "sarl": true, "sa": true, "bv": true, "und": true, "the": true,
	"www": true, "com": true, "de": true,
}

var umlauts = strings.NewReplacer("ä", "ae", "ö", "oe", "ü", "ue", "ß", "ss")

// Normalize splits a counterparty name into the words that identify it:
// lower case, umlauts written out, without legal forms, numbers and whatever
// follows "//" (card payments append the place there).
func Normalize(name string) []string {
	name = umlauts.Replace(strings.ToLower(name))
	name, _, _ = strings.Cut(name, "//")
	words := strings.FieldsFunc(name, func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	})
	kept := words[:0]
	for _, w := range words {
		if noise[w] || strings.Trim(w, "0123456789") == "" {
			continue
		}
		kept = append(kept, w)
	}
	return kept
}

// keys returns the key of the example for each kind of evidence; an empty
// key means the example has nothing to offer for that kind.
func keys(e store.CategoryExample) map[string]string {
	words := Normalize(e.Counterparty)
	return map[string]string{
		BasisName:     strings.Join(words, " "),
		BasisCreditor: strings.ToUpper(strings.TrimSpace(e.CreditorID)),
		BasisName2:    strings.Join(words[:min(2, len(words))], " "),
		BasisRef:      store.NormalizeIBAN(e.CounterpartyRef),
	}
}

// tally counts the categories seen for one key.
type tally struct {
	count map[int64]int
	last  map[int64]int // position of the newest example per category
	total int
}

// Index holds the history predictions are made from.
type Index struct {
	byBasis map[string]map[string]*tally
	seen    int
}

// NewIndex builds an index from categorized transactions, oldest first.
func NewIndex(history []store.CategoryExample) *Index {
	ix := &Index{byBasis: make(map[string]map[string]*tally, len(bases))}
	for _, b := range bases {
		ix.byBasis[b] = make(map[string]*tally)
	}
	for _, e := range history {
		ix.Add(e)
	}
	return ix
}

// Add records one more categorized transaction, newer than all before.
func (ix *Index) Add(e store.CategoryExample) {
	if e.CategoryID == 0 {
		return
	}
	ix.seen++
	for basis, key := range keys(e) {
		if key == "" {
			continue
		}
		t := ix.byBasis[basis][key]
		if t == nil {
			t = &tally{count: make(map[int64]int), last: make(map[int64]int)}
			ix.byBasis[basis][key] = t
		}
		t.count[e.CategoryID]++
		t.last[e.CategoryID] = ix.seen
		t.total++
	}
}

// Predict returns the most frequent category of the earlier transactions
// that share a key with e; among equally frequent ones the most recently
// used wins. It reports false when there is no history for e.
func (ix *Index) Predict(e store.CategoryExample) (Prediction, bool) {
	k := keys(e)
	for _, basis := range bases {
		t := ix.byBasis[basis][k[basis]]
		if k[basis] == "" || t == nil {
			continue
		}
		var best int64
		for category, n := range t.count {
			if best == 0 || n > t.count[best] || (n == t.count[best] && t.last[category] > t.last[best]) {
				best = category
			}
		}
		return Prediction{
			CategoryID: best,
			Basis:      basis,
			Count:      t.total,
			Sure:       (basis == BasisName || basis == BasisCreditor) && len(t.count) == 1 && t.total >= minSure,
		}, true
	}
	return Prediction{}, false
}
