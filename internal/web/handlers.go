package web

import (
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/GumboYaYa/budgeteer/internal/importer"
	"github.com/GumboYaYa/budgeteer/internal/importer/dkb"
	"github.com/GumboYaYa/budgeteer/internal/importer/finanzguru"
	"github.com/GumboYaYa/budgeteer/internal/store"
)

const (
	inboxPageSize = 200
	listPageSize  = 100
	maxUploadSize = 64 << 20
)

// --- overview ---------------------------------------------------------------

func (s *server) overview(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	latest, err := s.st.LatestDate(ctx)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	if latest == "" {
		latest = time.Now().Format(time.DateOnly)
	}
	start, err := time.Parse("2006-01", r.URL.Query().Get("month"))
	if err != nil {
		start, _ = time.Parse("2006-01", latest[:7])
	}

	d := overviewData{
		Month: start.Format("2006-01"),
		Label: start.Format("January 2006"),
		Prev:  start.AddDate(0, -1, 0).Format("2006-01"),
		Next:  start.AddDate(0, 1, 0).Format("2006-01"),
		From:  start.Format(time.DateOnly),
		To:    start.AddDate(0, 1, -1).Format(time.DateOnly),
	}
	if d.Inbox, err = s.st.CountUncategorized(ctx); err != nil {
		s.fail(w, r, err)
		return
	}
	// Balances at the end of the month, or as of the newest transaction while
	// the month is still running.
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
	trend, err := s.st.MonthlyTotals(ctx, start.AddDate(0, -11, 0).Format("2006-01"), d.Month)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	// Months without transactions are missing from the query; the chart needs
	// all twelve.
	byMonth := make(map[string]store.MonthTotal, len(trend))
	for _, m := range trend {
		byMonth[m.Month] = m
	}
	for i := -11; i <= 0; i++ {
		key := start.AddDate(0, i, 0).Format("2006-01")
		m := byMonth[key]
		m.Month = key
		d.Trend = append(d.Trend, m)
	}
	current := d.Trend[len(d.Trend)-1]
	d.Income, d.Spending = current.IncomeCents, current.SpendingCents
	d.Chart = buildCharts(d)

	s.render(w, r, overviewPage(d))
}

// --- inbox and transaction list ---------------------------------------------

func (s *server) inbox(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	page, err := s.st.ListTransactions(ctx, store.TxFilter{Status: store.StatusUncategorized, Limit: inboxPageSize})
	if err != nil {
		s.fail(w, r, err)
		return
	}
	picker, err := s.pickerCategories(r)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	s.render(w, r, inboxPage(inboxData{Page: page, Picker: picker}))
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
	if d.Inbox, err = s.st.CountUncategorized(ctx); err != nil {
		s.fail(w, r, err)
		return
	}
	s.render(w, r, listPage(d))
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
	rows := make([]store.TxView, 0, len(txIDs))
	for _, id := range txIDs {
		v, err := s.st.TxViewByID(ctx, id)
		if err != nil {
			s.fail(w, r, err)
			return
		}
		rows = append(rows, v)
	}
	s.render(w, r, txRows(rows, true))
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
