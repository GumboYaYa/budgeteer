package categorize

import (
	"slices"
	"testing"

	"github.com/GumboYaYa/budgeteer/internal/store"
)

func TestNormalize(t *testing.T) {
	tests := []struct {
		name string
		want []string
	}{
		{"Bäckerei Krümel GmbH & Co. KG", []string{"baeckerei", "kruemel"}},
		{"MARKTHALLE NORD 4711//Musterstadt/DE", []string{"markthalle", "nord"}},
		{"www.buchladen-beispiel.de", []string{"buchladen", "beispiel"}},
		{"Straßenbahn AG", []string{"strassenbahn"}},
		{"Studio 54b", []string{"studio", "54b"}},
		{"  ", nil},
		{"GmbH 123", nil},
	}
	for _, tt := range tests {
		if got := Normalize(tt.name); !slices.Equal(got, tt.want) {
			t.Errorf("Normalize(%q) = %v, want %v", tt.name, got, tt.want)
		}
	}
}

func TestPredict(t *testing.T) {
	const food, home, fun = 1, 2, 3
	history := []store.CategoryExample{
		{Counterparty: "Markthalle Nord GmbH", CounterpartyRef: "DE02 1203 0000 0000 2020 51", CategoryID: food},
		{Counterparty: "MARKTHALLE NORD", CounterpartyRef: "DE02120300000000202051", CategoryID: food},
		{Counterparty: "Hausverwaltung Beispiel", CreditorID: "DE98ZZZ09999999999", CategoryID: home},
		{Counterparty: "Hausverwaltung Beispiel", CreditorID: "DE98ZZZ09999999999", CategoryID: home},
		{Counterparty: "Kiosk am Eck", CategoryID: food},
		{Counterparty: "Versand Muster", CategoryID: fun},
		{Counterparty: "Versand Muster", CategoryID: home},
		{Counterparty: "Versand Muster", CategoryID: home},
		{Counterparty: "Versand Muster", CategoryID: fun},
		{Counterparty: "Kartenzahlung A", CounterpartyRef: "DE11111111111111111111", CategoryID: fun},
		{Counterparty: "Kartenzahlung B", CounterpartyRef: "DE11111111111111111111", CategoryID: fun},
		{Counterparty: "Ohne Kategorie"}, // not categorized, must not count
	}
	ix := NewIndex(history)

	tests := []struct {
		name  string
		tx    store.CategoryExample
		found bool
		want  Prediction
	}{
		{
			name:  "same name in another spelling is sure",
			tx:    store.CategoryExample{Counterparty: "Markthalle Nord 0815//Musterstadt"},
			found: true,
			want:  Prediction{CategoryID: food, Basis: BasisName, Count: 2, Sure: true},
		},
		{
			name:  "one earlier transaction only suggests",
			tx:    store.CategoryExample{Counterparty: "Kiosk am Eck"},
			found: true,
			want:  Prediction{CategoryID: food, Basis: BasisName, Count: 1},
		},
		{
			name:  "mixed history suggests the most frequent, newest on a tie",
			tx:    store.CategoryExample{Counterparty: "Versand Muster"},
			found: true,
			want:  Prediction{CategoryID: fun, Basis: BasisName, Count: 4},
		},
		{
			name:  "unknown name falls back to the creditor id",
			tx:    store.CategoryExample{Counterparty: "HV Beispiel Abrechnung", CreditorID: "de98zzz09999999999"},
			found: true,
			want:  Prediction{CategoryID: home, Basis: BasisCreditor, Count: 2, Sure: true},
		},
		{
			name:  "the first two words only suggest",
			tx:    store.CategoryExample{Counterparty: "Markthalle Nord Filiale Sued"},
			found: true,
			want:  Prediction{CategoryID: food, Basis: BasisName2, Count: 2},
		},
		{
			name:  "the reference only suggests, even when unanimous",
			tx:    store.CategoryExample{Counterparty: "Kartenzahlung C", CounterpartyRef: "DE11 1111 1111 1111 1111 11"},
			found: true,
			want:  Prediction{CategoryID: fun, Basis: BasisRef, Count: 2},
		},
		{
			name: "no history",
			tx:   store.CategoryExample{Counterparty: "Ganz Neu", CounterpartyRef: "DE22222222222222222222"},
		},
		{
			name: "uncategorized transactions are not history",
			tx:   store.CategoryExample{Counterparty: "Ohne Kategorie"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, found := ix.Predict(tt.tx)
			if found != tt.found || got != tt.want {
				t.Errorf("Predict = %+v, %v; want %+v, %v", got, found, tt.want, tt.found)
			}
		})
	}
}
