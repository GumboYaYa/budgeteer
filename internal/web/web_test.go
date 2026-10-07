package web

import (
	"bytes"
	"context"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/GumboYaYa/budgeteer/internal/importer"
	"github.com/GumboYaYa/budgeteer/internal/importer/finanzguru"
	"github.com/GumboYaYa/budgeteer/internal/money"
	"github.com/GumboYaYa/budgeteer/internal/store"
)

const (
	sampleV1 = "../../testdata/finanzguru_sample.csv"
	sampleV2 = "../../testdata/finanzguru_sample_v2.csv"
)

type env struct {
	t   *testing.T
	st  *store.Store
	srv *httptest.Server
	raw string
}

// newEnv starts the UI on a fresh database; with seed, the sample export is
// imported first (8 transactions, one of them uncategorized).
func newEnv(t *testing.T, seed bool) *env {
	t.Helper()
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	if _, err := st.Migrate(context.Background()); err != nil {
		t.Fatal(err)
	}
	if seed {
		f, err := os.Open(sampleV1)
		if err != nil {
			t.Fatal(err)
		}
		defer f.Close()
		if _, err := importer.Run(context.Background(), st, finanzguru.Parser{}, f, sampleV1, importer.Options{}); err != nil {
			t.Fatal(err)
		}
	}
	e := &env{t: t, st: st, raw: filepath.Join(dir, "raw")}
	e.srv = httptest.NewServer(New(st, Options{RawDir: e.raw}))
	t.Cleanup(e.srv.Close)
	return e
}

func (e *env) do(req *http.Request) (int, string, http.Header) {
	e.t.Helper()
	resp, err := e.srv.Client().Do(req)
	if err != nil {
		e.t.Fatal(err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		e.t.Fatal(err)
	}
	return resp.StatusCode, string(body), resp.Header
}

func (e *env) get(path string, headers ...string) (int, string) {
	e.t.Helper()
	req, _ := http.NewRequest(http.MethodGet, e.srv.URL+path, nil)
	for i := 0; i+1 < len(headers); i += 2 {
		req.Header.Set(headers[i], headers[i+1])
	}
	status, body, _ := e.do(req)
	return status, body
}

func (e *env) post(path string, form url.Values) (int, string, http.Header) {
	e.t.Helper()
	req, _ := http.NewRequest(http.MethodPost, e.srv.URL+path, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	return e.do(req)
}

func (e *env) text(query string, args ...any) string {
	e.t.Helper()
	var s *string
	if err := e.st.DB.QueryRow(query, args...).Scan(&s); err != nil {
		e.t.Fatalf("%s: %v", query, err)
	}
	if s == nil {
		return "<NULL>"
	}
	return *s
}

func (e *env) txID(externalID string) string {
	return e.text(`SELECT id FROM transactions WHERE external_id = ?`, externalID)
}

func (e *env) categoryID(slug string) string {
	return e.text(`SELECT id FROM categories WHERE slug = ?`, slug)
}

const categoryOf = `
	SELECT c.slug || ' (' || a.source || ')' FROM allocations a
	JOIN categories c ON c.id = a.category_id
	JOIN transactions t ON t.id = a.transaction_id WHERE t.external_id = ?`

func TestPagesRender(t *testing.T) {
	for _, seed := range []bool{true, false} {
		e := newEnv(t, seed)
		pages := map[string]string{
			"/":                                   "Overview",
			"/?month=2026-09":                     "September 2026",
			"/inbox":                              "Inbox",
			"/transactions":                       "Transactions",
			"/transactions?q=aldi&status=&page=1": "Transactions",
			"/categories":                         "Categories",
			"/reserve":                            "Reserve account",
			"/recurring":                          "New group",
			"/transactions?recurring=any":         "All recurring",
			"/import":                             "Finanzguru export",
			"/static/app.js":                      "tx-row",
			"/static/app.css":                     "tx-focus",
			"/static/htmx.min.js":                 "htmx",
		}
		for path, want := range pages {
			status, body := e.get(path)
			if status != http.StatusOK || !strings.Contains(body, want) {
				t.Errorf("seed=%v GET %s: status %d, body lacks %q", seed, path, status, want)
			}
		}
		// Every page has the shortcut list behind the Help link; undo is an
		// inbox key and missing from the transaction list's.
		for path, undo := range map[string]bool{"/": true, "/inbox": true, "/transactions": false, "/reserve": true} {
			_, body := e.get(path)
			_, help, found := strings.Cut(body, `aria-label="Keyboard shortcuts">Help</div>`)
			help, _, _ = strings.Cut(help, "</header>")
			if !found || !strings.Contains(help, "Choose a category") || strings.Contains(help, "Undo the last action") != undo {
				t.Errorf("seed=%v GET %s: Help lacks the shortcuts, or undo listed = %v, want %v", seed, path, !undo, undo)
			}
		}
		if status, _ := e.get("/nope"); status != http.StatusNotFound {
			t.Errorf("GET /nope: status %d, want 404", status)
		}
	}
}

func TestOverview(t *testing.T) {
	e := newEnv(t, true)
	_, body := e.get("/?month=2026-09")
	// September: salary 2500.00 in; spending 16.45 + 18.36 + 42.00 + 9.99 + 1234.56.
	// The two 500.00 transfers are left out.
	for _, want := range []string{"Account balances on 12.09.2026", "2.790,91 €", "1.240,01 €", "4.030,92 €", "2.500,00 €", "-1.321,36 €", "1.178,64 €", "Wohnen", "-1.252,92 €", "Uncategorized", `"labels":["Wohnen","Freizeit","Essen `, `Trinken","Uncategorized"]`} {
		if !strings.Contains(body, want) {
			t.Errorf("overview lacks %q", want)
		}
	}
	if strings.Contains(body, "Sparen") {
		t.Error("overview shows the transfer category")
	}
	// One month: this month's income, and beside it the average of the six
	// months before (nothing before September).
	for _, want := range []string{
		`text-emerald-600 dark:text-emerald-400">` + money.FormatDE(250000),
		"⌀ " + money.FormatDE(0) + " over the 6 months before", "this month",
		`name="from" value="2026-09"`, `name="to" value="2026-09"`,
		`href="/?month=2026-08" aria-label="Earlier`, `href="/?month=2026-10" aria-label="Later`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("overview for September lacks %q", want)
		}
	}
	if _, october := e.get("/?month=2026-10"); !strings.Contains(october, "⌀ "+money.FormatDE(41667)+" over the 6 months before") {
		t.Error("October should average September's income over six months")
	}
	// Without a month, the newest month with data is shown.
	if _, body := e.get("/"); !strings.Contains(body, "September 2026") {
		t.Error("default month is not the newest month with transactions")
	}
}

func TestOverviewPeriod(t *testing.T) {
	e := newEnv(t, true)
	active := func(body string) string {
		t.Helper()
		_, rest, found := strings.Cut(body, "btn-neutral\" href=\"/?")
		if !found {
			return ""
		}
		_, rest, _ = strings.Cut(rest, ">")
		label, _, _ := strings.Cut(rest, "<")
		return label
	}
	tests := []struct {
		query  string
		label  string
		preset string // highlighted shortcut
		wants  []string
		trendN int // months in the chart
	}{
		{query: "", label: "September 2026", preset: "Month", trendN: 12},
		// The sums are September's; the average is per month of the period.
		{query: "from=2026-07&to=2026-09", label: "July – September 2026", preset: "3 months", trendN: 12,
			wants: []string{money.FormatDE(250000), "⌀ " + money.FormatDE(83333) + " per month", "in these 3 months", money.FormatDE(-132136),
				`href="/?from=2026-04&amp;to=2026-06"`, `href="/?from=2026-10&amp;to=2026-12"`, "from=2026-07-01&amp;to=2026-09-30"}},
		{query: "from=2025-10&to=2026-09", label: "October 2025 – September 2026", preset: "12 months", trendN: 12},
		{query: "from=2026-01&to=2026-09", label: "January – September 2026", preset: "2026", trendN: 12},
		// Longer than a year: the chart grows with the period.
		{query: "from=2025-01&to=2026-09", label: "January 2025 – September 2026", trendN: 21,
			wants: []string{"in these 21 months"}},
		// The wrong way round, a single bound, nonsense.
		{query: "from=2026-09&to=2026-07", label: "July – September 2026", preset: "3 months", trendN: 12},
		{query: "from=2026-08", label: "August 2026", trendN: 12, wants: []string{money.FormatDE(0)}},
		{query: "from=x&to=y", label: "September 2026", preset: "Month", trendN: 12},
		// A period without transactions.
		{query: "from=2024-01&to=2024-03", label: "January – March 2024", trendN: 12, wants: []string{"No transactions in January – March 2024"}},
	}
	for _, tt := range tests {
		status, body := e.get("/?" + tt.query)
		if status != http.StatusOK || !strings.Contains(body, `<h1 class="text-2xl font-semibold">`+tt.label+`</h1>`) {
			t.Errorf("%q: status %d, heading is not %q", tt.query, status, tt.label)
			continue
		}
		if got := active(body); got != tt.preset {
			t.Errorf("%q: highlighted shortcut %q, want %q", tt.query, got, tt.preset)
		}
		for _, want := range tt.wants {
			if !strings.Contains(body, want) {
				t.Errorf("%q: page lacks %q", tt.query, want)
			}
		}
		_, trend, _ := strings.Cut(body, `"trend":{"labels":[`)
		trend, _, _ = strings.Cut(trend, "]")
		if got := strings.Count(trend, ",") + 1; got != tt.trendN {
			t.Errorf("%q: chart has %d months, want %d", tt.query, got, tt.trendN)
		}
	}

	// "All" starts at the oldest transaction. With only September in the
	// fixture that is the same period as "Month", so add an older one.
	if _, err := e.st.DB.Exec(`UPDATE transactions SET booking_date = '2025-03-14' WHERE external_id = 'fg-0002'`); err != nil {
		t.Fatal(err)
	}
	_, body := e.get("/")
	if !strings.Contains(body, `href="/?from=2025-03&amp;to=2026-09" aria-current="false"`) && !strings.Contains(body, `href="/?from=2025-03&amp;to=2026-09">All<`) {
		t.Error("the All shortcut does not start at the oldest transaction")
	}
	_, body = e.get("/?from=2025-03&to=2026-09")
	if got := active(body); got != "All" {
		t.Errorf("highlighted shortcut for the whole history = %q, want All", got)
	}
	// A period far beyond anything real is cut down instead of looping.
	if status, _ := e.get("/?from=0001-01&to=2026-09"); status != http.StatusOK {
		t.Errorf("huge period: status %d", status)
	}
}

func TestInboxListsUncategorized(t *testing.T) {
	e := newEnv(t, true)
	_, body := e.get("/inbox")
	if !strings.Contains(body, "Laden Unbekannt") || strings.Contains(body, "ALDI SUED") {
		t.Error("inbox should list exactly the uncategorized transaction")
	}
	if !strings.Contains(body, `id="tx-`+e.txID("fg-0007")+`"`) {
		t.Error("inbox row has no id")
	}
	if strings.Contains(body, "tx-check-all") {
		t.Error("inbox has a select-all checkbox")
	}
	if _, list := e.get("/transactions"); strings.Count(list, `tx-check-all"`) != 1 {
		t.Error("transaction list should have one select-all checkbox")
	}
	// The picker offers every category.
	categories, groups, _ := strings.Cut(body, `id="group-picker"`)
	// The group picker offers the two contracts of the file, "no group" and
	// the entry that creates a group.
	if n := strings.Count(groups, `class="picker-item"`); n != 4 {
		t.Errorf("group picker has %d entries, want 4", n)
	}
	if n := strings.Count(categories, `class="picker-item"`); n != 11 {
		t.Errorf("picker has %d categories, want 11", n)
	}
}

func TestTransactionFilters(t *testing.T) {
	e := newEnv(t, true)
	tests := []struct {
		query string
		want  int
	}{
		{"", 8},
		{"q=aldi", 1},
		{"q=%25", 0}, // a literal percent sign, not a wildcard
		{"account=gemeinschaftskonto", 2},
		{"status=uncategorized", 1},
		{"status=transfer", 2},
		{"from=2026-09-05&to=2026-09-10", 3},
		{"category=" + e.categoryID("wohnen"), 2}, // main category includes its sub categories
		{"category=" + e.categoryID("wohnen/miete"), 1},
		{"account=girokonto&q=miete", 1},
	}
	for _, tt := range tests {
		// As HTMX requests them: only the result list comes back.
		status, body := e.get("/transactions?"+tt.query, "HX-Request", "true", "HX-Target", "results")
		if status != http.StatusOK {
			t.Errorf("%q: status %d", tt.query, status)
			continue
		}
		if strings.Contains(body, "<html") {
			t.Errorf("%q: partial request returned a full page", tt.query)
		}
		if got := strings.Count(body, `class="tx-row"`); got != tt.want {
			t.Errorf("%q: %d rows, want %d", tt.query, got, tt.want)
		}
	}
}

func TestCategorize(t *testing.T) {
	e := newEnv(t, true)
	unknown, aldi := e.txID("fg-0007"), e.txID("fg-0002")
	miete := e.categoryID("wohnen/miete")

	// From the inbox: no body, the new inbox size in a header.
	status, body, header := e.post("/api/categorize", url.Values{"ids": {unknown}, "category_id": {miete}, "view": {"inbox"}})
	if status != http.StatusNoContent || body != "" || header.Get("X-Inbox-Count") != "0" {
		t.Fatalf("inbox categorize: status %d, body %q, count %q", status, body, header.Get("X-Inbox-Count"))
	}
	if got := e.text(categoryOf, "fg-0007"); got != "wohnen/miete (manual)" {
		t.Errorf("category = %s", got)
	}
	if got := e.text(`SELECT amount_cents FROM allocations WHERE transaction_id = ?`, unknown); got != "-999" {
		t.Errorf("allocation amount = %s, want the full amount -999", got)
	}

	// From the list, several at once: the updated rows come back.
	status, body, _ = e.post("/api/categorize", url.Values{"ids": {unknown, aldi}, "category_id": {e.categoryID("freizeit")}, "view": {"list"}})
	if status != http.StatusOK || strings.Count(body, `class="tx-row"`) != 2 || !strings.Contains(body, "Freizeit") {
		t.Fatalf("list categorize: status %d, body %s", status, body)
	}
	if got := e.text(categoryOf, "fg-0002"); got != "freizeit (manual)" {
		t.Errorf("category = %s", got)
	}
	if got := e.text(`SELECT count(*) FROM allocations`); got != "8" {
		t.Errorf("allocations = %s, want one per transaction (8)", got)
	}

	// Recently used categories lead the picker.
	_, page := e.get("/transactions")
	first := strings.Index(page, `class="picker-item"`)
	if first < 0 || !strings.Contains(page[first:first+300], ">Freizeit<") {
		t.Error("most recently used category is not first in the picker")
	}
}

func TestCategorizeErrors(t *testing.T) {
	e := newEnv(t, true)
	unknown := e.txID("fg-0007")
	tests := []struct {
		name string
		form url.Values
		want int
	}{
		{"no ids", url.Values{"category_id": {"1"}}, http.StatusBadRequest},
		{"bad id", url.Values{"ids": {"abc"}, "category_id": {"1"}}, http.StatusBadRequest},
		{"no category", url.Values{"ids": {unknown}}, http.StatusBadRequest},
		{"unknown category", url.Values{"ids": {unknown}, "category_id": {"9999"}}, http.StatusNotFound},
		{"unknown transaction", url.Values{"ids": {unknown, "9999"}, "category_id": {e.categoryID("freizeit")}}, http.StatusNotFound},
	}
	for _, tt := range tests {
		if status, _, _ := e.post("/api/categorize", tt.form); status != tt.want {
			t.Errorf("%s: status %d, want %d", tt.name, status, tt.want)
		}
	}
	// A failed batch changes nothing, not even its valid part.
	if got := e.text(`SELECT count(*) FROM uncategorized`); got != "1" {
		t.Errorf("uncategorized = %s after failed requests, want 1", got)
	}
}

func TestTransferAndUndo(t *testing.T) {
	e := newEnv(t, true)
	unknown := e.txID("fg-0007")
	flags := `SELECT is_transfer || '|' || is_transfer_manual FROM transactions WHERE id = ?`

	status, _, header := e.post("/api/transfer", url.Values{"ids": {unknown}, "value": {"1"}, "view": {"inbox"}})
	if status != http.StatusNoContent || header.Get("X-Inbox-Count") != "0" {
		t.Fatalf("transfer: status %d, count %q", status, header.Get("X-Inbox-Count"))
	}
	if got := e.text(flags, unknown); got != "1|1" {
		t.Errorf("flags = %s, want 1|1 (transfer, set by hand)", got)
	}

	status, _, header = e.post("/api/undo", url.Values{"ids": {unknown}, "kind": {"transfer"}, "view": {"inbox"}})
	if status != http.StatusNoContent || header.Get("X-Inbox-Count") != "1" {
		t.Fatalf("undo transfer: status %d, count %q", status, header.Get("X-Inbox-Count"))
	}
	if got := e.text(flags, unknown); got != "0|1" {
		t.Errorf("flags after undo = %s", got)
	}

	e.post("/api/categorize", url.Values{"ids": {unknown}, "category_id": {e.categoryID("freizeit")}, "view": {"inbox"}})
	status, _, header = e.post("/api/undo", url.Values{"ids": {unknown}, "kind": {"category"}, "view": {"inbox"}})
	if status != http.StatusNoContent || header.Get("X-Inbox-Count") != "1" {
		t.Fatalf("undo category: status %d, count %q", status, header.Get("X-Inbox-Count"))
	}
	if got := e.text(`SELECT count(*) FROM allocations WHERE transaction_id = ?`, unknown); got != "0" {
		t.Errorf("allocation left after undo")
	}

	if status, _, _ := e.post("/api/undo", url.Values{"ids": {unknown}, "kind": {"everything"}}); status != http.StatusBadRequest {
		t.Errorf("unknown undo kind: status %d, want 400", status)
	}

	// Unmarking a transfer from the list returns the row without the badge.
	transfer := e.txID("fg-0003")
	_, body, _ := e.post("/api/transfer", url.Values{"ids": {transfer}, "value": {"0"}, "view": {"list"}})
	if strings.Contains(body, "data-transfer") || strings.Contains(body, ">Transfer<") {
		t.Errorf("row still shown as transfer: %s", body)
	}
}

func TestReserve(t *testing.T) {
	e := newEnv(t, true)
	rent, radio, unknown := e.txID("fg-0008"), e.txID("fg-0005"), e.txID("fg-0007")
	marked := `SELECT COALESCE(group_concat(external_id), '') FROM (SELECT external_id FROM transactions WHERE is_reserve = 1 ORDER BY external_id)`
	contains := func(body string, wants ...string) {
		t.Helper()
		for _, want := range wants {
			if !strings.Contains(body, want) {
				t.Errorf("page lacks %q", want)
			}
		}
	}

	_, body := e.get("/reserve")
	contains(body, "No irregular expenses marked", "nothing marked yet", "no reserve account chosen")

	// Marking keeps the rows in place, so the inbox gets them back as well.
	status, body, _ := e.post("/api/reserve", url.Values{"ids": {rent, radio, unknown}, "value": {"1"}, "view": {"inbox"}})
	if status != http.StatusOK || strings.Count(body, "data-reserve") != 3 || strings.Contains(body, "Uncategorized") {
		t.Fatalf("mark: status %d, body %s", status, body)
	}
	if got := e.text(`SELECT count(*) FROM uncategorized`); got != "1" {
		t.Errorf("uncategorized = %s, want 1: marking must not categorize", got)
	}
	// Unmarking from the list returns the row without the badge.
	status, body, _ = e.post("/api/reserve", url.Values{"ids": {unknown}, "value": {"0"}, "view": {"list"}})
	if status != http.StatusOK || strings.Contains(body, "data-reserve") || !strings.Contains(body, "Uncategorized") {
		t.Errorf("unmark: status %d, body %s", status, body)
	}
	// A failed batch changes nothing.
	if status, _, _ := e.post("/api/reserve", url.Values{"ids": {unknown, "99999"}, "value": {"1"}}); status != http.StatusNotFound {
		t.Errorf("unknown transaction: status %d, want 404", status)
	}
	if status, _, _ := e.post("/api/reserve", url.Values{"value": {"1"}}); status != http.StatusBadRequest {
		t.Errorf("no ids: status %d, want 400", status)
	}
	if got := e.text(marked); got != "fg-0005,fg-0008" {
		t.Errorf("marked = %s", got)
	}
	_, body = e.get("/transactions?status=reserve", "HX-Request", "true", "HX-Target", "results")
	if got := strings.Count(body, `class="tx-row"`); got != 2 {
		t.Errorf("status=reserve lists %d rows, want 2", got)
	}

	// Both bills (1234.56 + 18.36) were paid this month and are due in a
	// year: a twelfth each month, rounded up.
	_, body = e.get("/reserve")
	contains(body, "the newest imported transaction", "12.09.2026", "Hausverwaltung Beispiel", "12.09.2027", "05.09.2027",
		money.FormatDE(125292), money.FormatDE(10441), "a twelfth of the bills of a year", "no reserve account chosen", "on target")

	// The balance of the reserve account counts as saved.
	status, body, _ = e.post("/reserve/account", url.Values{"account": {"gemeinschaftskonto"}})
	if status != http.StatusOK {
		t.Fatalf("choose account: status %d", status)
	}
	contains(body, "Gemeinschaftskonto on 12.09.2026", money.FormatDE(124001)+" ahead")
	e.post("/reserve/account", url.Values{"account": {"girokonto"}})
	if got := e.text(`SELECT group_concat(slug) FROM accounts WHERE holds_reserve = 1`); got != "girokonto" {
		t.Errorf("reserve accounts = %s, want only girokonto", got)
	}
	if status, _, _ := e.post("/reserve/account", url.Values{"account": {"nope"}}); status != http.StatusNotFound {
		t.Errorf("unknown account: status %d, want 404", status)
	}

	// Had the rent been paid on 20.10. last year, it would be due before the
	// next transfer but one: without savings the whole amount is needed now.
	_, body, _ = e.post("/reserve/account", url.Values{"account": {""}})
	contains(body, "no reserve account chosen")
	if _, err := e.st.DB.Exec(`UPDATE transactions SET booking_date = '2025-10-20' WHERE id = ?`, rent); err != nil {
		t.Fatal(err)
	}
	_, body = e.get("/reserve")
	contains(body, "20.10.2026", "this month, to cover the next bill", money.FormatDE(123456), money.FormatDE(113168)+" behind", "The reserve is behind")
}

func TestRecurring(t *testing.T) {
	e := newEnv(t, true)
	aldi, unknown, rent := e.txID("fg-0002"), e.txID("fg-0007"), e.txID("fg-0008")
	groupOf := `SELECT COALESCE((SELECT name FROM recurring_groups g WHERE g.id = t.recurring_group_id), '-') || '|' || recurring_manual
		FROM transactions t WHERE id = ?`
	groupID := func(name string) string {
		return e.text(`SELECT id FROM recurring_groups WHERE name = ?`, name)
	}
	rows := func(query string) int {
		t.Helper()
		_, body := e.get("/transactions?"+query, "HX-Request", "true", "HX-Target", "results")
		return strings.Count(body, `class="tx-row"`)
	}

	// The import made a group of each Finanzguru contract.
	_, body := e.get("/recurring")
	for _, want := range []string{"Hausverwaltung Beispiel", "Rundfunk ARD, ZDF, DRadio", "Per month in total", money.FormatDE(-125292)} {
		if !strings.Contains(body, want) {
			t.Errorf("recurring page lacks %q", want)
		}
	}

	// A new group is created by its name; the rows come back marked, in the
	// inbox as well.
	status, body, header := e.post("/api/recurring", url.Values{"ids": {aldi, unknown}, "name": {" Wocheneinkauf "}, "view": {"inbox"}})
	created := groupID("Wocheneinkauf")
	if status != http.StatusOK || header.Get("X-Group-Id") != created || strings.Count(body, "Recurring: Wocheneinkauf") != 4 {
		t.Fatalf("create by name: status %d, group %q (want %s), body %s", status, header.Get("X-Group-Id"), created, body)
	}
	// The same name again, in another case, is the same group.
	e.post("/api/recurring", url.Values{"ids": {aldi}, "name": {"wocheneinkauf"}})
	if n := e.text(`SELECT count(*) FROM recurring_groups`); n != "3" {
		t.Errorf("%s groups, want 3", n)
	}
	status, body, _ = e.post("/api/recurring", url.Values{"ids": {unknown}, "group_id": {"0"}, "view": {"list"}})
	if status != http.StatusOK || strings.Contains(body, "Recurring:") {
		t.Errorf("take out: status %d, body %s", status, body)
	}
	for id, want := range map[string]string{aldi: "Wocheneinkauf|1", unknown: "-|1", rent: "Hausverwaltung Beispiel|0"} {
		if got := e.text(groupOf, id); got != want {
			t.Errorf("transaction %s: group %s, want %s", id, got, want)
		}
	}
	// Failed requests change nothing.
	for name, form := range map[string]url.Values{
		"unknown group":       {"ids": {rent}, "group_id": {"999"}},
		"unknown transaction": {"ids": {rent, "99999"}, "group_id": {created}},
	} {
		if status, _, _ := e.post("/api/recurring", form); status != http.StatusNotFound {
			t.Errorf("%s: status %d, want 404", name, status)
		}
	}
	if status, _, _ := e.post("/api/recurring", url.Values{"ids": {rent}}); status != http.StatusBadRequest {
		t.Errorf("neither group nor name: status %d, want 400", status)
	}
	if got := e.text(groupOf, rent); got != "Hausverwaltung Beispiel|0" {
		t.Errorf("rent after failed requests: %s", got)
	}

	for query, want := range map[string]int{"recurring=any": 3, "recurring=" + created: 1, "recurring=" + created + "&q=miete": 0, "recurring=x": 8} {
		if got := rows(query); got != want {
			t.Errorf("%q: %d rows, want %d", query, got, want)
		}
	}

	// Managing groups, as HTMX posts them.
	hausverwaltung := groupID("Hausverwaltung Beispiel")
	steps := []struct {
		path string
		form url.Values
		want string // in the re-rendered list
	}{
		{"/recurring/" + hausverwaltung + "/rename", url.Values{"name": {"Miete"}}, `value="Miete"`},
		{"/recurring/" + hausverwaltung + "/rename", url.Values{"name": {"wocheneinkauf"}}, "merge the two instead"},
		{"/recurring/" + hausverwaltung + "/rename", url.Values{"name": {" "}}, "needs a name"},
		{"/recurring/" + hausverwaltung + "/interval", url.Values{"interval": {"yearly"}}, money.FormatDE(-10288)}, // 1234.56 / 12
		{"/recurring/" + hausverwaltung + "/interval", url.Values{"interval": {"weekly"}}, "unknown interval"},
		{"/recurring/" + hausverwaltung + "/reserve", url.Values{"value": {"1"}}, "checked"},
		{"/recurring", url.Values{"name": {"Versicherung"}, "interval": {"half-yearly"}}, `value="Versicherung"`},
		{"/recurring/" + hausverwaltung + "/merge", url.Values{}, "choose the group to merge into"},
		{"/recurring/999/delete", url.Values{}, "no longer exists"},
	}
	for _, step := range steps {
		status, body, _ := e.post(step.path, step.form)
		if status != http.StatusOK || strings.Contains(body, "<html") || !strings.Contains(body, step.want) {
			t.Errorf("POST %s %v: status %d, list lacks %q", step.path, step.form, status, step.want)
		}
	}

	// A group covered by the reserve makes its transactions count there.
	if got := rows("status=reserve"); got != 1 {
		t.Errorf("status=reserve lists %d rows, want the rent", got)
	}
	_, body = e.get("/reserve")
	if !strings.Contains(body, "Hausverwaltung Beispiel") || !strings.Contains(body, money.FormatDE(123456)) {
		t.Error("the reserve does not count the rent of the covered group")
	}
	_, body = e.get("/transactions?recurring=" + hausverwaltung)
	if !strings.Contains(body, "through its recurring group Miete") {
		t.Error("the row does not say that its group puts it into the reserve")
	}

	// A group that has ended keeps its transactions, but drops out of the
	// monthly cost and of the reserve, and can be filtered.
	status, body, _ = e.post("/recurring/"+hausverwaltung+"/active", url.Values{})
	if status != http.StatusOK || !strings.Contains(body, "1 inactive not counted") || strings.Contains(body, money.FormatDE(-10288)) {
		t.Errorf("switch to inactive: status %d, list still counts the group", status)
	}
	for query, want := range map[string]int{
		"recurring=any": 3, "recurring=active": 2, "recurring=inactive": 1, "recurring=" + hausverwaltung: 1, "status=reserve": 0,
	} {
		if got := rows(query); got != want {
			t.Errorf("with an inactive group, %q: %d rows, want %d", query, got, want)
		}
	}
	_, body = e.get("/transactions?recurring=" + hausverwaltung)
	if !strings.Contains(body, "Recurring, ended: Miete") || strings.Contains(body, "through its recurring group") {
		t.Error("the row of an ended group should say so and not count for the reserve")
	}
	if _, body = e.get("/inbox"); !strings.Contains(body, `badge-ghost">inactive<`) {
		t.Error("the group picker does not mark the inactive group")
	}
	e.post("/recurring/"+hausverwaltung+"/active", url.Values{"value": {"1"}})
	if got := rows("status=reserve"); got != 1 {
		t.Errorf("active again: status=reserve lists %d rows, want 1", got)
	}

	// Merging moves the transactions; deleting leaves them without a group.
	e.post("/recurring/"+created+"/merge", url.Values{"into_id": {hausverwaltung}})
	if got := e.text(groupOf, aldi); got != "Miete|1" {
		t.Errorf("after merge: %s", got)
	}
	e.post("/recurring/"+hausverwaltung+"/delete", url.Values{})
	if got := e.text(groupOf, aldi) + " " + e.text(groupOf, rent); got != "-|1 -|0" {
		t.Errorf("after delete: %s", got)
	}
	if got := rows("status=reserve"); got != 0 {
		t.Errorf("status=reserve lists %d rows after the group is gone", got)
	}
}

func upload(e *env, path string, fields map[string]string) (int, string) {
	e.t.Helper()
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	if path != "" {
		content, err := os.ReadFile(path)
		if err != nil {
			e.t.Fatal(err)
		}
		part, _ := w.CreateFormFile("file", filepath.Base(path))
		part.Write(content)
	}
	for k, v := range fields {
		w.WriteField(k, v)
	}
	w.Close()
	req, _ := http.NewRequest(http.MethodPost, e.srv.URL+"/import", &buf)
	req.Header.Set("Content-Type", w.FormDataContentType())
	status, body, _ := e.do(req)
	return status, body
}

func TestImportUpload(t *testing.T) {
	e := newEnv(t, false)

	status, body := upload(e, sampleV1, nil)
	if status != http.StatusOK || !strings.Contains(body, "Import finished") || !strings.Contains(body, "girokonto, gemeinschaftskonto") {
		t.Fatalf("first upload: status %d, body %s", status, body)
	}
	if got := e.text(`SELECT count(*) FROM transactions`); got != "8" {
		t.Errorf("transactions = %s, want 8", got)
	}
	if files, _ := filepath.Glob(filepath.Join(e.raw, "*_finanzguru_sample.csv")); len(files) != 1 {
		t.Error("original file was not kept")
	}

	if _, body := upload(e, sampleV1, nil); !strings.Contains(body, "imported before") {
		t.Errorf("second upload of the same file: %s", body)
	}
	if _, body := upload(e, sampleV1, map[string]string{"force": "1"}); !strings.Contains(body, "Import finished") {
		t.Errorf("forced upload: %s", body)
	}
	if _, body := upload(e, sampleV2, nil); !strings.Contains(body, "Import finished") {
		t.Errorf("newer export: %s", body)
	}
	if got := e.text(`SELECT count(*) FROM transactions`); got != "10" {
		t.Errorf("transactions = %s, want 10", got)
	}

	// Problems are shown, not raised as server errors.
	if status, body := upload(e, "", nil); status != http.StatusOK || !strings.Contains(body, "Choose a file") {
		t.Errorf("upload without file: status %d, body %s", status, body)
	}
	if status, body := upload(e, "web_test.go", nil); status != http.StatusOK || !strings.Contains(body, "Nothing was imported") {
		t.Errorf("upload of a non-export: status %d, body %s", status, body)
	}
	if _, body := upload(e, sampleV2, map[string]string{"cutover": "2026-09-05", "force": "1"}); !strings.Contains(body, "cut-over 2026-09-05 rejected") {
		t.Errorf("rejected cut-over: %s", body)
	}
}

func TestCategoryManagement(t *testing.T) {
	e := newEnv(t, true)
	slugs := func() string {
		return e.text(`SELECT group_concat(slug) FROM (SELECT slug FROM categories ORDER BY slug)`)
	}

	// Add a main category and a sub category.
	if _, body, _ := e.post("/categories", url.Values{"name": {"Haustiere"}, "parent_id": {"0"}}); !strings.Contains(body, "haustiere") || strings.Contains(body, "<html") {
		t.Fatalf("create: %s", body)
	}
	e.post("/categories", url.Values{"name": {"Futter & Zubehör"}, "parent_id": {e.categoryID("haustiere")}})
	if !strings.Contains(slugs(), "haustiere,haustiere/futter-zubehoer") {
		t.Fatalf("slugs = %s", slugs())
	}

	// Refused changes are explained and change nothing.
	refused := []struct {
		name, path string
		form       url.Values
		want       string
	}{
		{"duplicate", "/categories", url.Values{"name": {"haustiere"}, "parent_id": {"0"}}, "already exists"},
		{"empty name", "/categories", url.Values{"name": {"  "}, "parent_id": {"0"}}, "must not be empty"},
		{"third level", "/categories", url.Values{"name": {"X"}, "parent_id": {e.categoryID("wohnen/miete")}}, "cannot have sub categories"},
		{"move parent with children", "/categories/" + e.categoryID("wohnen") + "/move", url.Values{"parent_id": {e.categoryID("freizeit")}}, "has sub categories"},
		{"merge parent with children", "/categories/" + e.categoryID("wohnen") + "/merge", url.Values{"into_id": {e.categoryID("freizeit")}}, "has sub categories"},
		{"merge without target", "/categories/" + e.categoryID("wohnen/miete") + "/merge", url.Values{"into_id": {""}}, "choose the category"},
		{"merge into itself", "/categories/" + e.categoryID("wohnen/miete") + "/merge", url.Values{"into_id": {e.categoryID("wohnen/miete")}}, "into itself"},
		{"unknown category", "/categories/9999/rename", url.Values{"name": {"X"}}, "no longer exists"},
	}
	before := slugs()
	for _, tt := range refused {
		status, body, _ := e.post(tt.path, tt.form)
		if status != http.StatusOK || !strings.Contains(body, tt.want) {
			t.Errorf("%s: status %d, body lacks %q", tt.name, status, tt.want)
		}
	}
	if slugs() != before {
		t.Errorf("refused changes altered the categories: %s", slugs())
	}

	// Rename keeps the key.
	miete := e.categoryID("wohnen/miete")
	e.post("/categories/"+miete+"/rename", url.Values{"name": {"Kaltmiete"}})
	if got := e.text(`SELECT name || '|' || slug FROM categories WHERE id = ?`, miete); got != "Kaltmiete|wohnen/miete" {
		t.Errorf("after rename: %s", got)
	}

	// Move changes the key and keeps the transactions.
	e.post("/categories/"+miete+"/move", url.Values{"parent_id": {e.categoryID("haustiere")}})
	if got := e.text(`SELECT slug FROM categories WHERE id = ?`, miete); got != "haustiere/miete" {
		t.Errorf("after move: %s", got)
	}
	if got := e.text(categoryOf, "fg-0008"); got != "haustiere/miete (finanzguru)" {
		t.Errorf("transaction category after move: %s", got)
	}
	// ... and to the top level.
	e.post("/categories/"+miete+"/move", url.Values{"parent_id": {"0"}})
	if got := e.text(`SELECT slug || '|' || COALESCE(parent_id, 0) FROM categories WHERE id = ?`, miete); got != "miete|0" {
		t.Errorf("after move to top: %s", got)
	}

	// Merge moves the transactions and deletes the category.
	rundfunk := e.categoryID("wohnen/rundfunk")
	e.post("/categories/"+miete+"/merge", url.Values{"into_id": {rundfunk}})
	if got := e.text(`SELECT count(*) FROM categories WHERE id = ?`, miete); got != "0" {
		t.Error("merged category still exists")
	}
	if got := e.text(categoryOf, "fg-0008"); got != "wohnen/rundfunk (finanzguru)" {
		t.Errorf("transaction category after merge: %s", got)
	}
	if got := e.text(`SELECT count(*) FROM allocations`); got != "7" {
		t.Errorf("allocations = %s after merge, want 7", got)
	}
}

func TestOnlyLocalSameOriginRequests(t *testing.T) {
	e := newEnv(t, true)
	form := url.Values{"ids": {e.txID("fg-0007")}, "category_id": {e.categoryID("freizeit")}}

	crossSite := func(header, value string) int {
		req, _ := http.NewRequest(http.MethodPost, e.srv.URL+"/api/categorize", strings.NewReader(form.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.Header.Set(header, value)
		status, _, _ := e.do(req)
		return status
	}
	if status := crossSite("Sec-Fetch-Site", "cross-site"); status != http.StatusForbidden {
		t.Errorf("cross-site POST: status %d, want 403", status)
	}
	if status := crossSite("Origin", "https://evil.example"); status != http.StatusForbidden {
		t.Errorf("POST with foreign Origin: status %d, want 403", status)
	}

	// A foreign host name resolving to this machine must not get any data.
	req, _ := http.NewRequest(http.MethodGet, e.srv.URL+"/transactions", nil)
	req.Host = "evil.example:" + strconv.Itoa(8080)
	if status, body, _ := e.do(req); status != http.StatusForbidden || strings.Contains(body, "ALDI") {
		t.Errorf("GET with foreign Host: status %d", status)
	}

	if got := e.text(`SELECT count(*) FROM uncategorized`); got != "1" {
		t.Error("a rejected request changed data")
	}
	if status, _, _ := e.post("/api/categorize", form); status != http.StatusNoContent {
		t.Errorf("same-origin POST: status %d, want 204", status)
	}
}

// The totals and the pager appear above and below a multi-page list.
func TestPagerAboveAndBelow(t *testing.T) {
	e := newEnv(t, false)
	var b strings.Builder
	header, _ := os.ReadFile(sampleV1)
	lines := strings.SplitN(string(header), "\n", 3)
	b.WriteString(lines[0] + "\n")
	for i := 0; i < 150; i++ {
		b.WriteString(strings.Replace(lines[1], "fg-0001", "fg-9"+strconv.Itoa(1000+i), 1) + "\n")
	}
	if _, err := importer.Run(context.Background(), e.st, finanzguru.Parser{}, strings.NewReader(b.String()), "many.csv", importer.Options{}); err != nil {
		t.Fatal(err)
	}
	for path, wantRows := range map[string]int{"/transactions": 100, "/transactions?page=2": 50} {
		_, body := e.get(path)
		if n := strings.Count(body, `class="tx-row"`); n != wantRows {
			t.Errorf("%s: %d rows, want %d", path, n, wantRows)
		}
		if n := strings.Count(body, "150 transactions"); n != 2 {
			t.Errorf("%s: total shown %d times, want 2 (above and below)", path, n)
		}
		if n := strings.Count(body, "of 2</span>"); n != 2 {
			t.Errorf("%s: pager shown %d times, want 2", path, n)
		}
	}
}

func TestImportUploadDKB(t *testing.T) {
	e := newEnv(t, false)
	ctx := context.Background()
	for _, a := range []store.Account{
		{Slug: "girokonto", Name: "Girokonto", IBAN: "DE00111122223333444401"},
		{Slug: "gemeinschaftskonto", Name: "Gemeinschaftskonto", IBAN: "DE00111122223333444402"},
	} {
		if _, err := e.st.CreateAccount(ctx, a); err != nil {
			t.Fatal(err)
		}
	}
	const file = "../../testdata/dkb_a.csv"

	if _, body := e.get("/import"); !strings.Contains(body, `value="dkb_csv"`) || !strings.Contains(body, `<option value="girokonto">`) {
		t.Error("import page lacks the DKB source or the account choice")
	}
	if _, body := upload(e, file, map[string]string{"source": "dkb_csv"}); !strings.Contains(body, "Choose the account") {
		t.Errorf("DKB upload without account: %s", body)
	}
	if _, body := upload(e, file, map[string]string{"source": "dkb_csv", "account": "nope"}); !strings.Contains(body, "does not exist") {
		t.Errorf("DKB upload with unknown account: %s", body)
	}
	status, body := upload(e, file, map[string]string{"source": "dkb_csv", "account": "girokonto"})
	if status != http.StatusOK || !strings.Contains(body, "Import finished") || !strings.Contains(body, "Not booked yet") {
		t.Fatalf("DKB upload: status %d, body %s", status, body)
	}
	if got := e.text(`SELECT count(*) || '|' || sum(is_transfer) FROM transactions WHERE source = 'dkb_csv'`); got != "7|1" {
		t.Errorf("transactions|transfers = %s, want 7|1", got)
	}
	// The new transactions wait in the inbox; the statement row and the transfer do not.
	if _, body := e.get("/inbox"); strings.Count(body, `class="tx-row"`) != 5 {
		t.Errorf("inbox has %d rows, want 5", strings.Count(body, `class="tx-row"`))
	}
	if status, _ := upload(e, file, map[string]string{"source": "xls"}); status != http.StatusBadRequest {
		t.Errorf("unknown source: status %d, want 400", status)
	}
}
