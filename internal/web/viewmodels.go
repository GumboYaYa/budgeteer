package web

import (
	"cmp"
	"fmt"
	"slices"
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

// Values of the Optimize page's filters.
const (
	mandatoryHide = "hide"
	mandatoryShow = "show"

	reviewAll       = "all"
	reviewUndecided = "undecided"
)

var reviewFilters = []string{reviewAll, reviewUndecided, store.VerdictKeep, store.VerdictCancel}

// reviewVerdicts maps a review filter to the verdict it selects.
var reviewVerdicts = map[string]string{
	reviewUndecided: "", store.VerdictKeep: store.VerdictKeep, store.VerdictCancel: store.VerdictCancel,
}

func reviewLabel(filter string) string {
	switch filter {
	case reviewUndecided:
		return "Undecided"
	case store.VerdictKeep:
		return "Keep"
	case store.VerdictCancel:
		return "Cancel"
	}
	return "All"
}

// optimizeData is the review of the running recurring expenses. The totals
// cover every such group, whatever the filters show.
type optimizeData struct {
	Categories []optimizeCategory
	// MonthlyCents and YearlyCents sum all groups considered.
	MonthlyCents int64
	YearlyCents  int64
	// Mandatory groups cannot be cancelled.
	MandatoryCount   int
	MandatoryMonthly int64
	// Saving sums the groups marked "cancel".
	SavingMonthly int64
	SavingYearly  int64
	// Undecided groups are neither mandatory nor have a verdict.
	UndecidedCount   int
	UndecidedMonthly int64
	// Unknown counts the active expense groups left out because their
	// interval is not known, Hidden the mandatory ones filtered out.
	Unknown int
	Hidden  int
	// The filters: mandatoryHide or mandatoryShow, and one of reviewFilters.
	Mandatory string
	Review    string
	AsOf      string
	Error     string
}

// Tabs of the Optimize page.
const (
	tabCosts     = "costs"
	tabIncreases = "increases"
)

// optimizePageData holds the data of the tab shown; the other stays empty.
type optimizePageData struct {
	Tab       string
	Costs     optimizeData
	Increases increasesData
	Inbox     int
}

// tabTarget is the element a change made on a tab re-renders.
func tabTarget(tab string) string {
	if tab == tabIncreases {
		return "#increases"
	}
	return "#optimize"
}

// runningExpense reports whether a group is a cost that is still paid: it
// is active, has an interval and its newest transaction is an outflow.
func runningExpense(g store.RecurringGroup) (monthly, yearly int64, ok bool) {
	monthly, ok = g.MonthlyCents()
	if !ok || g.LastCents >= 0 {
		return 0, 0, false
	}
	yearly, _ = g.YearlyCents()
	return monthly, yearly, true
}

// increasesData is the running expenses that got more expensive.
type increasesData struct {
	Rows []increaseRow
	// ExtraYearly is what the rises cost per year in total, MandatoryCount
	// and MandatoryExtra the part of the mandatory groups.
	ExtraYearly    int64
	MandatoryCount int
	MandatoryExtra int64
	// Compared counts the running expenses that have an earlier payment to
	// compare with, risen or not.
	Compared int
	AsOf     string
	Error    string
}

// increaseRow is a group whose price rose. Its figures are positive.
type increaseRow struct {
	store.RecurringCost
	// RiseCents is the rise per payment, ExtraYearly per year. Percent is
	// the rise relative to the earlier payment, 0 if that was no outflow.
	RiseCents   int64
	ExtraYearly int64
	Percent     int64
}

// optimizeCategory is the groups mostly booked to one main category.
type optimizeCategory struct {
	Name         string
	MonthlyCents int64
	YearlyCents  int64
	Rows         []optimizeRow
}

type optimizeRow struct {
	store.RecurringCost
	MonthlyCents int64
	YearlyCents  int64
	// Share is the group's part of all recurring expenses, in percent.
	Share int64
}

// optimizeCategories sorts the rows into their categories, the most
// expensive category and, within each, the most expensive group first.
func optimizeCategories(rows []optimizeRow, totalYearly int64) []optimizeCategory {
	var categories []optimizeCategory
	index := map[int64]int{}
	for _, row := range rows {
		if totalYearly != 0 {
			row.Share = roundDiv(-row.YearlyCents*100, -totalYearly)
		}
		i, ok := index[row.CategoryID]
		if !ok {
			i = len(categories)
			index[row.CategoryID] = i
			name := row.CategoryName
			if row.CategoryID == 0 {
				name = "Uncategorized"
			}
			categories = append(categories, optimizeCategory{Name: name})
		}
		categories[i].MonthlyCents += row.MonthlyCents
		categories[i].YearlyCents += row.YearlyCents
		categories[i].Rows = append(categories[i].Rows, row)
	}
	// Costs are negative: the smallest number is the most expensive.
	slices.SortStableFunc(categories, func(a, b optimizeCategory) int {
		return cmp.Or(cmp.Compare(a.YearlyCents, b.YearlyCents), cmp.Compare(a.Name, b.Name))
	})
	for _, c := range categories {
		slices.SortStableFunc(c.Rows, func(a, b optimizeRow) int {
			return cmp.Or(cmp.Compare(a.YearlyCents, b.YearlyCents), cmp.Compare(a.Name, b.Name))
		})
	}
	return categories
}

// changeLabel says how the price moved since the payment compared with, ""
// if there is none. more is true if the group got more expensive.
func (r optimizeRow) changeLabel() (label string, more bool) {
	change, ok := r.ChangeCents()
	switch {
	case !ok:
		return "", false
	case change == 0:
		return "same as on " + dateDE(r.PrevDate), false
	case change < 0:
		return "+" + money.FormatDE(-change) + " since " + dateDE(r.PrevDate), true
	default:
		return money.FormatDE(-change) + " since " + dateDE(r.PrevDate), false
	}
}

// cancelTitle explains the Cancel button of a group.
func cancelTitle(mandatory bool) string {
	if mandatory {
		return "A mandatory group cannot be cancelled"
	}
	return "Mark to be cancelled"
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
