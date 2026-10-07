package reserve

import (
	"testing"

	"github.com/GumboYaYa/budgeteer/internal/store"
)

func paid(date string, cents int64) store.TxView {
	return store.TxView{BookingDate: date, AmountCents: cents}
}

func TestBuild(t *testing.T) {
	const asOf = "2026-10-05"
	tests := []struct {
		name  string
		paid  []store.TxView
		saved int64

		yearly, steady, target, monthly int64
		catchUp                         int
	}{
		{name: "nothing marked"},
		{
			// 1200.00 paid in April, due again in 6 months; half is saved.
			name: "one yearly bill, on target",
			paid: []store.TxView{paid("2026-04-15", -120000)}, saved: 60000,
			yearly: 120000, steady: 10000, target: 60000, monthly: 10000,
		},
		{
			name:   "one yearly bill, empty pot",
			paid:   []store.TxView{paid("2026-04-15", -120000)},
			yearly: 120000, steady: 10000, target: 60000, monthly: 20000, catchUp: 6,
		},
		{
			name:   "bill due in two months, empty pot",
			paid:   []store.TxView{paid("2025-12-01", -60000)},
			yearly: 60000, steady: 5000, target: 50000, monthly: 30000, catchUp: 2,
		},
		{
			// More saved than needed: the bill still comes every year.
			name: "over-funded pot keeps the steady amount",
			paid: []store.TxView{paid("2026-04-15", -120000)}, saved: 500000,
			yearly: 120000, steady: 10000, target: 60000, monthly: 10000,
		},
		{
			// Due in 3 and 9 months, 600.00 each.
			name: "half-yearly bill, on target",
			paid: []store.TxView{paid("2026-01-10", -60000), paid("2026-07-10", -60000)}, saved: 60000,
			yearly: 120000, steady: 10000, target: 60000, monthly: 10000,
		},
		{
			// The first bill is covered, the second needs 1200 - 700 in 9 months.
			name: "later bill decides",
			paid: []store.TxView{paid("2026-01-10", -60000), paid("2026-07-10", -60000)}, saved: 70000,
			yearly: 120000, steady: 10000, target: 60000, monthly: 10000,
		},
		{
			name: "early bill decides",
			paid: []store.TxView{paid("2026-01-10", -60000), paid("2026-07-10", -60000)}, saved: 15000,
			yearly: 120000, steady: 10000, target: 60000, monthly: 15000, catchUp: 3,
		},
		{
			name: "refund reduces the sum",
			paid: []store.TxView{paid("2026-04-15", -120000), paid("2026-04-20", 12000)}, saved: 54000,
			yearly: 108000, steady: 9000, target: 54000, monthly: 9000,
		},
		{
			// Booked later in October last year: due this month, one transfer left.
			name:   "bill due in the current month",
			paid:   []store.TxView{paid("2025-10-20", -24000)},
			yearly: 24000, steady: 2000, target: 22000, monthly: 24000, catchUp: 1,
		},
		{
			// A year ago to the day is outside, the day after is inside; so is asOf itself.
			name:   "window",
			paid:   []store.TxView{paid("2025-10-05", -99900), paid("2024-03-01", -99900), paid("2026-10-06", -99900), paid("2026-10-05", -12000)},
			saved:  0,
			yearly: 12000, steady: 1000, target: 0, monthly: 1000,
		},
		{
			name:   "rounds up to the cent",
			paid:   []store.TxView{paid("2026-10-01", -10000)},
			yearly: 10000, steady: 834, target: 0, monthly: 834,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p, err := Build(asOf, tt.paid, tt.saved)
			if err != nil {
				t.Fatal(err)
			}
			if p.YearlyCents != tt.yearly || p.SteadyCents != tt.steady || p.TargetCents != tt.target ||
				p.MonthlyCents != tt.monthly || p.CatchUpMonths != tt.catchUp {
				t.Errorf("yearly %d steady %d target %d monthly %d catch-up %d; want %d %d %d %d %d",
					p.YearlyCents, p.SteadyCents, p.TargetCents, p.MonthlyCents, p.CatchUpMonths,
					tt.yearly, tt.steady, tt.target, tt.monthly, tt.catchUp)
			}
		})
	}
}

func TestBuildItems(t *testing.T) {
	p, err := Build("2026-10-05", []store.TxView{
		{ID: 2, BookingDate: "2026-07-10", AmountCents: -60000, Counterparty: "Finanzamt", CategoryName: "Steuern"},
		{ID: 1, BookingDate: "2026-01-10", AmountCents: -30000},
		{ID: 3, BookingDate: "2025-10-20", AmountCents: -24000},
	}, 0)
	if err != nil {
		t.Fatal(err)
	}
	want := []Item{
		{TxID: 3, PaidOn: "2025-10-20", DueOn: "2026-10-20", MonthsLeft: 1, Cents: 24000},
		{TxID: 1, PaidOn: "2026-01-10", DueOn: "2027-01-10", MonthsLeft: 3, Cents: 30000},
		{TxID: 2, PaidOn: "2026-07-10", DueOn: "2027-07-10", MonthsLeft: 9, Cents: 60000, Counterparty: "Finanzamt", Category: "Steuern"},
	}
	if len(p.Items) != len(want) {
		t.Fatalf("%d items, want %d", len(p.Items), len(want))
	}
	for i := range want {
		if p.Items[i] != want[i] {
			t.Errorf("item %d = %+v, want %+v", i, p.Items[i], want[i])
		}
	}
	if p.From != "2025-10-05" {
		t.Errorf("From = %q", p.From)
	}
	if _, err := Build("05.10.2026", nil, 0); err == nil {
		t.Error("Build accepted a date that is not ISO")
	}
}
