package slug

import "testing"

func TestMake(t *testing.T) {
	tests := []struct{ in, want string }{
		{"Essen & Trinken", "essen-trinken"},
		{"Lebensmittel", "lebensmittel"},
		{"Möbel, Geräte & Zubehör", "moebel-geraete-zubehoer"},
		{"ÖPNV", "oepnv"},
		{"Straße", "strasse"},
		{"  Auto / KFZ  ", "auto-kfz"},
		{"Café", "cafe"},
		{"Urlaub 2026", "urlaub-2026"},
		{"--a--b--", "a-b"},
		{"&&&", ""},
		{"", ""},
		{"日本", ""},
	}
	for _, tt := range tests {
		if got := Make(tt.in); got != tt.want {
			t.Errorf("Make(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}
