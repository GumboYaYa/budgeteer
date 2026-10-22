package categorize

import (
	"context"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/GumboYaYa/budgeteer/internal/store"
)

type fixture struct {
	t        *testing.T
	st       *store.Store
	account  int64
	importID int64
	n        int
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	ctx := context.Background()
	st, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	if _, err := st.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	a, err := st.CreateAccount(ctx, store.Account{Slug: "giro", Name: "Giro"})
	if err != nil {
		t.Fatal(err)
	}
	importID, err := st.CreateImport(ctx, store.Import{Source: "test", FileName: "test.csv", SHA256: "x"})
	if err != nil {
		t.Fatal(err)
	}
	return &fixture{t: t, st: st, account: a.ID, importID: importID}
}

func (f *fixture) category(slug string) int64 {
	f.t.Helper()
	id, _, err := f.st.EnsureCategory(context.Background(), slug, slug, 0)
	if err != nil {
		f.t.Fatal(err)
	}
	return id
}

// tx adds a transaction; source "" leaves it uncategorized.
func (f *fixture) tx(counterparty string, categoryID int64, source string, transfer bool) int64 {
	f.t.Helper()
	ctx := context.Background()
	f.n++
	raw, err := f.st.PutRawRecord(ctx, f.importID, f.n, "{}")
	if err != nil {
		f.t.Fatal(err)
	}
	id, err := f.st.InsertTransaction(ctx, store.Transaction{
		AccountID: f.account, RawRecordID: raw, Source: "test", DedupKey: "t:" + strconv.Itoa(f.n),
		BookingDate: "2026-01-" + strconv.Itoa(10+f.n), AmountCents: -1000, Counterparty: counterparty, IsTransfer: transfer,
	})
	if err != nil {
		f.t.Fatal(err)
	}
	if source != "" {
		err := f.st.InsertAllocation(ctx, store.Allocation{
			TransactionID: id, CategoryID: categoryID, AmountCents: -1000, Source: source, RuleID: ruleIDFor(source),
		})
		if err != nil {
			f.t.Fatal(err)
		}
	}
	return id
}

func ruleIDFor(source string) string {
	if source == store.SourceRule {
		return "history:name"
	}
	return ""
}

func (f *fixture) allocation(txID int64) string {
	f.t.Helper()
	allocations, err := f.st.Allocations(context.Background(), txID)
	if err != nil {
		f.t.Fatal(err)
	}
	if len(allocations) == 0 {
		return ""
	}
	if len(allocations) > 1 {
		f.t.Fatalf("transaction %d has %d allocations", txID, len(allocations))
	}
	return strconv.FormatInt(allocations[0].CategoryID, 10) + " " + allocations[0].Source
}

func TestRun(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)
	food, home := f.category("essen"), f.category("wohnen")

	f.tx("Markthalle Nord", food, store.SourceFinanzguru, false)
	f.tx("Markthalle Nord", food, store.SourceManual, false)
	f.tx("Kiosk am Eck", food, store.SourceManual, false)
	// Applied automatically before: not evidence for the next one.
	f.tx("Blumen Beispiel", home, store.SourceRule, false)
	f.tx("Blumen Beispiel", home, store.SourceRule, false)
	// Transfers are no evidence either.
	f.tx("Eigenes Konto", home, store.SourceManual, true)
	f.tx("Eigenes Konto", home, store.SourceManual, true)

	sure := f.tx("MARKTHALLE NORD GmbH", 0, "", false)
	unsure := f.tx("Kiosk am Eck", 0, "", false)
	fromAuto := f.tx("Blumen Beispiel", 0, "", false)
	fromTransfer := f.tx("Eigenes Konto", 0, "", false)
	unknown := f.tx("Ganz Neu", 0, "", false)

	want := Result{Applied: 1, Suggested: 1, Left: 3}
	dry, err := Run(ctx, f.st, true)
	if err != nil {
		t.Fatal(err)
	}
	if dry != want {
		t.Errorf("dry run = %+v, want %+v", dry, want)
	}
	if got := f.allocation(sure); got != "" {
		t.Errorf("dry run categorized the transaction: %s", got)
	}

	suggestions, err := Suggest(ctx, f.st, []int64{sure, unsure, unknown})
	if err != nil {
		t.Fatal(err)
	}
	if len(suggestions) != 2 || suggestions[sure].CategoryID != food || suggestions[unsure].CategoryID != food || suggestions[unsure].Sure {
		t.Errorf("suggestions = %+v", suggestions)
	}

	got, err := Run(ctx, f.st, false)
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Errorf("run = %+v, want %+v", got, want)
	}
	for id, want := range map[int64]string{
		sure:         strconv.FormatInt(food, 10) + " rule",
		unsure:       "",
		fromAuto:     "",
		fromTransfer: "",
		unknown:      "",
	} {
		if got := f.allocation(id); got != want {
			t.Errorf("transaction %d: allocation %q, want %q", id, got, want)
		}
	}
	var ruleID string
	if err := f.st.DB.QueryRow(`SELECT rule_id FROM allocations WHERE transaction_id = ?`, sure).Scan(&ruleID); err != nil || ruleID != "history:name" {
		t.Errorf("rule_id = %q, %v", ruleID, err)
	}

	again, err := Run(ctx, f.st, false)
	if err != nil {
		t.Fatal(err)
	}
	if again.Applied != 0 {
		t.Errorf("second run applied %d", again.Applied)
	}

	// A category that exists is kept, whoever set it.
	if err := f.st.SetAutoCategory(ctx, sure, home, "history:name"); err != nil {
		t.Fatal(err)
	}
	if got := f.allocation(sure); got != strconv.FormatInt(food, 10)+" rule" {
		t.Errorf("SetAutoCategory replaced an allocation: %s", got)
	}

	// Confirming by hand turns it into a manual category, and the list of
	// automatic ones no longer has it.
	page, err := f.st.ListTransactions(ctx, store.TxFilter{Status: store.StatusAuto})
	if err != nil {
		t.Fatal(err)
	}
	if page.Total != 3 {
		t.Errorf("automatic transactions = %d, want 3", page.Total)
	}
	if err := f.st.SetCategory(ctx, sure, food); err != nil {
		t.Fatal(err)
	}
	if got := f.allocation(sure); got != strconv.FormatInt(food, 10)+" manual" {
		t.Errorf("after SetCategory: %s", got)
	}
}
