package export

import (
	"context"
	"database/sql"
	"encoding/csv"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/parquet-go/parquet-go"

	"github.com/GumboYaYa/budgeteer/internal/importer"
	"github.com/GumboYaYa/budgeteer/internal/importer/finanzguru"
	"github.com/GumboYaYa/budgeteer/internal/money"
	"github.com/GumboYaYa/budgeteer/internal/store"
)

// newStore returns a store holding the sample export: 8 transactions, one of
// them uncategorized, two transfers.
func newStore(t *testing.T) *store.Store {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	ctx := context.Background()
	if _, err := st.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	f, err := os.Open("../../testdata/finanzguru_sample.csv")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if _, err := importer.Run(ctx, st, finanzguru.Parser{}, f, "sample.csv", importer.Options{}); err != nil {
		t.Fatal(err)
	}
	// A value with quote, comma and line break has to survive the CSV.
	if _, err := st.DB.Exec(`UPDATE transactions SET purpose = 'say "hi", twice' || char(10) || 'second line' WHERE external_id = 'fg-0006'`); err != nil {
		t.Fatal(err)
	}
	return st
}

func readCSV(t *testing.T, path string) (header []string, rows []map[string]string) {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	records, err := csv.NewReader(f).ReadAll()
	if err != nil {
		t.Fatalf("%s is not valid CSV: %v", path, err)
	}
	header = records[0]
	for _, record := range records[1:] {
		row := make(map[string]string, len(header))
		for i, name := range header {
			row[name] = record[i]
		}
		rows = append(rows, row)
	}
	return header, rows
}

func TestCSV(t *testing.T) {
	st := newStore(t)
	dir := filepath.Join(t.TempDir(), "out")
	files, err := Run(context.Background(), st, dir, CSV)
	if err != nil {
		t.Fatal(err)
	}

	wantRows := map[string]int{
		"transactions_flat": 8, "transactions": 8, "accounts": 2, "categories": 11,
		"allocations": 7, "tags": 3, "transaction_tags": 4, "imports": 1, "raw_records": 8,
	}
	if len(files) != len(wantRows) {
		t.Errorf("%d files written, want %d", len(files), len(wantRows))
	}
	for _, f := range files {
		name := strings.TrimSuffix(filepath.Base(f.Path), ".csv")
		if f.Rows != wantRows[name] {
			t.Errorf("%s: %d rows reported, want %d", name, f.Rows, wantRows[name])
		}
		if _, rows := readCSV(t, f.Path); len(rows) != f.Rows {
			t.Errorf("%s: file has %d rows, reported %d", name, len(rows), f.Rows)
		}
	}
	if leftovers, _ := filepath.Glob(filepath.Join(dir, "*.tmp")); len(leftovers) != 0 {
		t.Errorf("temporary files left behind: %v", leftovers)
	}

	header, rows := readCSV(t, filepath.Join(dir, "transactions_flat.csv"))
	if got := strings.Join(header[:8], ","); got != "id,account,booking_date,value_date,purchase_date,amount,amount_cents,currency" {
		t.Errorf("header starts with %s", got)
	}

	// Amounts: the decimal column and the cents column agree, and the total
	// equals the database.
	var sum int64
	byID := make(map[string]map[string]string)
	for _, row := range rows {
		cents, err := strconv.ParseInt(row["amount_cents"], 10, 64)
		if err != nil {
			t.Fatal(err)
		}
		if parsed, err := money.ParseEN(row["amount"]); err != nil || parsed != cents {
			t.Errorf("amount %q does not match amount_cents %d", row["amount"], cents)
		}
		sum += cents
		byID[row["external_id"]] = row
	}
	var dbSum int64
	if err := st.DB.QueryRow(`SELECT sum(amount_cents) FROM transactions`).Scan(&dbSum); err != nil {
		t.Fatal(err)
	}
	if sum != dbSum {
		t.Errorf("exported total %d, database total %d", sum, dbSum)
	}

	checks := []struct{ id, column, want string }{
		{"fg-0008", "amount", "-1234.56"},
		{"fg-0008", "main_category", "Wohnen"},
		{"fg-0008", "sub_category", "Miete"},
		{"fg-0008", "category_slug", "wohnen/miete"},
		{"fg-0008", "category_source", "finanzguru"},
		{"fg-0008", "account", "girokonto"},
		{"fg-0008", "tags", "vertrag"},
		{"fg-0002", "booking_date", "2026-09-02"},
		{"fg-0002", "purchase_date", "2026-09-01"},
		{"fg-0002", "value_date", ""}, // NULL
		{"fg-0002", "main_category", "Essen & Trinken"},
		{"fg-0003", "is_transfer", "1"},
		{"fg-0005", "counterparty", "Rundfunk ARD, ZDF, DRadio"},
		{"fg-0006", "tags", "geschenk;urlaub-2026"},
		{"fg-0006", "purpose", "say \"hi\", twice\nsecond line"},
		{"fg-0007", "main_category", ""}, // uncategorized
		{"fg-0007", "category_slug", ""},
		{"fg-0001", "amount", "2500.00"},
	}
	for _, c := range checks {
		if got := byID[c.id][c.column]; got != c.want {
			t.Errorf("%s %s = %q, want %q", c.id, c.column, got, c.want)
		}
	}

	// Running it again replaces the files.
	if _, err := Run(context.Background(), st, dir, CSV); err != nil {
		t.Errorf("second export into the same directory: %v", err)
	}
}

func TestParquet(t *testing.T) {
	st := newStore(t)
	dir := t.TempDir()
	files, err := Run(context.Background(), st, dir, Parquet)
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 9 {
		t.Errorf("%d files written, want 9", len(files))
	}

	path := filepath.Join(dir, "transactions_flat.parquet")
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	info, _ := f.Stat()
	pf, err := parquet.OpenFile(f, info.Size())
	if err != nil {
		t.Fatalf("not a readable Parquet file: %v", err)
	}
	if pf.NumRows() != 8 {
		t.Errorf("%d rows, want 8", pf.NumRows())
	}
	schema := pf.Schema().String()
	for _, want := range []string{"optional int64 amount (DECIMAL(18,2))", "optional int64 amount_cents", "optional binary counterparty (STRING)"} {
		if !strings.Contains(schema, want) {
			t.Errorf("schema lacks %q:\n%s", want, schema)
		}
	}
	// Column order follows the dataset, not the alphabet.
	columns := pf.Schema().Columns()
	if columns[0][0] != "id" || columns[1][0] != "account" || columns[5][0] != "amount" {
		t.Errorf("unexpected column order: %v", columns[:6])
	}

	// Read the rows back generically and compare with the database.
	index := make(map[string]int)
	for i, c := range columns {
		index[c[0]] = i
	}
	rows := make([]parquet.Row, 8)
	n, _ := pf.RowGroups()[0].Rows().ReadRows(rows)
	if n != 8 {
		t.Fatalf("read %d rows, want 8", n)
	}
	var sum int64
	sawNullCategory, sawRent := false, false
	for _, row := range rows[:n] {
		sum += row[index["amount_cents"]].Int64()
		if row[index["amount"]].Int64() != row[index["amount_cents"]].Int64() {
			t.Error("decimal amount is not backed by the cents value")
		}
		switch row[index["external_id"]].String() {
		case "fg-0007":
			sawNullCategory = row[index["main_category"]].IsNull()
		case "fg-0008":
			sawRent = row[index["main_category"]].String() == "Wohnen" && row[index["amount_cents"]].Int64() == -123456
		}
	}
	var dbSum int64
	if err := st.DB.QueryRow(`SELECT sum(amount_cents) FROM transactions`).Scan(&dbSum); err != nil {
		t.Fatal(err)
	}
	if sum != dbSum {
		t.Errorf("exported total %d, database total %d", sum, dbSum)
	}
	if !sawNullCategory {
		t.Error("uncategorized transaction should have a NULL category")
	}
	if !sawRent {
		t.Error("rent row not found with its category and amount")
	}
}

func TestUnknownFormat(t *testing.T) {
	st := newStore(t)
	dir := filepath.Join(t.TempDir(), "out")
	if _, err := Run(context.Background(), st, dir, "xlsx"); err == nil {
		t.Error("want an error for an unknown format")
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Error("output directory created for an invalid request")
	}
}

func TestBackup(t *testing.T) {
	ctx := context.Background()
	st := newStore(t)
	path := filepath.Join(t.TempDir(), "backup.db")
	if err := st.Backup(ctx, path); err != nil {
		t.Fatal(err)
	}

	// The copy is a complete, independent database.
	copy, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatal(err)
	}
	defer copy.Close()
	for _, table := range []string{"transactions", "allocations", "categories", "raw_records", "goose_db_version"} {
		var want, got int
		if err := st.DB.QueryRow(`SELECT count(*) FROM ` + table).Scan(&want); err != nil {
			t.Fatal(err)
		}
		if err := copy.QueryRow(`SELECT count(*) FROM ` + table).Scan(&got); err != nil {
			t.Fatalf("backup lacks table %s: %v", table, err)
		}
		if got != want || want == 0 {
			t.Errorf("%s: backup has %d rows, database %d", table, got, want)
		}
	}
	var check string
	if err := copy.QueryRow(`PRAGMA integrity_check`).Scan(&check); err != nil || check != "ok" {
		t.Errorf("integrity_check = %q, %v", check, err)
	}

	// An existing backup is never overwritten.
	if err := st.Backup(ctx, path); err == nil {
		t.Error("second backup to the same path succeeded, want an error")
	}
}
