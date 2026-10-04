package money

import "testing"

func TestParseDE(t *testing.T) {
	tests := []struct {
		in   string
		want int64
	}{
		{"-9,99", -999},
		{"1.234,56", 123456},
		{"-57", -5700},
		{"0", 0},
		{"0,00", 0},
		{"9,9", 990},
		{"+12,30", 1230},
		{" 12,30 ", 1230},
		{"1.234", 123400},
		{"1.234.567,89", 123456789},
		{"-1234,5", -123450},
	}
	for _, tt := range tests {
		got, err := ParseDE(tt.in)
		if err != nil {
			t.Errorf("ParseDE(%q): unexpected error: %v", tt.in, err)
			continue
		}
		if got != tt.want {
			t.Errorf("ParseDE(%q) = %d, want %d", tt.in, got, tt.want)
		}
	}
}

func TestParseEN(t *testing.T) {
	tests := []struct {
		in   string
		want int64
	}{
		{"-16.45", -1645},
		{"4,602.28", 460228},
		{"57", 5700},
		{"0.5", 50},
		{"1,234,567.89", 123456789},
		{"-0.01", -1},
	}
	for _, tt := range tests {
		got, err := ParseEN(tt.in)
		if err != nil {
			t.Errorf("ParseEN(%q): unexpected error: %v", tt.in, err)
			continue
		}
		if got != tt.want {
			t.Errorf("ParseEN(%q) = %d, want %d", tt.in, got, tt.want)
		}
	}
}

func TestParseErrors(t *testing.T) {
	de := []string{
		"", " ", "-", "abc", "9,999", "9,", ",99", "1,2,3",
		"-16.45",  // English decimal: "45" is not a thousands group
		"1.23,45", // short thousands group
		"1234.567,00",
		"1..234", "12 €", "--5", "1e3",
		"1234567890123456",
	}
	for _, in := range de {
		if got, err := ParseDE(in); err == nil {
			t.Errorf("ParseDE(%q) = %d, want error", in, got)
		}
	}

	en := []string{
		"", "9.999", "9.", ".99",
		"-9,99", // German decimal: "99" is not a thousands group
		"4,60.28",
	}
	for _, in := range en {
		if got, err := ParseEN(in); err == nil {
			t.Errorf("ParseEN(%q) = %d, want error", in, got)
		}
	}
}

func TestFormat(t *testing.T) {
	tests := []struct {
		cents  int64
		plain  string
		german string
	}{
		{0, "0.00", "0,00 €"},
		{5, "0.05", "0,05 €"},
		{-5, "-0.05", "-0,05 €"},
		{-999, "-9.99", "-9,99 €"},
		{123456, "1234.56", "1.234,56 €"},
		{-123456789, "-1234567.89", "-1.234.567,89 €"},
		{100000, "1000.00", "1.000,00 €"},
		{99999, "999.99", "999,99 €"},
	}
	for _, tt := range tests {
		if got := Format(tt.cents); got != tt.plain {
			t.Errorf("Format(%d) = %q, want %q", tt.cents, got, tt.plain)
		}
		if got := FormatDE(tt.cents); got != tt.german {
			t.Errorf("FormatDE(%d) = %q, want %q", tt.cents, got, tt.german)
		}
	}
}

func TestFormatRoundTrip(t *testing.T) {
	for _, cents := range []int64{0, 1, -1, 99, 100, -12345, 987654321} {
		got, err := ParseEN(Format(cents))
		if err != nil || got != cents {
			t.Errorf("ParseEN(Format(%d)) = %d, %v", cents, got, err)
		}
	}
}
