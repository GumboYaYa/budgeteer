package web

import (
	"cmp"
	"context"
	"errors"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/GumboYaYa/budgeteer/internal/categorize"
	"github.com/GumboYaYa/budgeteer/internal/importer"
	"github.com/GumboYaYa/budgeteer/internal/importer/dkb"
	"github.com/GumboYaYa/budgeteer/internal/importer/finanzguru"
	"github.com/GumboYaYa/budgeteer/internal/reserve"
	"github.com/GumboYaYa/budgeteer/internal/store"
)

const (
	inboxPageSize = 200
	listPageSize  = 100
	maxUploadSize = 64 << 20
	// defaultAvgMonths is the number of months the overview takes its
	// typical month from unless ?avg= chooses one of avgMonthOptions.
	defaultAvgMonths = 6
	// trendMinMonths is the least number of months in the overview's chart.
	trendMinMonths = 12
	// overviewMaxMonths limits the period the overview accepts.
	overviewMaxMonths = 600

	monthLayout = "2006-01"
)

var avgMonthOptions = []int{3, 6, 12}

// --- overview ---------------------------------------------------------------

func (s *server) overview(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	latest, err := s.st.LatestDate(ctx)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	earliest, err := s.st.EarliestDate(ctx)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	if latest == "" {
		latest = time.Now().Format(time.DateOnly)
		earliest = latest
	}
	newest, _ := time.Parse(monthLayout, latest[:7])
	oldest, _ := time.Parse(monthLayout, earliest[:7])
	from, to := overviewRange(r.URL.Query(), newest)
	months := monthsBetween(from, to)
	avg := overviewAvg(r.URL.Query())

	d := overviewData{
		FromMonth: from.Format(monthLayout),
		ToMonth:   to.Format(monthLayout),
		Months:    months,
		Label:     rangeLabel(from, to),
		Prev:      rangeURL(from.AddDate(0, -months, 0), to.AddDate(0, -months, 0), avg),
		Next:      rangeURL(from.AddDate(0, months, 0), to.AddDate(0, months, 0), avg),
		From:      from.Format(time.DateOnly),
		To:        to.AddDate(0, 1, -1).Format(time.DateOnly),
		Avg:       avg,
	}
	for _, n := range avgMonthOptions {
		d.AvgOptions = append(d.AvgOptions, rangePreset{Label: strconv.Itoa(n), URL: rangeURL(from, to, n), Active: n == avg})
	}
	// The shortcuts are counted back from the newest month with transactions.
	for _, p := range []struct {
		label string
		from  time.Time
	}{
		{"Month", newest},
		{"3 months", newest.AddDate(0, -2, 0)},
		{"12 months", newest.AddDate(0, -11, 0)},
		{newest.Format("2006"), time.Date(newest.Year(), time.January, 1, 0, 0, 0, 0, time.UTC)},
		{"All", oldest},
	} {
		d.Presets = append(d.Presets, rangePreset{
			Label: p.label, URL: rangeURL(p.from, newest, avg), Active: p.from.Equal(from) && newest.Equal(to),
		})
	}
	if d.Inbox, err = s.st.CountUncategorized(ctx); err != nil {
		s.fail(w, r, err)
		return
	}
	// Balances at the end of the period, or as of the newest transaction
	// while its last month is still running.
	d.BalanceDate = min(d.To, latest)
	if d.Balances, err = s.st.AccountBalances(ctx, d.BalanceDate); err != nil {
		s.fail(w, r, err)
		return
	}
	for _, b := range d.Balances {
		d.BalanceTotal += b.Cents
	}
	if d.Categories, err = s.st.MainCategoryTotals(ctx, d.From, d.To); err != nil {
		s.fail(w, r, err)
		return
	}

	// The chart shows every month of the period, and at least twelve. The
	// typical month is taken from the avg months before the newest month
	// shown, but not from before the oldest transaction.
	trendFrom := to.AddDate(0, 1-trendMinMonths, 0)
	if from.Before(trendFrom) {
		trendFrom = from
	}
	typicalFrom := to.AddDate(0, -avg, 0)
	if typicalFrom.Before(oldest) {
		typicalFrom = oldest
	}
	typicalTo := to.AddDate(0, -1, 0)
	queryFrom := trendFrom
	if typicalFrom.Before(queryFrom) {
		queryFrom = typicalFrom
	}
	trend, err := s.st.MonthlyTotals(ctx, queryFrom.Format(monthLayout), d.ToMonth)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	// Months without transactions are missing from the query.
	byMonth := make(map[string]store.MonthTotal, len(trend))
	for _, m := range trend {
		byMonth[m.Month] = m
	}
	var all []store.MonthTotal
	for month := queryFrom; !month.After(to); month = month.AddDate(0, 1, 0) {
		m := byMonth[month.Format(monthLayout)]
		m.Month = month.Format(monthLayout)
		all = append(all, m)
	}
	d.Trend = all[monthsBetween(queryFrom, trendFrom)-1:]
	for _, m := range d.Trend[len(d.Trend)-months:] {
		d.Income.Cents += m.IncomeCents
		d.RegularIncome.Cents += m.RegularIncomeCents
		d.Spending += m.SpendingCents
	}
	// For one month the income boxes show the month before, which is
	// complete.
	if months == 1 {
		before := d.Trend[len(d.Trend)-2]
		d.ShowIncomeLast = true
		d.LastMonthNote = "last month, " + from.AddDate(0, -1, 0).Format("January 2006")
		d.Income.Last = before.IncomeCents
		d.RegularIncome.Last = before.RegularIncomeCents
	}
	if !typicalFrom.After(typicalTo) {
		window := all[monthsBetween(queryFrom, typicalFrom)-1 : monthsBetween(queryFrom, typicalTo)]
		cats, err := s.st.MainCategoryMonthlyTotals(ctx, typicalFrom.Format(monthLayout), typicalTo.Format(monthLayout))
		if err != nil {
			s.fail(w, r, err)
			return
		}
		d.Typical = typicalOf(window, cats)
		d.Typical.From = typicalFrom.Format(monthLayout)
		d.Typical.Label = rangeLabelShort(typicalFrom, typicalTo)
		d.Categories = withTypicalCategories(d.Categories, cats, d.Typical.Categories)
	}
	d.Chart = buildCharts(d)

	s.render(w, r, overviewPage(d))
}

// overviewAvg reads the number of months the typical month is taken from.
func overviewAvg(q url.Values) int {
	n, err := strconv.Atoi(q.Get("avg"))
	if err == nil && slices.Contains(avgMonthOptions, n) {
		return n
	}
	return defaultAvgMonths
}

// typicalOf takes the medians of the months of the window, oldest first,
// and of the category totals of the same months.
func typicalOf(window []store.MonthTotal, cats []store.CategoryMonthTotal) typicalMonth {
	t := typicalMonth{Months: len(window), Categories: map[int64]int64{}}
	var income, regular, spending, net []int64
	index := make(map[string]int, len(window))
	for i, m := range window {
		index[m.Month] = i
		income = append(income, m.IncomeCents)
		regular = append(regular, m.RegularIncomeCents)
		spending = append(spending, m.SpendingCents)
		net = append(net, m.IncomeCents+m.SpendingCents)
	}
	t.Income, t.RegularIncome, t.Spending, t.Net = median(income), median(regular), median(spending), median(net)
	perCategory := map[int64][]int64{}
	for _, c := range cats {
		if perCategory[c.CategoryID] == nil {
			perCategory[c.CategoryID] = make([]int64, len(window))
		}
		perCategory[c.CategoryID][index[c.Month]] = c.Cents
	}
	for id, xs := range perCategory {
		t.Categories[id] = median(xs)
	}
	return t
}

// withTypicalCategories adds the main categories that are typical but have
// no transactions in the period, so their absence shows; they come last,
// most negative first.
func withTypicalCategories(totals []store.CategoryTotal, cats []store.CategoryMonthTotal, typical map[int64]int64) []store.CategoryTotal {
	shown := map[int64]bool{}
	for _, c := range totals {
		shown[c.CategoryID] = true
	}
	var missing []store.CategoryTotal
	for _, c := range cats {
		if !shown[c.CategoryID] && typical[c.CategoryID] != 0 {
			shown[c.CategoryID] = true
			missing = append(missing, store.CategoryTotal{CategoryID: c.CategoryID, Name: c.Name})
		}
	}
	slices.SortFunc(missing, func(a, b store.CategoryTotal) int {
		return cmp.Or(cmp.Compare(typical[a.CategoryID], typical[b.CategoryID]), cmp.Compare(a.Name, b.Name))
	})
	return append(totals, missing...)
}

// overviewRange reads the months to show: ?month= for a single one, or ?from=
// and ?to=. Without a valid month it is the newest one with transactions.
func overviewRange(q url.Values, newest time.Time) (from, to time.Time) {
	if month, err := time.Parse(monthLayout, q.Get("month")); err == nil {
		return month, month
	}
	from, errFrom := time.Parse(monthLayout, q.Get("from"))
	to, errTo := time.Parse(monthLayout, q.Get("to"))
	switch {
	case errFrom != nil && errTo != nil:
		return newest, newest
	case errFrom != nil:
		from = to
	case errTo != nil:
		to = from
	}
	if from.After(to) {
		from, to = to, from
	}
	if monthsBetween(from, to) > overviewMaxMonths {
		from = to.AddDate(0, 1-overviewMaxMonths, 0)
	}
	return from, to
}

// --- inbox and transaction list ---------------------------------------------

func (s *server) inbox(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	page, err := s.st.ListTransactions(ctx, store.TxFilter{Status: store.StatusUncategorized, Limit: inboxPageSize})
	if err != nil {
		s.fail(w, r, err)
		return
	}
	if err := s.addSuggestions(ctx, page.Rows); err != nil {
		s.fail(w, r, err)
		return
	}
	picker, err := s.pickerCategories(r)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	groups, err := s.st.ListRecurringGroups(ctx)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	s.render(w, r, inboxPage(inboxData{Page: page, Picker: picker, Groups: groups}))
}

func (s *server) transactions(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	q := r.URL.Query()
	d := listData{
		Filter: store.TxFilter{
			AccountSlug: q.Get("account"),
			From:        q.Get("from"),
			To:          q.Get("to"),
			Text:        q.Get("q"),
			Status:      q.Get("status"),
			Limit:       listPageSize,
		},
		PageNo: 1,
	}
	d.Filter.CategoryID, _ = strconv.ParseInt(q.Get("category"), 10, 64)
	if named, ok := recurringFilters[q.Get("recurring")]; ok {
		d.Filter.Recurring = named
	} else if id, err := strconv.ParseInt(q.Get("recurring"), 10, 64); err == nil && id > 0 {
		d.Filter.Recurring = id
	}
	if n, err := strconv.Atoi(q.Get("page")); err == nil && n > 1 {
		d.PageNo = n
	}
	d.Filter.Offset = (d.PageNo - 1) * listPageSize

	var err error
	if d.Page, err = s.st.ListTransactions(ctx, d.Filter); err != nil {
		s.fail(w, r, err)
		return
	}
	d.Pages = max(1, (d.Page.Total+listPageSize-1)/listPageSize)
	pageURL := func(n int) string {
		v := url.Values{}
		for key, values := range q {
			if key != "page" && len(values) > 0 && values[0] != "" {
				v.Set(key, values[0])
			}
		}
		if n > 1 {
			v.Set("page", strconv.Itoa(n))
		}
		if len(v) == 0 {
			return "/transactions"
		}
		return "/transactions?" + v.Encode()
	}
	if d.PageNo > 1 {
		d.PrevURL = pageURL(d.PageNo - 1)
	}
	if d.PageNo < d.Pages {
		d.NextURL = pageURL(d.PageNo + 1)
	}

	// The filter form only replaces the result list.
	if r.Header.Get("HX-Target") == "results" {
		s.render(w, r, listResults(d))
		return
	}

	if d.Accounts, err = s.st.ListAccounts(ctx); err != nil {
		s.fail(w, r, err)
		return
	}
	if d.Picker, err = s.pickerCategories(r); err != nil {
		s.fail(w, r, err)
		return
	}
	if d.Categories, err = s.st.ListCategories(ctx); err != nil {
		s.fail(w, r, err)
		return
	}
	if d.Groups, err = s.st.ListRecurringGroups(ctx); err != nil {
		s.fail(w, r, err)
		return
	}
	if d.Inbox, err = s.st.CountUncategorized(ctx); err != nil {
		s.fail(w, r, err)
		return
	}
	s.render(w, r, listPage(d))
}

// addSuggestions fills in the category that history suggests for the
// uncategorized ones among rows.
func (s *server) addSuggestions(ctx context.Context, rows []store.TxView) error {
	ids := make([]int64, 0, len(rows))
	for _, v := range rows {
		if v.CategoryID == 0 && !v.IsTransfer {
			ids = append(ids, v.ID)
		}
	}
	suggestions, err := categorize.Suggest(ctx, s.st, ids)
	if err != nil || len(suggestions) == 0 {
		return err
	}
	categories, err := s.st.ListCategories(ctx)
	if err != nil {
		return err
	}
	names := make(map[int64]string, len(categories))
	for _, c := range categories {
		names[c.ID] = c.FullName()
	}
	for i, v := range rows {
		if p, ok := suggestions[v.ID]; ok {
			rows[i].SuggestedID, rows[i].SuggestedName = p.CategoryID, names[p.CategoryID]
		}
	}
	return nil
}

// pickerCategories returns all categories for the picker, the most recently
// used ones first.
func (s *server) pickerCategories(r *http.Request) ([]pickerItem, error) {
	ctx := r.Context()
	all, err := s.st.ListCategories(ctx)
	if err != nil {
		return nil, err
	}
	recent, err := s.st.RecentCategoryIDs(ctx, 8)
	if err != nil {
		return nil, err
	}
	byID := make(map[int64]store.Category, len(all))
	for _, c := range all {
		byID[c.ID] = c
	}
	items := make([]pickerItem, 0, len(all))
	isRecent := make(map[int64]bool, len(recent))
	for _, id := range recent {
		if c, ok := byID[id]; ok {
			isRecent[id] = true
			items = append(items, pickerItem{Category: c, Recent: true})
		}
	}
	for _, c := range all {
		if !isRecent[c.ID] {
			items = append(items, pickerItem{Category: c})
		}
	}
	return items, nil
}

// --- actions on transactions ------------------------------------------------

// respondRows answers an action. The inbox only needs the new inbox size (it
// removes the rows itself); the transaction list gets the updated rows back.
func (s *server) respondRows(w http.ResponseWriter, r *http.Request, txIDs []int64) {
	ctx := r.Context()
	count, err := s.st.CountUncategorized(ctx)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	w.Header().Set("X-Inbox-Count", strconv.Itoa(count))
	if r.PostFormValue("view") != "list" {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	s.renderRows(w, r, txIDs, true)
}

// renderRows answers with the current markup of the given transactions.
func (s *server) renderRows(w http.ResponseWriter, r *http.Request, txIDs []int64, withCategory bool) {
	rows := make([]store.TxView, 0, len(txIDs))
	for _, id := range txIDs {
		v, err := s.st.TxViewByID(r.Context(), id)
		if err != nil {
			s.fail(w, r, err)
			return
		}
		rows = append(rows, v)
	}
	if !withCategory {
		if err := s.addSuggestions(r.Context(), rows); err != nil {
			s.fail(w, r, err)
			return
		}
	}
	s.render(w, r, txRows(rows, withCategory))
}

func (s *server) categorize(w http.ResponseWriter, r *http.Request) {
	txIDs, err := ids(r)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	categoryID, err := formID(r, "category_id")
	if err != nil {
		s.fail(w, r, err)
		return
	}
	err = s.st.InTx(r.Context(), func(tx *store.Store) error {
		for _, id := range txIDs {
			if err := tx.SetCategory(r.Context(), id, categoryID); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		s.fail(w, r, err)
		return
	}
	s.respondRows(w, r, txIDs)
}

func (s *server) transfer(w http.ResponseWriter, r *http.Request) {
	txIDs, err := ids(r)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	value := r.PostFormValue("value") == "1"
	err = s.st.InTx(r.Context(), func(tx *store.Store) error {
		for _, id := range txIDs {
			if err := tx.SetTransfer(r.Context(), id, value); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		s.fail(w, r, err)
		return
	}
	s.respondRows(w, r, txIDs)
}

// undo reverts an inbox action: it removes the category again or clears the
// transfer flag, which puts the transactions back into the inbox.
func (s *server) undo(w http.ResponseWriter, r *http.Request) {
	txIDs, err := ids(r)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	kind := r.PostFormValue("kind")
	if kind != "category" && kind != "transfer" {
		s.fail(w, r, badRequestf("unknown undo kind %q", kind))
		return
	}
	err = s.st.InTx(r.Context(), func(tx *store.Store) error {
		for _, id := range txIDs {
			var err error
			if kind == "category" {
				err = tx.ClearCategory(r.Context(), id)
			} else {
				err = tx.SetTransfer(r.Context(), id, false)
			}
			if err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		s.fail(w, r, err)
		return
	}
	s.respondRows(w, r, txIDs)
}

// reserveMark marks or unmarks transactions as irregular expenses. The rows
// stay where they are, also in the inbox, so the fresh rows are sent back for
// both views.
func (s *server) reserveMark(w http.ResponseWriter, r *http.Request) {
	txIDs, err := ids(r)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	value := r.PostFormValue("value") == "1"
	err = s.st.InTx(r.Context(), func(tx *store.Store) error {
		for _, id := range txIDs {
			if err := tx.SetReserve(r.Context(), id, value); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		s.fail(w, r, err)
		return
	}
	s.renderRows(w, r, txIDs, r.PostFormValue("view") == "list")
}

// recurringAssign puts transactions into a recurring group: the one with
// group_id, or the one called name, which is created if needed. group_id 0
// takes them out of their group. The rows stay in place and come back fresh.
func (s *server) recurringAssign(w http.ResponseWriter, r *http.Request) {
	txIDs, err := ids(r)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	var groupID int64
	err = s.st.InTx(r.Context(), func(tx *store.Store) error {
		if name := strings.TrimSpace(r.PostFormValue("name")); name != "" {
			if groupID, err = tx.EnsureRecurringGroup(r.Context(), name); err != nil {
				return err
			}
		} else if groupID, err = formID(r, "group_id"); err != nil {
			return err
		}
		for _, id := range txIDs {
			if err := tx.SetRecurringGroup(r.Context(), id, groupID); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		s.fail(w, r, err)
		return
	}
	w.Header().Set("X-Group-Id", strconv.FormatInt(groupID, 10))
	s.renderRows(w, r, txIDs, r.PostFormValue("view") == "list")
}

// --- recurring groups -------------------------------------------------------

func (s *server) recurringData(r *http.Request, problem string) (recurringData, error) {
	ctx := r.Context()
	d := recurringData{Error: problem}
	var err error
	if d.Groups, err = s.st.ListRecurringGroups(ctx); err != nil {
		return d, err
	}
	if d.AsOf, err = s.st.LatestDate(ctx); err != nil {
		return d, err
	}
	for _, g := range d.Groups {
		switch cents, ok := g.MonthlyCents(); {
		case ok:
			d.MonthlyCents += cents
		case !g.Active:
			d.Inactive++
		case g.Count > 0:
			d.Unknown++
		}
	}
	d.Inbox, err = s.st.CountUncategorized(ctx)
	return d, err
}

func (s *server) recurring(w http.ResponseWriter, r *http.Request) {
	d, err := s.recurringData(r, "")
	if err != nil {
		s.fail(w, r, err)
		return
	}
	s.render(w, r, recurringPage(d))
}

// recurringAction runs a change on the group in the path (0 if the path has
// none) and re-renders the group list, with the error shown above it if the
// change was refused. Posted from the Optimize page (view=optimize), it
// re-renders the content of that page's tab instead.
func (s *server) recurringAction(w http.ResponseWriter, r *http.Request, change func(id int64) error) {
	problem := ""
	id, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err := change(id); err != nil {
		var bad badRequest
		if errors.Is(err, store.ErrNotFound) {
			problem = "That group no longer exists."
		} else if errors.As(err, &bad) || !isInternal(err) {
			problem = err.Error()
		} else {
			s.fail(w, r, err)
			return
		}
	}
	if r.FormValue("view") == "optimize" && r.FormValue("tab") == tabIncreases {
		d, err := s.increasesData(r, problem)
		if err != nil {
			s.fail(w, r, err)
			return
		}
		s.render(w, r, increasesSection(d))
		return
	}
	if r.FormValue("view") == "optimize" {
		d, err := s.optimizeData(r, problem)
		if err != nil {
			s.fail(w, r, err)
			return
		}
		s.render(w, r, optimizeSection(d))
		return
	}
	d, err := s.recurringData(r, problem)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	s.render(w, r, recurringSection(d))
}

func (s *server) recurringCreate(w http.ResponseWriter, r *http.Request) {
	s.recurringAction(w, r, func(int64) error {
		id, err := s.st.EnsureRecurringGroup(r.Context(), r.FormValue("name"))
		if err != nil {
			return err
		}
		return s.st.SetRecurringInterval(r.Context(), id, r.FormValue("interval"))
	})
}

func (s *server) recurringRename(w http.ResponseWriter, r *http.Request) {
	s.recurringAction(w, r, func(id int64) error {
		return s.st.RenameRecurringGroup(r.Context(), id, r.FormValue("name"))
	})
}

func (s *server) recurringInterval(w http.ResponseWriter, r *http.Request) {
	s.recurringAction(w, r, func(id int64) error {
		return s.st.SetRecurringInterval(r.Context(), id, r.FormValue("interval"))
	})
}

func (s *server) recurringReserve(w http.ResponseWriter, r *http.Request) {
	s.recurringAction(w, r, func(id int64) error {
		return s.st.SetRecurringReserve(r.Context(), id, r.FormValue("value") == "1")
	})
}

func (s *server) recurringActive(w http.ResponseWriter, r *http.Request) {
	s.recurringAction(w, r, func(id int64) error {
		return s.st.SetRecurringActive(r.Context(), id, r.FormValue("value") == "1")
	})
}

func (s *server) recurringMandatory(w http.ResponseWriter, r *http.Request) {
	s.recurringAction(w, r, func(id int64) error {
		return s.st.SetRecurringMandatory(r.Context(), id, r.FormValue("value") == "1")
	})
}

func (s *server) recurringVerdict(w http.ResponseWriter, r *http.Request) {
	s.recurringAction(w, r, func(id int64) error {
		return s.st.SetRecurringVerdict(r.Context(), id, r.FormValue("verdict"))
	})
}

func (s *server) recurringMerge(w http.ResponseWriter, r *http.Request) {
	s.recurringAction(w, r, func(id int64) error {
		intoID, err := formID(r, "into_id")
		if err != nil {
			return badRequestf("choose the group to merge into")
		}
		return s.st.MergeRecurringGroup(r.Context(), id, intoID)
	})
}

func (s *server) recurringDelete(w http.ResponseWriter, r *http.Request) {
	s.recurringAction(w, r, func(id int64) error {
		return s.st.DeleteRecurringGroup(r.Context(), id)
	})
}

// --- optimize ---------------------------------------------------------------

// optimizeData collects the running expenses for the review: the active
// groups with a known interval whose newest transaction is an outflow. The
// totals cover all of them; the filters (mandatory, review) only choose the
// rows shown.
func (s *server) optimizeData(r *http.Request, problem string) (optimizeData, error) {
	ctx := r.Context()
	d := optimizeData{Error: problem, Mandatory: mandatoryHide, Review: r.FormValue("review")}
	if r.FormValue("mandatory") == mandatoryShow {
		d.Mandatory = mandatoryShow
	}
	if !slices.Contains(reviewFilters, d.Review) {
		d.Review = reviewAll
	}
	var err error
	if d.AsOf, err = s.st.LatestDate(ctx); err != nil {
		return d, err
	}
	costs, err := s.st.ListRecurringCosts(ctx, d.AsOf)
	if err != nil {
		return d, err
	}
	var rows []optimizeRow
	for _, c := range costs {
		monthly, yearly, ok := runningExpense(c.RecurringGroup)
		if !ok {
			if c.Active && c.Count > 0 && c.LastCents < 0 {
				d.Unknown++
			}
			continue
		}
		d.MonthlyCents += monthly
		d.YearlyCents += yearly
		switch {
		case c.Mandatory:
			d.MandatoryCount++
			d.MandatoryMonthly += monthly
			d.MandatoryYearly += yearly
		case c.Verdict == store.VerdictCancel:
			d.SavingMonthly += monthly
			d.SavingYearly += yearly
		case c.Verdict == "":
			d.UndecidedCount++
			d.UndecidedMonthly += monthly
			d.UndecidedYearly += yearly
		}
		if c.Mandatory && d.Mandatory == mandatoryHide {
			d.Hidden++
			continue
		}
		if d.Review != reviewAll && c.Verdict != reviewVerdicts[d.Review] {
			continue
		}
		rows = append(rows, optimizeRow{RecurringCost: c, MonthlyCents: monthly, YearlyCents: yearly})
	}
	d.Categories = optimizeCategories(rows, d.YearlyCents)
	return d, nil
}

// increasesData collects the running expenses that got more expensive,
// mandatory ones included, the highest rise in percent first.
func (s *server) increasesData(r *http.Request, problem string) (increasesData, error) {
	ctx := r.Context()
	d := increasesData{Error: problem}
	var err error
	if d.AsOf, err = s.st.LatestDate(ctx); err != nil {
		return d, err
	}
	costs, err := s.st.ListRecurringCosts(ctx, d.AsOf)
	if err != nil {
		return d, err
	}
	for _, c := range costs {
		if _, _, ok := runningExpense(c.RecurringGroup); !ok {
			continue
		}
		change, ok := c.ChangeCents()
		if !ok {
			continue
		}
		d.Compared++
		if change >= 0 {
			continue
		}
		yearly, _ := c.YearlyChangeCents()
		row := increaseRow{RecurringCost: c, RiseCents: -change, ExtraYearly: -yearly}
		if c.PrevCents < 0 {
			row.Percent = roundDiv(-change*100, -c.PrevCents)
		}
		d.ExtraYearly += row.ExtraYearly
		if c.Mandatory {
			d.MandatoryCount++
			d.MandatoryExtra += row.ExtraYearly
		}
		d.Rows = append(d.Rows, row)
	}
	slices.SortStableFunc(d.Rows, func(a, b increaseRow) int {
		return cmp.Or(cmp.Compare(b.Percent, a.Percent), cmp.Compare(b.ExtraYearly, a.ExtraYearly), cmp.Compare(a.Name, b.Name))
	})
	return d, nil
}

// optimize shows one of the page's two tabs.
func (s *server) optimize(w http.ResponseWriter, r *http.Request) {
	d := optimizePageData{Tab: tabCosts}
	var err error
	if r.FormValue("tab") == tabIncreases {
		d.Tab = tabIncreases
		d.Increases, err = s.increasesData(r, "")
	} else {
		d.Costs, err = s.optimizeData(r, "")
	}
	if err == nil {
		d.Inbox, err = s.st.CountUncategorized(r.Context())
	}
	if err != nil {
		s.fail(w, r, err)
		return
	}
	s.render(w, r, optimizePage(d))
}

// --- reserve ----------------------------------------------------------------

func (s *server) reservePage(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	var d reserveData
	var err error
	if d.Status, err = reserve.Load(ctx, s.st); err != nil {
		s.fail(w, r, err)
		return
	}
	if d.Accounts, err = s.st.ListAccounts(ctx); err != nil {
		s.fail(w, r, err)
		return
	}
	if d.Inbox, err = s.st.CountUncategorized(ctx); err != nil {
		s.fail(w, r, err)
		return
	}
	s.render(w, r, reservePage(d))
}

// reserveAccount chooses the account that holds the reserve; an empty slug
// means none.
func (s *server) reserveAccount(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	var id int64
	if slug := r.PostFormValue("account"); slug != "" {
		a, err := s.st.AccountBySlug(ctx, slug)
		if err != nil {
			s.fail(w, r, err)
			return
		}
		id = a.ID
	}
	if err := s.st.SetReserveAccount(ctx, id); err != nil {
		s.fail(w, r, err)
		return
	}
	http.Redirect(w, r, "/reserve", http.StatusSeeOther)
}

// --- import -----------------------------------------------------------------

func (s *server) importPage(w http.ResponseWriter, r *http.Request) {
	count, err := s.st.CountUncategorized(r.Context())
	if err != nil {
		s.fail(w, r, err)
		return
	}
	accounts, err := s.st.ListAccounts(r.Context())
	if err != nil {
		s.fail(w, r, err)
		return
	}
	s.render(w, r, importPage(count, accounts))
}

func (s *server) importUpload(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, maxUploadSize)
	file, header, err := r.FormFile("file")
	if err != nil {
		s.render(w, r, importResult(importer.Summary{}, "Choose a file to import."))
		return
	}
	defer file.Close()

	opts := importer.Options{RawDir: s.rawDir, Force: r.FormValue("force") == "1"}
	var parser importer.Parser
	switch source := r.FormValue("source"); source {
	case "", finanzguru.Source:
		parser = finanzguru.Parser{}
		opts.Cutover = r.FormValue("cutover")
	case dkb.Source:
		opts.Account = r.FormValue("account")
		if opts.Account == "" {
			s.render(w, r, importResult(importer.Summary{}, "Choose the account this DKB export belongs to."))
			return
		}
		parser = dkb.Parser{Account: opts.Account}
	default:
		s.fail(w, r, badRequestf("unknown import source %q", source))
		return
	}
	summary, err := importer.Run(r.Context(), s.st, parser, file, header.Filename, opts)
	if err != nil {
		// Import errors describe the file (line, column) and are meant for the user.
		s.render(w, r, importResult(importer.Summary{}, err.Error()))
		return
	}
	s.render(w, r, importResult(summary, ""))
}

// --- categories -------------------------------------------------------------

func (s *server) categoriesData(r *http.Request, problem string) (categoriesData, error) {
	ctx := r.Context()
	d := categoriesData{Error: problem}
	var err error
	if d.Categories, err = s.st.ListCategories(ctx); err != nil {
		return d, err
	}
	for _, c := range d.Categories {
		if c.ParentID == 0 {
			d.Mains = append(d.Mains, c)
		}
	}
	d.Inbox, err = s.st.CountUncategorized(ctx)
	return d, err
}

func (s *server) categories(w http.ResponseWriter, r *http.Request) {
	d, err := s.categoriesData(r, "")
	if err != nil {
		s.fail(w, r, err)
		return
	}
	s.render(w, r, categoriesPage(d))
}

// categoryAction runs a change and re-renders the category list, with the
// error shown above it if the change was refused.
func (s *server) categoryAction(w http.ResponseWriter, r *http.Request, change func() error) {
	problem := ""
	if err := change(); err != nil {
		var bad badRequest
		if errors.Is(err, store.ErrNotFound) {
			problem = "That category no longer exists."
		} else if errors.As(err, &bad) || !isInternal(err) {
			problem = err.Error()
		} else {
			s.fail(w, r, err)
			return
		}
	}
	d, err := s.categoriesData(r, problem)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	s.render(w, r, categoriesSection(d))
}

// isInternal reports whether an error comes from the database layer rather
// than from a rule the user can act on.
func isInternal(err error) bool {
	msg := err.Error()
	return len(msg) >= 6 && msg[:6] == "store:"
}

func (s *server) categoryCreate(w http.ResponseWriter, r *http.Request) {
	s.categoryAction(w, r, func() error {
		parentID, _ := strconv.ParseInt(r.FormValue("parent_id"), 10, 64)
		_, err := s.st.CreateCategory(r.Context(), r.FormValue("name"), parentID)
		return err
	})
}

func (s *server) categoryRename(w http.ResponseWriter, r *http.Request) {
	s.categoryAction(w, r, func() error {
		id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
		if err != nil {
			return store.ErrNotFound
		}
		return s.st.RenameCategory(r.Context(), id, r.FormValue("name"))
	})
}

func (s *server) categoryMove(w http.ResponseWriter, r *http.Request) {
	s.categoryAction(w, r, func() error {
		id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
		if err != nil {
			return store.ErrNotFound
		}
		parentID, err := formID(r, "parent_id")
		if err != nil {
			return err
		}
		return s.st.MoveCategory(r.Context(), id, parentID)
	})
}

func (s *server) categoryMerge(w http.ResponseWriter, r *http.Request) {
	s.categoryAction(w, r, func() error {
		id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
		if err != nil {
			return store.ErrNotFound
		}
		intoID, err := formID(r, "into_id")
		if err != nil {
			return badRequestf("choose the category to merge into")
		}
		return s.st.MergeCategory(r.Context(), id, intoID)
	})
}
