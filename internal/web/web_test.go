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
	// Without a month, the newest month with data is shown.
	if _, body := e.get("/"); !strings.Contains(body, "September 2026") {
		t.Error("default month is not the newest month with transactions")
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
	// The picker offers every category.
	if n := strings.Count(body, `class="picker-item"`); n != 11 {
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
