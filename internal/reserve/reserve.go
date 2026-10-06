// Package reserve calculates how much to set aside each month for expenses
// that come only once or twice a year (taxes, insurances, ...).
//
// The bills are not planned by hand: every transaction marked as an irregular
// expense in the past twelve months is expected again one year after its
// booking date, with the same amount.
package reserve

import (
	"sort"
	"time"

	"github.com/GumboYaYa/budgeteer/internal/store"
)

// Item is one expected bill.
type Item struct {
	TxID         int64
	PaidOn       string // booking date of last year's payment
	DueOn        string // PaidOn plus one year
	MonthsLeft   int    // monthly transfers that can still be made before DueOn, at least 1
	Counterparty string
	Purpose      string
	Category     string
	Cents        int64 // amount to pay, positive; negative for a refund
}

// Plan is the result for one day. All amounts are positive cents.
type Plan struct {
	AsOf  string // the day the plan is calculated for
	From  string // bills paid after this day are taken into account
	Items []Item // ordered by DueOn
	// YearlyCents is the sum of all items, SteadyCents a twelfth of it: the
	// amount that keeps a reserve that is on target on target.
	YearlyCents int64
	SteadyCents int64
	// TargetCents is what the reserve should hold on AsOf if SteadyCents had
	// been saved for every bill since its last payment.
	TargetCents int64
	SavedCents  int64
	// MonthlyCents is the amount to move each month: SteadyCents, or more if
	// a bill comes due before the reserve can cover it at that rate. In that
	// case CatchUpMonths is the number of months the higher amount is needed.
	MonthlyCents  int64
	CatchUpMonths int
}

// Window returns the booking date range (from exclusive, asOf inclusive) of
// the payments a plan for asOf is built from.
func Window(asOf string) (from string, err error) {
	day, err := time.Parse(time.DateOnly, asOf)
	if err != nil {
		return "", err
	}
	return day.AddDate(-1, 0, 0).Format(time.DateOnly), nil
}

// Build calculates the plan for asOf (YYYY-MM-DD) from the marked payments
// and the amount already saved. Payments outside the window are ignored.
func Build(asOf string, paid []store.TxView, savedCents int64) (Plan, error) {
	from, err := Window(asOf)
	if err != nil {
		return Plan{}, err
	}
	day, _ := time.Parse(time.DateOnly, asOf)
	p := Plan{AsOf: asOf, From: from, SavedCents: savedCents}

	// due[k] is the sum of the bills with k months left.
	var due [13]int64
	for _, tx := range paid {
		if tx.BookingDate <= from || tx.BookingDate > asOf {
			continue
		}
		booked, err := time.Parse(time.DateOnly, tx.BookingDate)
		if err != nil {
			return Plan{}, err
		}
		next := booked.AddDate(1, 0, 0)
		left := (next.Year()-day.Year())*12 + int(next.Month()) - int(day.Month())
		left = min(max(left, 1), 12)
		item := Item{
			TxID: tx.ID, PaidOn: tx.BookingDate, DueOn: next.Format(time.DateOnly), MonthsLeft: left,
			Counterparty: tx.Counterparty, Purpose: tx.Purpose, Category: tx.CategoryName, Cents: -tx.AmountCents,
		}
		p.Items = append(p.Items, item)
		p.YearlyCents += item.Cents
		p.TargetCents += item.Cents * int64(12-left)
		due[left] += item.Cents
	}
	sort.SliceStable(p.Items, func(i, j int) bool { return p.Items[i].DueOn < p.Items[j].DueOn })
	p.TargetCents /= 12

	// The bills repeat every year, so in the long run a twelfth of their sum
	// is needed whatever is saved today. On top of that, every bill of the
	// coming year must be covered when it is due: the bills of the next k
	// months minus the savings have to come in within k transfers.
	p.SteadyCents = max(0, ceilDiv(p.YearlyCents, 12))
	p.MonthlyCents = p.SteadyCents
	var cumulative int64
	for k := 1; k <= 12; k++ {
		cumulative += due[k]
		if need := ceilDiv(cumulative-savedCents, int64(k)); need > p.MonthlyCents {
			p.MonthlyCents, p.CatchUpMonths = need, k
		}
	}
	return p, nil
}

// ceilDiv divides rounding up; b must be positive.
func ceilDiv(a, b int64) int64 {
	q := a / b
	if a%b > 0 {
		q++
	}
	return q
}
