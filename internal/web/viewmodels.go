package web

import (
	"fmt"
	"strconv"
	"time"

	"github.com/GumboYaYa/budgeteer/internal/money"
	"github.com/GumboYaYa/budgeteer/internal/reserve"
	"github.com/GumboYaYa/budgeteer/internal/store"
)

type pickerItem struct {
	store.Category
	Recent bool
}

type inboxData struct {
	Page   store.TxPage
	Picker []pickerItem
	Groups []store.RecurringGroup
}

type recurringData struct {
	Groups []store.RecurringGroup
	// MonthlyCents sums what the active groups cost per month; Unknown
	// counts the active groups left out because their interval is not known,
	// Inactive the groups that have ended.
	MonthlyCents int64
	Unknown      int
	Inactive     int
	// AsOf is the newest booking date; groups without a payment for long
	// before it are pointed out.
	AsOf  string
	Error string
	Inbox int
}

// intervalLabel names an interval for the UI.
func intervalLabel(interval string) string {
	switch interval {
	case store.IntervalMonthly:
		return "Monthly"
	case store.IntervalQuarterly:
		return "Quarterly"
	case store.IntervalHalfYearly:
		return "Half-yearly"
	case store.IntervalYearly:
		return "Yearly"
	}
	return "Not known"
}

// Values of the list's "recurring" filter besides a group id.
const (
	recurringAny      = "any"
	recurringActive   = "active"
	recurringInactive = "inactive"
)

var recurringFilters = map[string]int64{
	recurringAny: store.AnyRecurring, recurringActive: store.ActiveRecurring, recurringInactive: store.InactiveRecurring,
}

type listData struct {
	Filter     store.TxFilter
	Page       store.TxPage
	PageNo     int
	Pages      int
	PrevURL    string
	NextURL    string
	Accounts   []store.Account
	Categories []store.Category
	Groups     []store.RecurringGroup
	Picker     []pickerItem
	Inbox      int
}

type overviewData struct {
	// The period shown: whole months, FromMonth to ToMonth inclusive.
	FromMonth, ToMonth string // YYYY-MM
	Months             int
	Label              string // "September 2026" or "July – September 2026"
	Prev, Next         string // URLs of the periods of the same length before and after
	Presets            []rangePreset
	From, To           string // first and last day
	Income             int64
	Spending           int64 // negative
	// IncomeAverage is a monthly average shown next to the income;
	// IncomeAverageNote says over which months.
	IncomeAverage     int64
	IncomeAverageNote string
	Inbox             int
	// Account balances at the end of BalanceDate.
	Balances     []store.AccountBalance
	BalanceTotal int64
	BalanceDate  string
	Categories   []store.CategoryTotal
	Trend        []store.MonthTotal
	Chart        chartData
}

// rangePreset is a shortcut to a period of the overview.
type rangePreset struct {
	Label  string
	URL    string
	Active bool
}

// periodNote is the line below a figure of the overview.
func (d overviewData) periodNote() string {
	if d.Months == 1 {
		return "this month"
	}
	return "in these " + strconv.Itoa(d.Months) + " months"
}

// monthsBetween counts the months from one month to another, both included.
func monthsBetween(from, to time.Time) int {
	return (to.Year()-from.Year())*12 + int(to.Month()) - int(from.Month()) + 1
}

// rangeLabel names a period of whole months.
func rangeLabel(from, to time.Time) string {
	switch {
	case from.Equal(to):
		return to.Format("January 2006")
	case from.Year() == to.Year():
		return from.Format("January") + " – " + to.Format("January 2006")
	default:
		return from.Format("January 2006") + " – " + to.Format("January 2006")
	}
}

// rangeURL links to the overview of a period.
func rangeURL(from, to time.Time) string {
	if from.Equal(to) {
		return "/?month=" + to.Format(monthLayout)
	}
	return "/?from=" + from.Format(monthLayout) + "&to=" + to.Format(monthLayout)
}

// roundDiv divides rounding to the nearest; b must be positive.
func roundDiv(a, b int64) int64 {
	if a < 0 {
		return -((-a + b/2) / b)
	}
	return (a + b/2) / b
}

type categoriesData struct {
	Categories []store.Category
	Mains      []store.Category
	Error      string
	Inbox      int
}

type reserveData struct {
	reserve.Status
	Accounts []store.Account
	Inbox    int
}

// monthlyNote explains the monthly amount below its figure.
func (d reserveData) monthlyNote() string {
	switch {
	case len(d.Items) == 0:
		return "nothing marked yet"
	case d.CatchUpMonths == 0:
		return "a twelfth of the bills of a year"
	case d.CatchUpMonths == 1:
		return "this month, to cover the next bill"
	default:
		return "for " + strconv.Itoa(d.CatchUpMonths) + " months, to cover the bills due until then"
	}
}

// savedNote says where the saved amount comes from.
func (d reserveData) savedNote() string {
	switch {
	case !d.HasAccount:
		return "no reserve account chosen"
	case !d.BalanceKnown:
		return "balance of " + d.Account.Name + " is not known"
	default:
		return d.Account.Name + " on " + dateDE(d.AsOf)
	}
}

// targetNote compares the saved amount with the target.
func (d reserveData) targetNote() string {
	switch diff := d.SavedCents - d.TargetCents; {
	case diff > 0:
		return money.FormatDE(diff) + " ahead"
	case diff < 0:
		return money.FormatDE(-diff) + " behind"
	default:
		return "on target"
	}
}

// chartData is handed to the browser as JSON. Values are euros and for
// drawing only; all figures shown as text come from integer cents.
type chartData struct {
	Spending struct {
		Labels []string  `json:"labels"`
		Values []float64 `json:"values"`
	} `json:"spending"`
	Trend struct {
		Labels   []string  `json:"labels"`
		Income   []float64 `json:"income"`
		Spending []float64 `json:"spending"`
	} `json:"trend"`
}

func buildCharts(d overviewData) chartData {
	var c chartData
	// Categories arrive most negative first, so the biggest spending is on top.
	for _, cat := range d.Categories {
		if cat.Cents < 0 {
			c.Spending.Labels = append(c.Spending.Labels, categoryLabel(cat))
			c.Spending.Values = append(c.Spending.Values, float64(-cat.Cents)/100)
		}
	}
	for _, m := range d.Trend {
		c.Trend.Labels = append(c.Trend.Labels, monthShort(m.Month))
		c.Trend.Income = append(c.Trend.Income, float64(m.IncomeCents)/100)
		c.Trend.Spending = append(c.Trend.Spending, float64(-m.SpendingCents)/100)
	}
	return c
}

func categoryLabel(c store.CategoryTotal) string {
	if c.CategoryID == 0 {
		return "Uncategorized"
	}
	return c.Name
}

// dateDE renders YYYY-MM-DD as dd.mm.yyyy.
func dateDE(iso string) string {
	t, err := time.Parse(time.DateOnly, iso)
	if err != nil {
		return iso
	}
	return t.Format("02.01.2006")
}

// monthShort renders YYYY-MM as "Sep 26".
func monthShort(month string) string {
	t, err := time.Parse("2006-01", month)
	if err != nil {
		return month
	}
	return t.Format("Jan 06")
}

func itoa(n int64) string { return strconv.FormatInt(n, 10) }

func rowID(id int64) string { return "tx-" + itoa(id) }

// categoryURL links a main category of the overview to its transactions.
func categoryURL(c store.CategoryTotal, from, to string) string {
	if c.CategoryID == 0 {
		return fmt.Sprintf("/transactions?status=%s&from=%s&to=%s", store.StatusUncategorized, from, to)
	}
	return fmt.Sprintf("/transactions?category=%d&from=%s&to=%s", c.CategoryID, from, to)
}

func plural(n int, one, many string) string {
	if n == 1 {
		return "1 " + one
	}
	return strconv.Itoa(n) + " " + many
}
