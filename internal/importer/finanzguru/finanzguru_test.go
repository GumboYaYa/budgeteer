package finanzguru

import (
	"bytes"
	"context"
	"encoding/csv"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/GumboYaYa/budgeteer/internal/importer"
	"github.com/GumboYaYa/budgeteer/internal/store"
)

const (
	sampleV1 = "../../../testdata/finanzguru_sample.csv"
	sampleV2 = "../../../testdata/finanzguru_sample_v2.csv"
)

func newStore(t *testing.T) *store.Store {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	if _, err := st.Migrate(context.Background()); err != nil {
		t.Fatal(err)
	}
	return st
}

func runFile(t *testing.T, st *store.Store, path string, opts importer.Options) (importer.Summary, error) {
	t.Helper()
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return importer.Run(context.Background(), st, Parser{}, bytes.NewReader(content), path, opts)
}

func mustRun(t *testing.T, st *store.Store, path string, opts importer.Options) importer.Summary {
	t.Helper()
	s, err := runFile(t, st, path, opts)
	if err != nil {
		t.Fatalf("import %s: %v", filepath.Base(path), err)
	}
	return s
}

// count runs a "SELECT count(*) ..." style query.
func count(t *testing.T, st *store.Store, query string, args ...any) int {
	t.Helper()
	var n int
	if err := st.DB.QueryRow(query, args...).Scan(&n); err != nil {
		t.Fatalf("%s: %v", query, err)
	}
	return n
}

func text(t *testing.T, st *store.Store, query string, args ...any) string {
	t.Helper()
	var s *string
	if err := st.DB.QueryRow(query, args...).Scan(&s); err != nil {
		t.Fatalf("%s: %v", query, err)
	}
	if s == nil {
		return "<NULL>"
	}
	return *s
}

const categoryOf = `
	SELECT c.slug || ' (' || a.source || ')'
	FROM transactions t
	JOIN allocations a ON a.transaction_id = t.id
	JOIN categories c ON c.id = a.category_id
	WHERE t.external_id = ?`

func TestFirstImport(t *testing.T) {
	st := newStore(t)
	rawDir := filepath.Join(t.TempDir(), "raw")
	s := mustRun(t, st, sampleV1, importer.Options{RawDir: rawDir})

	if s.Rows != 8 || s.New != 8 || s.Duplicates != 0 || s.Updated != 0 || s.SkippedCutover != 0 {
		t.Errorf("summary = %+v", s)
	}
	if got := strings.Join(s.AccountsCreated, ","); got != "girokonto,gemeinschaftskonto" {
		t.Errorf("AccountsCreated = %s", got)
	}
	if s.CategoriesCreated != 11 {
		t.Errorf("CategoriesCreated = %d, want 11", s.CategoriesCreated)
	}
	if len(s.Warnings) != 0 {
		t.Errorf("Warnings = %v", s.Warnings)
	}

	if n := count(t, st, `SELECT count(*) FROM imports WHERE source = 'finanzguru' AND row_count = 8 AND account_id IS NULL`); n != 1 {
		t.Errorf("imports rows = %d, want 1", n)
	}
	if n := count(t, st, `SELECT count(*) FROM raw_records`); n != 8 {
		t.Errorf("raw_records = %d, want 8", n)
	}
	if files, _ := filepath.Glob(filepath.Join(rawDir, "*_finanzguru_sample.csv")); len(files) != 1 {
		t.Errorf("original file not kept in %s", rawDir)
	}

	// Raw layer keeps the row verbatim, including columns that are not mapped.
	raw := text(t, st, `SELECT r.data FROM raw_records r JOIN transactions t ON t.raw_record_id = r.id WHERE t.external_id = 'fg-0001'`)
	for _, want := range []string{`"Kontostand":"4,602.28"`, `"Betrag":"2500.00"`, `"Analyse-Hauptkategorie":"Einnahmen"`} {
		if !strings.Contains(raw, want) {
			t.Errorf("raw record lacks %s: %s", want, raw)
		}
	}

	checks := []struct{ query, want string }{
		// Card payment: purchase date from the purpose, no counterparty ref.
		{`SELECT booking_date || '|' || purchase_date || '|' || amount_cents || '|' || account_id || '|' || dedup_key FROM transactions WHERE external_id = 'fg-0002'`,
			"2026-09-02|2026-09-01|-1645|1|fg:fg-0002"},
		{`SELECT counterparty_ref FROM transactions WHERE external_id = 'fg-0002'`, "<NULL>"},
		{`SELECT purchase_date FROM transactions WHERE external_id = 'fg-0001'`, "<NULL>"},
		// Direct debit references.
		{`SELECT creditor_id || '|' || mandate_ref || '|' || end_to_end_ref || '|' || counterparty FROM transactions WHERE external_id = 'fg-0005'`,
			"DE00ZZZ00000000001|MR-12345|RF-2026-09|Rundfunk ARD, ZDF, DRadio"},
		// Counterparty ref is not always an IBAN.
		{`SELECT counterparty_ref FROM transactions WHERE external_id = 'fg-0006'`, "shop@example.com"},
		{`SELECT amount_cents FROM transactions WHERE external_id = 'fg-0008'`, "-123456"},
		{`SELECT currency || '|' || source FROM transactions WHERE external_id = 'fg-0008'`, "EUR|finanzguru"},
		{`SELECT a.slug FROM transactions t JOIN accounts a ON a.id = t.account_id WHERE t.external_id = 'fg-0004'`, "gemeinschaftskonto"},
		{`SELECT group_concat(external_id) FROM (SELECT external_id FROM transactions WHERE is_transfer = 1 ORDER BY external_id)`, "fg-0003,fg-0004"},
		// Categories and allocations.
		{categoryOfID("fg-0002"), "essen-trinken/lebensmittel (finanzguru)"},
		{`SELECT p.slug FROM categories c JOIN categories p ON p.id = c.parent_id WHERE c.slug = 'essen-trinken/lebensmittel'`, "essen-trinken"},
		{`SELECT name FROM categories WHERE slug = 'essen-trinken'`, "Essen & Trinken"},
		{`SELECT a.amount_cents FROM allocations a JOIN transactions t ON t.id = a.transaction_id WHERE t.external_id = 'fg-0008'`, "-123456"},
		{`SELECT group_concat(slug) FROM (SELECT slug FROM categories WHERE excluded_from_income = 1 ORDER BY slug)`,
			"sparen-umbuchungen,sparen-umbuchungen/umbuchung"},
		// Tags, including the contract tag.
		{tagsOf("fg-0006"), "geschenk,urlaub-2026"},
		{tagsOf("fg-0005"), "vertrag"},
		{tagsOf("fg-0008"), "vertrag"},
		// The inbox holds exactly the row without a category.
		{`SELECT group_concat(external_id) FROM uncategorized`, "fg-0007"},
	}
	for _, c := range checks {
		if got := text(t, st, c.query); got != c.want {
			t.Errorf("%s\n  got  %s\n  want %s", strings.TrimSpace(c.query), got, c.want)
		}
	}
}

func categoryOfID(externalID string) string {
	return strings.Replace(categoryOf, "?", "'"+externalID+"'", 1)
}

func tagsOf(externalID string) string {
	return `SELECT group_concat(name) FROM (
		SELECT g.name FROM tags g
		JOIN transaction_tags tt ON tt.tag_id = g.id
		JOIN transactions t ON t.id = tt.transaction_id
		WHERE t.external_id = '` + externalID + `' ORDER BY g.name)`
}

func TestSameFileTwice(t *testing.T) {
	st := newStore(t)
	mustRun(t, st, sampleV1, importer.Options{})
	s := mustRun(t, st, sampleV1, importer.Options{})

	if !s.AlreadyImported || s.New != 0 {
		t.Errorf("summary = %+v, want AlreadyImported", s)
	}
	for table, want := range map[string]int{"imports": 1, "raw_records": 8, "transactions": 8, "allocations": 7, "accounts": 2} {
		if n := count(t, st, `SELECT count(*) FROM `+table); n != want {
			t.Errorf("%s = %d rows, want %d", table, n, want)
		}
	}
}

func TestNewerFullExport(t *testing.T) {
	st := newStore(t)
	mustRun(t, st, sampleV1, importer.Options{})

	// fg-0002 is re-categorized by hand between the two imports.
	_, err := st.DB.Exec(`
		UPDATE allocations
		SET source = 'manual', category_id = (SELECT id FROM categories WHERE slug = 'wohnen/miete')
		WHERE transaction_id = (SELECT id FROM transactions WHERE external_id = 'fg-0002')`)
	if err != nil {
		t.Fatal(err)
	}

	s := mustRun(t, st, sampleV2, importer.Options{})
	if s.Rows != 10 || s.New != 2 || s.Duplicates != 8 || s.Updated != 2 || s.SkippedCutover != 0 {
		t.Errorf("summary = %+v", s)
	}
	if len(s.AccountsCreated) != 0 || s.CategoriesCreated != 3 {
		t.Errorf("AccountsCreated = %v, CategoriesCreated = %d", s.AccountsCreated, s.CategoriesCreated)
	}
	// Lebensmittel has one excluded row out of two in this export.
	if len(s.Warnings) != 2 || !strings.Contains(s.Warnings[0], "essen-trinken: 1 of 2") {
		t.Errorf("Warnings = %q", s.Warnings)
	}

	for table, want := range map[string]int{"imports": 2, "raw_records": 10, "transactions": 10, "allocations": 10, "accounts": 2} {
		if n := count(t, st, `SELECT count(*) FROM `+table); n != want {
			t.Errorf("%s = %d rows, want %d", table, n, want)
		}
	}

	checks := []struct{ query, want string }{
		{categoryOfID("fg-0006"), "freizeit/geschenke (finanzguru)"}, // changed in Finanzguru
		{categoryOfID("fg-0007"), "shopping/sonstiges (finanzguru)"}, // categorized since
		{categoryOfID("fg-0002"), "wohnen/miete (manual)"},           // set by hand, kept
		{categoryOfID("fg-0010"), "essen-trinken/lebensmittel (finanzguru)"},
		{`SELECT purchase_date FROM transactions WHERE external_id = 'fg-0010'`, "2026-10-01"},
		{`SELECT count(*) FROM uncategorized`, "0"},
		{`SELECT excluded_from_income FROM categories WHERE slug = 'essen-trinken/lebensmittel'`, "0"},
	}
	for _, c := range checks {
		if got := text(t, st, c.query); got != c.want {
			t.Errorf("%s\n  got  %s\n  want %s", strings.TrimSpace(c.query), got, c.want)
		}
	}
}

func TestCutover(t *testing.T) {
	st := newStore(t)
	s := mustRun(t, st, sampleV1, importer.Options{Cutover: "2026-09-05"})
	if s.New != 5 || s.SkippedCutover != 3 {
		t.Errorf("summary = %+v, want 5 new and 3 skipped", s)
	}
	if n := count(t, st, `SELECT count(*) FROM transactions WHERE booking_date > '2026-09-05'`); n != 0 {
		t.Errorf("%d transactions after the cut-over", n)
	}
	if n := count(t, st, `SELECT count(*) FROM accounts WHERE cutover_date = '2026-09-05'`); n != 2 {
		t.Errorf("cut-over stored on %d accounts, want 2", n)
	}
	// Filtered rows still reach the raw layer.
	if n := count(t, st, `SELECT count(*) FROM raw_records`); n != 8 {
		t.Errorf("raw_records = %d, want 8", n)
	}

	// Without the flag, the stored cut-over applies.
	s = mustRun(t, st, sampleV2, importer.Options{})
	if s.New != 0 || s.Duplicates != 5 || s.SkippedCutover != 5 {
		t.Errorf("second import summary = %+v", s)
	}
}

func TestMoveCutover(t *testing.T) {
	ctx := context.Background()

	t.Run("later date is accepted", func(t *testing.T) {
		st := newStore(t)
		mustRun(t, st, sampleV1, importer.Options{Cutover: "2026-09-05"})
		s := mustRun(t, st, sampleV2, importer.Options{Cutover: "2026-09-30"})
		if s.New != 3 || s.Duplicates != 5 || s.SkippedCutover != 2 {
			t.Errorf("summary = %+v", s)
		}
	})

	t.Run("rejected when this source already reaches past the date", func(t *testing.T) {
		st := newStore(t)
		mustRun(t, st, sampleV1, importer.Options{})
		_, err := runFile(t, st, sampleV2, importer.Options{Cutover: "2026-09-05"})
		if err == nil || !strings.Contains(err.Error(), "up to 2026-09-12") {
			t.Fatalf("error = %v, want rejection naming 2026-09-12", err)
		}
		if n := count(t, st, `SELECT count(*) FROM imports`); n != 1 {
			t.Errorf("imports = %d after rejected import, want 1", n)
		}
		if n := count(t, st, `SELECT count(*) FROM accounts WHERE cutover_date IS NOT NULL`); n != 0 {
			t.Errorf("cut-over was stored despite the rejection")
		}
	})

	t.Run("rejected when another source has rows on or before the date", func(t *testing.T) {
		st := newStore(t)
		mustRun(t, st, sampleV1, importer.Options{Cutover: "2026-09-15"})
		giro, err := st.AccountBySlug(ctx, "girokonto")
		if err != nil {
			t.Fatal(err)
		}
		_, err = st.InsertTransaction(ctx, store.Transaction{
			AccountID: giro.ID, RawRecordID: 1, Source: "dkb_csv", DedupKey: "dkb:test",
			BookingDate: "2026-09-20", AmountCents: -500,
		})
		if err != nil {
			t.Fatal(err)
		}
		_, err = runFile(t, st, sampleV2, importer.Options{Cutover: "2026-09-30"})
		if err == nil || !strings.Contains(err.Error(), "another source since 2026-09-20") {
			t.Fatalf("error = %v, want rejection naming 2026-09-20", err)
		}
	})

	t.Run("invalid date", func(t *testing.T) {
		st := newStore(t)
		if _, err := runFile(t, st, sampleV1, importer.Options{Cutover: "30.09.2026"}); err == nil {
			t.Error("want error for a non-ISO cut-over date")
		}
	})
}

// variant returns the sample with one cell of its last row (line 9) replaced.
func variant(t *testing.T, column, value string) []byte {
	t.Helper()
	f, err := os.Open(sampleV1)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	records, err := csv.NewReader(f).ReadAll()
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for i, name := range records[0] {
		if name == column {
			records[len(records)-1][i] = value
			found = true
		}
	}
	if !found {
		t.Fatalf("column %q not in sample", column)
	}
	var buf bytes.Buffer
	w := csv.NewWriter(&buf)
	if err := w.WriteAll(records); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func TestBadRowsRollBack(t *testing.T) {
	tests := []struct {
		name, column, value, wantErr string
	}{
		{"bad amount", "Betrag", "abc", "line 9: Betrag"},
		{"German amount format", "Betrag", "-12,34", "line 9: Betrag"},
		{"bad date", "Buchungstag", "2026-09-12", "line 9: Buchungstag"},
		{"missing booking id", "Buchungs-ID", "", "line 9: Buchungs-ID is empty"},
		{"split type set", "Split-Typ", "Teil", "split transactions are not supported"},
		{"split reference set", "Referenz-Original-ID", "fg-0001", "split transactions are not supported"},
		{"bad yes/no", "Analyse-Umbuchung", "vielleicht", "line 9: Analyse-Umbuchung"},
		// Fails inside the database transaction, after accounts, categories
		// and seven transactions were written.
		{"missing account", "Referenzkonto", "", "line 9: row has no account IBAN"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			st := newStore(t)
			rawDir := filepath.Join(t.TempDir(), "raw")
			_, err := importer.Run(context.Background(), st, Parser{},
				bytes.NewReader(variant(t, tt.column, tt.value)), "bad.csv", importer.Options{RawDir: rawDir})
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("error = %v, want it to contain %q", err, tt.wantErr)
			}
			for _, table := range []string{"imports", "raw_records", "transactions", "allocations", "accounts", "categories", "tags"} {
				if n := count(t, st, `SELECT count(*) FROM `+table); n != 0 {
					t.Errorf("%s has %d rows after a failed import, want 0", table, n)
				}
			}
			if files, _ := filepath.Glob(filepath.Join(rawDir, "*")); len(files) != 0 {
				t.Errorf("original file kept after a failed import: %v", files)
			}
		})
	}
}

func TestMissingColumn(t *testing.T) {
	st := newStore(t)
	content := "Buchungstag,Betrag\n01.09.2026,1.00\n"
	_, err := importer.Run(context.Background(), st, Parser{}, strings.NewReader(content), "other.csv", importer.Options{})
	if err == nil || !strings.Contains(err.Error(), "missing column(s) Referenzkonto") {
		t.Fatalf("error = %v, want missing-column error", err)
	}
}

func TestDuplicateBookingIDInFile(t *testing.T) {
	st := newStore(t)
	content, err := os.ReadFile(sampleV1)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimRight(string(content), "\n"), "\n")
	doubled := strings.Join(append(lines, lines[len(lines)-1]), "\n") + "\n"

	s, err := importer.Run(context.Background(), st, Parser{}, strings.NewReader(doubled), "doubled.csv", importer.Options{})
	if err != nil {
		t.Fatal(err)
	}
	if s.New != 8 || s.Duplicates != 1 {
		t.Errorf("summary = %+v, want 8 new and 1 duplicate", s)
	}
}

func TestNormalizeDetails(t *testing.T) {
	if got := purchaseDate("2026-10-03T05:07 Debitk.12 2029-12 SHOP (ECOM)"); got != "2026-10-03" {
		t.Errorf("purchaseDate = %q", got)
	}
	for _, purpose := range []string{"", "Miete 09/2026", "Ref 2026-10-03T05:07 Debitk.", "2026-13-40T05:07 Debitk.12"} {
		if got := purchaseDate(purpose); got != "" {
			t.Errorf("purchaseDate(%q) = %q, want empty", purpose, got)
		}
	}
	if got := strings.Join(splitTags(" Urlaub-2026; geschenk, ,GESCHENK"), "|"); got != "urlaub-2026|geschenk" {
		t.Errorf("splitTags = %q", got)
	}
}
