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
	Picker     []pickerItem
	Inbox      int
}

type overviewData struct {
	Month      string // YYYY-MM
	Label      string // "September 2026"
	Prev, Next string
	From, To   string
	Income     int64
	Spending   int64 // negative
	Inbox      int
	// Account balances at the end of BalanceDate.
	Balances     []store.AccountBalance
	BalanceTotal int64
	BalanceDate  string
	Categories   []store.CategoryTotal
	Trend        []store.MonthTotal
	Chart        chartData
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
