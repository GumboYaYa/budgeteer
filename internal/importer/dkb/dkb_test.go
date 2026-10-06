package dkb

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/GumboYaYa/budgeteer/internal/importer"
	"github.com/GumboYaYa/budgeteer/internal/importer/finanzguru"
	"github.com/GumboYaYa/budgeteer/internal/store"
)

const (
	fileA       = "../../../testdata/dkb_a.csv"        // 01.10.–10.10., as re-exported by Google Sheets
	fileB       = "../../../testdata/dkb_b.csv"        // 08.10.–20.10., overlaps A
	fileADirect = "../../../testdata/dkb_a_direct.csv" // A as DKB writes it: BOM, semicolons, quotes
	fgSample    = "../../../testdata/finanzguru_sample_v2.csv"
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

// withAccounts creates the two own accounts the fixtures refer to.
func withAccounts(t *testing.T, st *store.Store) {
	t.Helper()
	ctx := context.Background()
	for _, a := range []store.Account{
		{Slug: "girokonto", Name: "Girokonto", IBAN: "DE00111122223333444401", Bank: "dkb"},
		{Slug: "gemeinschaftskonto", Name: "Gemeinschaftskonto", IBAN: "DE00111122223333444402"},
	} {
		if _, err := st.CreateAccount(ctx, a); err != nil {
			t.Fatal(err)
		}
	}
}

func run(t *testing.T, st *store.Store, path string, opts importer.Options) (importer.Summary, error) {
	t.Helper()
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if opts.Account == "" {
		opts.Account = "girokonto"
	}
	return importer.Run(context.Background(), st, Parser{Account: opts.Account}, bytes.NewReader(content), path, opts)
}

func mustRun(t *testing.T, st *store.Store, path string, opts importer.Options) importer.Summary {
	t.Helper()
	s, err := run(t, st, path, opts)
	if err != nil {
		t.Fatalf("import %s: %v", filepath.Base(path), err)
	}
	return s
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

func TestImport(t *testing.T) {
	st := newStore(t)
	withAccounts(t, st)
	s := mustRun(t, st, fileA, importer.Options{})

	// Eight rows: one pending, seven booked.
	if s.Rows != 8 || s.New != 7 || s.SkippedPending != 1 || s.Duplicates != 0 || s.SkippedCutover != 0 {
		t.Errorf("summary = %+v", s)
	}
	checks := []struct{ query, want string }{
		{`SELECT count(*) FROM transactions WHERE source = 'dkb_csv' AND external_id IS NULL AND dedup_key LIKE 'dkb:%'`, "7"},
		{`SELECT count(*) FROM raw_records`, "8"}, // the pending row is kept in the raw layer
		{`SELECT a.slug FROM imports i JOIN accounts a ON a.id = i.account_id`, "girokonto"},
		{`SELECT count(DISTINCT account_id) FROM transactions`, "1"},
		// Outgoing: counterparty is the payee; German amount; two-digit years.
		{`SELECT booking_date || '|' || value_date || '|' || amount_cents || '|' || counterparty || '|' || counterparty_ref || '|' || mandate_ref || '|' || customer_ref
		  FROM transactions WHERE purpose = 'Miete 10/2026'`,
			"2026-10-07|2026-10-07|-123456|Hausverwaltung Beispiel|DE00999900000000000003|MIETE-1|KREF-77"},
		// Incoming: counterparty is the payer.
		{`SELECT counterparty || '|' || amount_cents FROM transactions WHERE purpose LIKE 'Lohn%'`, "Beispiel GmbH|250000"},
		{`SELECT creditor_id || '|' || counterparty FROM transactions WHERE purpose LIKE 'Rundfunk%'`, "DE00ZZZ00000000001|Rundfunk ARD, ZDF, DRadio"},
		// Card payment: purchase date from the purpose.
		{`SELECT group_concat(DISTINCT purchase_date) FROM transactions WHERE counterparty = 'Baeckerei Beispiel'`, "2026-10-08"},
		// Two identical bookings on one day are two transactions.
		{`SELECT count(*) || '|' || count(DISTINCT dedup_key) FROM transactions WHERE counterparty = 'Baeckerei Beispiel'`, "2|2"},
		// A booking to the other own account is a transfer; a bare "-500" is 500.00.
		{`SELECT group_concat(purpose || ':' || amount_cents) FROM transactions WHERE is_transfer = 1`, "Haushaltskasse:-50000"},
		// The statement row is imported with amount 0 ...
		{`SELECT amount_cents || '|' || counterparty || '|' || counterparty_ref FROM transactions WHERE purpose LIKE 'Abrechnung%'`, "0|DKB AG|1234567890"},
		// ... but neither it nor the transfer shows up in the inbox.
		{`SELECT count(*) FROM uncategorized`, "5"},
		{`SELECT count(*) FROM uncategorized WHERE purpose LIKE 'Abrechnung%' OR is_transfer = 1`, "0"},
		{`SELECT count(*) FROM allocations`, "0"},
		// The balance from the preamble.
		{`SELECT balance_cents || '@' || balance_date FROM accounts WHERE slug = 'girokonto'`, "500000@2026-10-10"},
	}
	for _, c := range checks {
		if got := text(t, st, c.query); got != c.want {
			t.Errorf("%s\n  got  %s\n  want %s", strings.Join(strings.Fields(c.query), " "), got, c.want)
		}
	}
}

func TestOverlappingExports(t *testing.T) {
	st := newStore(t)
	withAccounts(t, st)
	mustRun(t, st, fileA, importer.Options{})

	if s := mustRun(t, st, fileA, importer.Options{}); !s.AlreadyImported {
		t.Errorf("same file again: %+v, want AlreadyImported", s)
	}

	// B repeats the two bakery bookings and the transfer, and adds two rows.
	s := mustRun(t, st, fileB, importer.Options{})
	if s.Rows != 5 || s.New != 2 || s.Duplicates != 3 || s.SkippedPending != 0 {
		t.Errorf("summary of B = %+v", s)
	}
	checks := []struct{ query, want string }{
		{`SELECT count(*) FROM transactions`, "9"},
		{`SELECT count(*) FROM transactions WHERE counterparty = 'Baeckerei Beispiel'`, "2"},
		{`SELECT count(*) FROM transactions WHERE purpose = 'Haushaltskasse'`, "1"},
		// Pending in A (-45,00), booked in B with a different date, text and
		// amount: imported exactly once, as booked.
		{`SELECT group_concat(booking_date || ':' || amount_cents) FROM transactions WHERE counterparty = 'Tankstelle Nord'`, "2026-10-11:-4510"},
		{`SELECT amount_cents || '|' || purchase_date FROM transactions WHERE counterparty = 'REWE'`, "-5700|2026-10-14"},
		{`SELECT balance_cents || '@' || balance_date FROM accounts WHERE slug = 'girokonto'`, "489790@2026-10-20"},
	}
	for _, c := range checks {
		if got := text(t, st, c.query); got != c.want {
			t.Errorf("%s\n  got  %s\n  want %s", c.query, got, c.want)
		}
	}

	// The older file, processed again, adds nothing and keeps the newer balance.
	if s := mustRun(t, st, fileA, importer.Options{Force: true}); s.New != 0 || s.Duplicates != 7 {
		t.Errorf("forced A after B: %+v", s)
	}
	if got := text(t, st, `SELECT balance_cents || '@' || balance_date FROM accounts WHERE slug = 'girokonto'`); got != "489790@2026-10-20" {
		t.Errorf("balance after older file = %s", got)
	}
}

// The file as DKB writes it and the same data re-exported by Google Sheets
// must produce the same transactions.
func TestDirectExportMatchesSheetsExport(t *testing.T) {
	st := newStore(t)
	withAccounts(t, st)
	first := mustRun(t, st, fileADirect, importer.Options{})
	if first.Rows != 8 || first.New != 7 || first.SkippedPending != 1 {
		t.Errorf("direct export: %+v", first)
	}
	if got := text(t, st, `SELECT balance_cents || '@' || balance_date FROM accounts WHERE slug = 'girokonto'`); got != "500000@2026-10-10" {
		t.Errorf("balance from direct export = %s", got)
	}
	second := mustRun(t, st, fileA, importer.Options{})
	if second.AlreadyImported || second.New != 0 || second.Duplicates != 7 {
		t.Errorf("Sheets export after direct export: %+v, want 7 duplicates", second)
	}
}

func TestCutoverWithFinanzguruHistory(t *testing.T) {
	ctx := context.Background()
	importHistory := func(t *testing.T, st *store.Store, cutover string) {
		t.Helper()
		f, err := os.Open(fgSample)
		if err != nil {
			t.Fatal(err)
		}
		defer f.Close()
		if _, err := importer.Run(ctx, st, finanzguru.Parser{}, f, fgSample, importer.Options{Cutover: cutover}); err != nil {
			t.Fatal(err)
		}
	}

	t.Run("each source keeps its side", func(t *testing.T) {
		st := newStore(t)
		importHistory(t, st, "2026-10-02") // creates both accounts; history up to 02.10.
		s := mustRun(t, st, fileA, importer.Options{})
		// 01.10. and 02.10. belong to Finanzguru; 05.10.–09.10. are new.
		if s.New != 5 || s.SkippedCutover != 2 || s.SkippedPending != 1 {
			t.Errorf("summary = %+v", s)
		}
		if got := text(t, st, `SELECT count(*) FROM transactions WHERE source = 'dkb_csv' AND booking_date <= '2026-10-02'`); got != "0" {
			t.Errorf("%s DKB transactions on or before the cut-over", got)
		}
		if got := text(t, st, `SELECT group_concat(source || ':' || n, ' ') FROM (SELECT source, count(*) n FROM transactions GROUP BY source ORDER BY source)`); got != "dkb_csv:5 finanzguru:10" {
			t.Errorf("transactions per source = %s", got)
		}

		// Finanzguru may not claim days that DKB already covers.
		f, _ := os.Open(fgSample)
		defer f.Close()
		_, err := importer.Run(ctx, st, finanzguru.Parser{}, f, fgSample, importer.Options{Cutover: "2026-10-08", Force: true})
		if err == nil || !strings.Contains(err.Error(), "another source since 2026-10-05") {
			t.Errorf("later Finanzguru cut-over: error = %v, want rejection naming 2026-10-05", err)
		}
	})

	t.Run("refused without a cut-over date", func(t *testing.T) {
		st := newStore(t)
		importHistory(t, st, "")
		_, err := run(t, st, fileA, importer.Options{})
		if err == nil || !strings.Contains(err.Error(), "account set-cutover --slug girokonto") {
			t.Fatalf("error = %v, want a hint to set the cut-over date", err)
		}
		if got := text(t, st, `SELECT count(*) FROM transactions WHERE source = 'dkb_csv'`); got != "0" {
			t.Errorf("%s DKB transactions after a refused import", got)
		}

		// Set it by hand, then the import goes through.
		if err := importer.SetCutover(ctx, st, "girokonto", "2026-09-30", finanzguru.Source); err == nil || !strings.Contains(err.Error(), "up to 2026-10-02") {
			t.Errorf("cut-over before existing history: error = %v, want rejection naming 2026-10-02", err)
		}
		if err := importer.SetCutover(ctx, st, "girokonto", "2026-10-02", finanzguru.Source); err != nil {
			t.Fatal(err)
		}
		if s := mustRun(t, st, fileA, importer.Options{}); s.New != 5 || s.SkippedCutover != 2 {
			t.Errorf("summary = %+v", s)
		}
		// Now the date cannot move past the first DKB booking any more.
		if err := importer.SetCutover(ctx, st, "girokonto", "2026-10-05", finanzguru.Source); err == nil || !strings.Contains(err.Error(), "another source since 2026-10-05") {
			t.Errorf("cut-over onto a DKB day: error = %v", err)
		}
		if err := importer.SetCutover(ctx, st, "girokonto", "2026-10-04", finanzguru.Source); err != nil {
			t.Errorf("cut-over between the sources: %v", err)
		}
	})

	t.Run("set-cutover input", func(t *testing.T) {
		st := newStore(t)
		if err := importer.SetCutover(ctx, st, "nope", "2026-10-02", finanzguru.Source); err == nil {
			t.Error("want an error for an unknown account")
		}
		if err := importer.SetCutover(ctx, st, "nope", "02.10.2026", finanzguru.Source); err == nil {
			t.Error("want an error for a non-ISO date")
		}
	})
}

func variant(t *testing.T, old, new string) *strings.Reader {
	t.Helper()
	content, err := os.ReadFile(fileA)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(content), old) {
		t.Fatalf("fixture does not contain %q", old)
	}
	return strings.NewReader(strings.Replace(string(content), old, new, 1))
}

func TestBadFiles(t *testing.T) {
	ctx := context.Background()
	tests := []struct{ name, old, new, wantErr string }{
		{"unknown status", "07.10.26,07.10.26,Gebucht", "07.10.26,07.10.26,Storniert", "line 10: Status: unexpected value"},
		{"English amount", `"-1.234,56"`, "-1234.56", "line 10: Betrag (€)"},
		{"bad date", "07.10.26,07.10.26", "2026-10-07,07.10.26", "line 10: Buchungsdatum"},
		{"missing column", "Kundenreferenz", "Referenz", "missing column(s) Kundenreferenz"},
		{"not a DKB file", "Buchungsdatum,", "Datum,", "is this a DKB export?"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			st := newStore(t)
			withAccounts(t, st)
			_, err := importer.Run(ctx, st, Parser{Account: "girokonto"}, variant(t, tt.old, tt.new), "bad.csv", importer.Options{Account: "girokonto"})
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("error = %v, want it to contain %q", err, tt.wantErr)
			}
			if got := text(t, st, `SELECT (SELECT count(*) FROM transactions) + (SELECT count(*) FROM imports) + (SELECT count(*) FROM raw_records)`); got != "0" {
				t.Errorf("%s rows written by a failed import", got)
			}
		})
	}

	t.Run("unknown account", func(t *testing.T) {
		st := newStore(t)
		_, err := run(t, st, fileA, importer.Options{Account: "girokonto"})
		if err == nil || !strings.Contains(err.Error(), `account "girokonto" does not exist`) {
			t.Fatalf("error = %v", err)
		}
	})

	t.Run("unreadable balance is not fatal", func(t *testing.T) {
		st := newStore(t)
		withAccounts(t, st)
		s, err := importer.Run(ctx, st, Parser{Account: "girokonto"}, variant(t, `"5.000,00 €"`, "n/a"), "a.csv", importer.Options{Account: "girokonto"})
		if err != nil || s.New != 7 {
			t.Fatalf("summary = %+v, error = %v", s, err)
		}
		if got := text(t, st, `SELECT balance_date FROM accounts WHERE slug = 'girokonto'`); got != "<NULL>" {
			t.Errorf("balance_date = %s, want none", got)
		}
	})
}

func TestCounterparty(t *testing.T) {
	tests := []struct {
		kind         string
		amount       int64
		payer, payee string
		want         string
	}{
		{"Ausgang", -100, "me", "shop", "shop"},
		{"Eingang", 100, "employer", "me", "employer"},
		{"", -100, "me", "shop", "shop"},
		{"", 100, "employer", "me", "employer"},
		{"", 0, "", "DKB AG", "DKB AG"},
		{"", 0, "someone", "", "someone"},
	}
	for _, tt := range tests {
		if got := counterparty(tt.kind, tt.amount, tt.payer, tt.payee); got != tt.want {
			t.Errorf("counterparty(%q, %d, %q, %q) = %q, want %q", tt.kind, tt.amount, tt.payer, tt.payee, got, tt.want)
		}
	}
}
