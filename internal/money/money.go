// Package money parses and formats amounts as integer cents.
//
// Amounts are never represented as floats. The caller picks the number format
// explicitly; the format is never guessed, because "1.234" means 1234.00 in
// German and 1.234 in English notation.
package money

import (
	"fmt"
	"strconv"
	"strings"
)

// maxIntDigits keeps every parsed amount far inside the int64 range.
const maxIntDigits = 15

// ParseDE parses German notation: "." groups thousands, "," is the decimal
// separator. Examples: "-9,99", "1.234,56", "-57".
func ParseDE(s string) (int64, error) {
	return parse(s, '.', ',')
}

// ParseEN parses English notation: "," groups thousands, "." is the decimal
// separator. Examples: "-16.45", "4,602.28".
func ParseEN(s string) (int64, error) {
	return parse(s, ',', '.')
}

func parse(s string, group, decimal byte) (int64, error) {
	orig := s
	s = strings.TrimSpace(s)
	fail := func(reason string) (int64, error) {
		return 0, fmt.Errorf("money: invalid amount %q: %s", orig, reason)
	}
	if s == "" {
		return fail("empty")
	}

	negative := false
	switch s[0] {
	case '-':
		negative = true
		s = s[1:]
	case '+':
		s = s[1:]
	}

	intPart, fracPart, hasDecimal := strings.Cut(s, string(decimal))
	if hasDecimal && (len(fracPart) < 1 || len(fracPart) > 2) {
		return fail("expected one or two decimal places")
	}
	if !allDigits(fracPart) {
		return fail("unexpected character in decimal places")
	}

	if strings.IndexByte(intPart, group) >= 0 {
		groups := strings.Split(intPart, string(group))
		for i, g := range groups {
			if !allDigits(g) || g == "" {
				return fail("unexpected character")
			}
			if (i == 0 && len(g) > 3) || (i > 0 && len(g) != 3) {
				return fail("misplaced thousands separator")
			}
		}
		intPart = strings.Join(groups, "")
	}
	if intPart == "" || !allDigits(intPart) {
		return fail("unexpected character")
	}
	if len(intPart) > maxIntDigits {
		return fail("too large")
	}

	whole, err := strconv.ParseInt(intPart, 10, 64)
	if err != nil {
		return fail(err.Error())
	}
	var frac int64
	switch len(fracPart) {
	case 1:
		frac = int64(fracPart[0]-'0') * 10
	case 2:
		frac = int64(fracPart[0]-'0')*10 + int64(fracPart[1]-'0')
	}

	cents := whole*100 + frac
	if negative {
		cents = -cents
	}
	return cents, nil
}

func allDigits(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}

// Format renders cents as a plain decimal with a dot and no grouping, e.g.
// "-9.99". This is the machine-readable form used in exports.
func Format(cents int64) string {
	sign, whole, frac := split(cents)
	return fmt.Sprintf("%s%d.%02d", sign, whole, frac)
}

// FormatDE renders cents for display in German notation with the euro sign,
// e.g. "-1.234,56 €".
func FormatDE(cents int64) string {
	sign, whole, frac := split(cents)
	digits := strconv.FormatUint(whole, 10)
	var b strings.Builder
	b.WriteString(sign)
	for i := 0; i < len(digits); i++ {
		if i > 0 && (len(digits)-i)%3 == 0 {
			b.WriteByte('.')
		}
		b.WriteByte(digits[i])
	}
	fmt.Fprintf(&b, ",%02d €", frac)
	return b.String()
}

func split(cents int64) (sign string, whole, frac uint64) {
	abs := uint64(cents)
	if cents < 0 {
		sign = "-"
		abs = -abs
	}
	return sign, abs / 100, abs % 100
}
