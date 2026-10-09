package store

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
)

// newTestStore returns a migrated store backed by a temporary file.
func newTestStore(t *testing.T) *Store {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "sub", "test.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	if _, err := s.Migrate(context.Background()); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	return s
}

func TestMigrate(t *testing.T) {
	ctx := context.Background()
	s, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer s.Close()

	applied, err := s.Migrate(ctx)
	if err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	if len(applied) == 0 || applied[0] != "0001_init.sql" {
		t.Fatalf("applied = %v, want 0001_init.sql first", applied)
	}

	again, err := s.Migrate(ctx)
	if err != nil {
		t.Fatalf("second Migrate: %v", err)
	}
	if len(again) != 0 {
		t.Errorf("second Migrate applied %v, want nothing", again)
	}

	version, err := s.SchemaVersion(ctx)
	if err != nil {
		t.Fatalf("SchemaVersion: %v", err)
	}
	if version != int64(len(applied)) {
		t.Errorf("SchemaVersion = %d, want %d", version, len(applied))
	}

	objects := map[string]string{
		"accounts": "table", "imports": "table", "raw_records": "table",
		"transactions": "table", "categories": "table", "allocations": "table",
		"tags": "table", "transaction_tags": "table", "uncategorized": "view",
	}
	for name, kind := range objects {
		var n int
		err := s.DB.QueryRowContext(ctx,
			`SELECT count(*) FROM sqlite_master WHERE type = ? AND name = ?`, kind, name).Scan(&n)
		if err != nil {
			t.Fatal(err)
		}
		if n != 1 {
			t.Errorf("%s %q missing after migration", kind, name)
		}
	}
}

func TestPragmas(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)

	var mode string
	if err := s.DB.QueryRowContext(ctx, `PRAGMA journal_mode`).Scan(&mode); err != nil {
		t.Fatal(err)
	}
	if mode != "wal" {
		t.Errorf("journal_mode = %q, want wal", mode)
	}

	var timeout int
	if err := s.DB.QueryRowContext(ctx, `PRAGMA busy_timeout`).Scan(&timeout); err != nil {
		t.Fatal(err)
	}
	if timeout != 5000 {
		t.Errorf("busy_timeout = %d, want 5000", timeout)
	}

	// foreign_keys must be enforced, not just reported.
	_, err := s.DB.ExecContext(ctx,
		`INSERT INTO imports (source, file_name, file_sha256, account_id, row_count) VALUES ('x', 'f', 'h', 999, 0)`)
	if err == nil {
		t.Error("insert with dangling account_id succeeded, want foreign key error")
	}
}

func TestAccounts(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)

	giro, err := s.CreateAccount(ctx, Account{Slug: "giro", Name: " Girokonto ", IBAN: "de12 5001 0517 0648 4898 90", Bank: "dkb"})
	if err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}
	if giro.ID == 0 || giro.Name != "Girokonto" || giro.IBAN != "DE12500105170648489890" || giro.Currency != "EUR" {
		t.Errorf("created account = %+v", giro)
	}
	// Two accounts without IBAN must not collide on the UNIQUE column.
	for _, slug := range []string{"bar", "depot"} {
		if _, err := s.CreateAccount(ctx, Account{Slug: slug, Name: slug}); err != nil {
			t.Fatalf("CreateAccount(%s): %v", slug, err)
		}
	}

	got, err := s.AccountBySlug(ctx, "giro")
	if err != nil || got != giro {
		t.Errorf("AccountBySlug = %+v, %v; want %+v", got, err, giro)
	}
	got, err = s.AccountByIBAN(ctx, "DE12 5001 0517 0648 4898 90")
	if err != nil || got != giro {
		t.Errorf("AccountByIBAN = %+v, %v; want %+v", got, err, giro)
	}
	if _, err := s.AccountBySlug(ctx, "nope"); !errors.Is(err, ErrNotFound) {
		t.Errorf("AccountBySlug(nope) error = %v, want ErrNotFound", err)
	}

	list, err := s.ListAccounts(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 3 || list[0].Slug != "bar" || list[1].Slug != "depot" || list[2].Slug != "giro" {
		t.Errorf("ListAccounts = %+v", list)
	}

	if err := s.SetCutover(ctx, giro.ID, "2026-09-30"); err != nil {
		t.Fatalf("SetCutover: %v", err)
	}
	if got, _ := s.AccountBySlug(ctx, "giro"); got.CutoverDate != "2026-09-30" {
		t.Errorf("CutoverDate = %q", got.CutoverDate)
	}
	if err := s.SetCutover(ctx, 999, "2026-09-30"); !errors.Is(err, ErrNotFound) {
		t.Errorf("SetCutover(unknown) error = %v, want ErrNotFound", err)
	}
}

func TestReserveAccount(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	ids := map[string]int64{}
	for _, slug := range []string{"giro", "tagesgeld"} {
		a, err := s.CreateAccount(ctx, Account{Slug: slug, Name: slug})
		if err != nil {
			t.Fatal(err)
		}
		ids[slug] = a.ID
	}
	holders := func() string {
		t.Helper()
		list, err := s.ListAccounts(ctx)
		if err != nil {
			t.Fatal(err)
		}
		var out string
		for _, a := range list {
			if a.HoldsReserve {
				out += a.Slug + " "
			}
		}
		return out
	}

	steps := []struct {
		id      int64
		wantErr error
		want    string
	}{
		{ids["giro"], nil, "giro "},
		{ids["tagesgeld"], nil, "tagesgeld "}, // moves, never two
		{999, ErrNotFound, "tagesgeld "},      // a failed change keeps the old account
		{0, nil, ""},
	}
	for _, step := range steps {
		if err := s.SetReserveAccount(ctx, step.id); !errors.Is(err, step.wantErr) {
			t.Errorf("SetReserveAccount(%d) error = %v, want %v", step.id, err, step.wantErr)
		}
		if got := holders(); got != step.want {
			t.Errorf("after SetReserveAccount(%d): holders = %q, want %q", step.id, got, step.want)
		}
	}

	if err := s.SetReserve(ctx, 999, true); !errors.Is(err, ErrNotFound) {
		t.Errorf("SetReserve(unknown) error = %v, want ErrNotFound", err)
	}
}

func TestCreateAccountErrors(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	if _, err := s.CreateAccount(ctx, Account{Slug: "giro", Name: "Giro", IBAN: "DE12500105170648489890"}); err != nil {
		t.Fatal(err)
	}

	bad := []Account{
		{Slug: "giro", Name: "Duplicate slug"},
		{Slug: "other", Name: "Duplicate IBAN", IBAN: "DE12500105170648489890"},
		{Slug: "Giro", Name: "Upper case"},
		{Slug: "my account", Name: "Space"},
		{Slug: "-giro", Name: "Leading dash"},
		{Slug: "", Name: "Empty slug"},
		{Slug: "ok", Name: "  "},
	}
	for _, a := range bad {
		if _, err := s.CreateAccount(ctx, a); err == nil {
			t.Errorf("CreateAccount(%+v) succeeded, want error", a)
		}
	}
}

func TestRecurringGroups(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)

	strom, err := s.EnsureRecurringGroup(ctx, " Strom ")
	if err != nil {
		t.Fatal(err)
	}
	if again, err := s.EnsureRecurringGroup(ctx, "strom"); err != nil || again != strom {
		t.Errorf("EnsureRecurringGroup(strom) = %d, %v; want the existing group %d", again, err, strom)
	}
	if _, err := s.EnsureRecurringGroup(ctx, "  "); err == nil {
		t.Error("a group without a name was accepted")
	}
	// Contracts with the same counterparty get distinct names.
	a, err := s.CreateContractGroup(ctx, "finanzguru", "vt-1", "Strom", "yearly")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateContractGroup(ctx, "finanzguru", "vt-2", "", "sometimes"); err != nil {
		t.Fatal(err)
	}
	if err := s.RenameRecurringGroup(ctx, a, "STROM"); err == nil {
		t.Error("renaming to the name of another group was accepted")
	}
	if err := s.SetRecurringInterval(ctx, strom, "weekly"); err == nil {
		t.Error("an unknown interval was accepted")
	}
	if err := s.SetRecurringInterval(ctx, strom, IntervalQuarterly); err != nil {
		t.Fatal(err)
	}
	if err := s.SetRecurringReserve(ctx, a, true); err != nil {
		t.Fatal(err)
	}
	// Only a group that is not monthly can be covered by the reserve, and
	// becoming monthly ends it.
	covered := func(id int64) bool {
		t.Helper()
		var covers bool
		if err := s.DB.QueryRow(`SELECT covers_reserve FROM recurring_groups WHERE id = ?`, id).Scan(&covers); err != nil {
			t.Fatal(err)
		}
		return covers
	}
	monthly, err := s.EnsureRecurringGroup(ctx, "Monatlich")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SetRecurringReserve(ctx, monthly, true); err != nil || !covered(monthly) {
		t.Errorf("a group of unknown interval was not covered: %v", err)
	}
	if err := s.SetRecurringInterval(ctx, monthly, IntervalMonthly); err != nil || covered(monthly) {
		t.Errorf("a group that became monthly is still covered (error %v)", err)
	}
	if err := s.SetRecurringReserve(ctx, monthly, true); err == nil || covered(monthly) {
		t.Error("a monthly group was covered by the reserve")
	}
	if err := s.SetRecurringReserve(ctx, monthly, false); err != nil {
		t.Errorf("switching the reserve off for a monthly group: %v", err)
	}
	if err := s.SetRecurringInterval(ctx, a, IntervalHalfYearly); err != nil || !covered(a) {
		t.Errorf("another interval than monthly ended the reserve (error %v)", err)
	}
	if err := s.SetRecurringReserve(ctx, 999, true); !errors.Is(err, ErrNotFound) {
		t.Errorf("SetRecurringReserve(unknown) error = %v, want ErrNotFound", err)
	}
	if err := s.DeleteRecurringGroup(ctx, monthly); err != nil {
		t.Fatal(err)
	}
	if err := s.SetRecurringInterval(ctx, a, IntervalYearly); err != nil {
		t.Fatal(err)
	}
	groups, err := s.ListRecurringGroups(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, g := range groups {
		got = append(got, g.Name+"|"+g.Interval)
	}
	if want := "Contract vt-2| Strom|quarterly Strom 2|yearly"; strings.Join(got, " ") != want {
		t.Errorf("groups = %q, want %q", strings.Join(got, " "), want)
	}

	// Merging moves the contract along; deleting dismisses it.
	if err := s.MergeRecurringGroup(ctx, a, strom); err != nil {
		t.Fatal(err)
	}
	if id, known, _ := s.ContractGroup(ctx, "finanzguru", "vt-1"); !known || id != strom {
		t.Errorf("contract vt-1 after merge: group %d, known %v; want %d", id, known, strom)
	}
	if err := s.DeleteRecurringGroup(ctx, strom); err != nil {
		t.Fatal(err)
	}
	if id, known, _ := s.ContractGroup(ctx, "finanzguru", "vt-1"); !known || id != 0 {
		t.Errorf("contract vt-1 after delete: group %d, known %v; want dismissed", id, known)
	}
	if _, known, _ := s.ContractGroup(ctx, "finanzguru", "vt-9"); known {
		t.Error("an unseen contract is reported as known")
	}
	for name, err := range map[string]error{
		"merge unknown": s.MergeRecurringGroup(ctx, 998, 999), "delete unknown": s.DeleteRecurringGroup(ctx, 999),
		"rename unknown": s.RenameRecurringGroup(ctx, 999, "x"), "assign unknown group": s.SetRecurringGroup(ctx, 1, 999),
	} {
		if !errors.Is(err, ErrNotFound) {
			t.Errorf("%s: error = %v, want ErrNotFound", name, err)
		}
	}

	tests := []struct {
		group RecurringGroup
		want  int64
		ok    bool
	}{
		{RecurringGroup{Active: true, Interval: IntervalMonthly, Count: 3, LastCents: -1299}, -1299, true},
		{RecurringGroup{Active: true, Interval: IntervalYearly, Count: 1, LastCents: -10000}, -833, true},
		{RecurringGroup{Active: true, Interval: IntervalQuarterly, Count: 1, LastCents: 5000}, 1667, true},
		{RecurringGroup{Active: true, Interval: "", Count: 3, LastCents: -1299}, 0, false},
		{RecurringGroup{Active: true, Interval: IntervalMonthly}, 0, false},
		{RecurringGroup{Active: false, Interval: IntervalMonthly, Count: 3, LastCents: -1299}, 0, false}, // ended
	}
	for _, tt := range tests {
		if got, ok := tt.group.MonthlyCents(); got != tt.want || ok != tt.ok {
			t.Errorf("MonthlyCents(%+v) = %d, %v; want %d, %v", tt.group, got, ok, tt.want, tt.ok)
		}
	}

	// New groups are active; the flag is switched by hand.
	netflix, err := s.EnsureRecurringGroup(ctx, "Netflix")
	if err != nil {
		t.Fatal(err)
	}
	active := func() bool {
		t.Helper()
		groups, err := s.ListRecurringGroups(ctx)
		if err != nil {
			t.Fatal(err)
		}
		for _, g := range groups {
			if g.ID == netflix {
				return g.Active
			}
		}
		t.Fatal("group not listed")
		return false
	}
	if !active() {
		t.Error("a new group is not active")
	}
	if err := s.SetRecurringActive(ctx, netflix, false); err != nil || active() {
		t.Errorf("SetRecurringActive(false): error %v, still active %v", err, active())
	}
	if err := s.SetRecurringActive(ctx, 999, false); !errors.Is(err, ErrNotFound) {
		t.Errorf("SetRecurringActive(unknown) error = %v, want ErrNotFound", err)
	}

	// More than two intervals without a payment look like the end.
	lapsed := []struct {
		group RecurringGroup
		want  bool
	}{
		{RecurringGroup{Active: true, Interval: IntervalMonthly, Count: 1, LastDate: "2026-08-04"}, true},
		{RecurringGroup{Active: true, Interval: IntervalMonthly, Count: 1, LastDate: "2026-08-05"}, false},
		{RecurringGroup{Active: true, Interval: IntervalYearly, Count: 1, LastDate: "2025-01-10"}, false},
		{RecurringGroup{Active: true, Interval: IntervalYearly, Count: 1, LastDate: "2024-10-04"}, true},
		{RecurringGroup{Active: false, Interval: IntervalMonthly, Count: 1, LastDate: "2020-01-01"}, false}, // already marked
		{RecurringGroup{Active: true, Interval: "", Count: 1, LastDate: "2020-01-01"}, false},
		{RecurringGroup{Active: true, Interval: IntervalMonthly}, false},
	}
	for _, tt := range lapsed {
		if got := tt.group.Lapsed("2026-10-05"); got != tt.want {
			t.Errorf("Lapsed(%+v) = %v, want %v", tt.group, got, tt.want)
		}
	}
}

func TestRecurringReview(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	loan, err := s.EnsureRecurringGroup(ctx, "Kredit")
	if err != nil {
		t.Fatal(err)
	}
	other, err := s.EnsureRecurringGroup(ctx, "Zeitung")
	if err != nil {
		t.Fatal(err)
	}
	review := func(id int64) string {
		t.Helper()
		groups, err := s.ListRecurringGroups(ctx)
		if err != nil {
			t.Fatal(err)
		}
		for _, g := range groups {
			if g.ID == id && g.Mandatory {
				return "mandatory|" + g.Verdict
			} else if g.ID == id {
				return "|" + g.Verdict
			}
		}
		return "not listed"
	}

	steps := []struct {
		name    string
		change  func() error
		wantErr bool
		want    string // of the loan afterwards
	}{
		{"new group", func() error { return nil }, false, "|"},
		{"cancel", func() error { return s.SetRecurringVerdict(ctx, loan, VerdictCancel) }, false, "|cancel"},
		{"unknown verdict", func() error { return s.SetRecurringVerdict(ctx, loan, "maybe") }, true, "|cancel"},
		{"mandatory clears cancel", func() error { return s.SetRecurringMandatory(ctx, loan, true) }, false, "mandatory|"},
		{"cancel a mandatory group", func() error { return s.SetRecurringVerdict(ctx, loan, VerdictCancel) }, true, "mandatory|"},
		{"keep a mandatory group", func() error { return s.SetRecurringVerdict(ctx, loan, VerdictKeep) }, false, "mandatory|keep"},
		{"not mandatory keeps the verdict", func() error { return s.SetRecurringMandatory(ctx, loan, false) }, false, "|keep"},
		{"mandatory keeps keep", func() error { return s.SetRecurringMandatory(ctx, loan, true) }, false, "mandatory|keep"},
		{"undecided", func() error { return s.SetRecurringVerdict(ctx, loan, "") }, false, "mandatory|"},
		// The group merged into keeps its own review.
		{"merge", func() error { return s.MergeRecurringGroup(ctx, other, loan) }, false, "mandatory|"},
	}
	for _, step := range steps {
		if err := step.change(); (err != nil) != step.wantErr {
			t.Errorf("%s: error = %v, want an error: %v", step.name, err, step.wantErr)
		}
		if got := review(loan); got != step.want {
			t.Errorf("%s: review = %q, want %q", step.name, got, step.want)
		}
	}
	for name, err := range map[string]error{
		"mandatory": s.SetRecurringMandatory(ctx, 999, true), "verdict": s.SetRecurringVerdict(ctx, 999, VerdictKeep),
	} {
		if !errors.Is(err, ErrNotFound) {
			t.Errorf("%s of an unknown group: error = %v, want ErrNotFound", name, err)
		}
	}

	yearly := []struct {
		group RecurringGroup
		want  int64
		ok    bool
	}{
		{RecurringGroup{Active: true, Interval: IntervalMonthly, Count: 3, LastCents: -1299}, -15588, true},
		{RecurringGroup{Active: true, Interval: IntervalQuarterly, Count: 1, LastCents: -5000}, -20000, true},
		{RecurringGroup{Active: true, Interval: IntervalHalfYearly, Count: 1, LastCents: -5000}, -10000, true},
		{RecurringGroup{Active: true, Interval: IntervalYearly, Count: 1, LastCents: -10000}, -10000, true},
		{RecurringGroup{Active: true, Interval: "", Count: 3, LastCents: -1299}, 0, false},
		{RecurringGroup{Active: false, Interval: IntervalMonthly, Count: 3, LastCents: -1299}, 0, false},
	}
	for _, tt := range yearly {
		if got, ok := tt.group.YearlyCents(); got != tt.want || ok != tt.ok {
			t.Errorf("YearlyCents(%+v) = %d, %v; want %d, %v", tt.group, got, ok, tt.want, tt.ok)
		}
	}
}

func TestRecurringCosts(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	account, err := s.CreateAccount(ctx, Account{Slug: "giro", Name: "Giro"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB.Exec(`
		INSERT INTO imports (id, source, file_name, file_sha256, row_count) VALUES (1, 'test', 'test.csv', 'x', 1);
		INSERT INTO raw_records (id, import_id, line_no, data) VALUES (1, 1, 1, '{}')`); err != nil {
		t.Fatal(err)
	}
	wohnen, _, err := s.EnsureCategory(ctx, "wohnen", "Wohnen", 0)
	if err != nil {
		t.Fatal(err)
	}
	strom, _, err := s.EnsureCategory(ctx, "wohnen/strom", "Strom", wohnen)
	if err != nil {
		t.Fatal(err)
	}
	freizeit, _, err := s.EnsureCategory(ctx, "freizeit", "Freizeit", 0)
	if err != nil {
		t.Fatal(err)
	}

	type tx struct {
		date     string
		cents    int64
		category int64 // 0 = uncategorized
	}
	groups := []struct {
		name, interval string
		txs            []tx
		want           RecurringCost // the figures only
		change         int64
		changed        bool
	}{
		{
			// Compared with the payment a year earlier, not the one before;
			// most transactions decide the category.
			"Streaming", IntervalMonthly,
			[]tx{{"2025-10-10", -1299, freizeit}, {"2026-08-10", -1499, freizeit}, {"2026-09-10", -1499, 0}},
			RecurringCost{PaidYearCents: -4297, PrevDate: "2025-10-10", PrevCents: -1299, CategoryID: freizeit, CategoryName: "Freizeit"},
			-200, true,
		},
		{
			// A sub category counts for its main category; the newest
			// payment is more than twelve months before the date asked for.
			"Versicherung", IntervalYearly,
			[]tx{{"2024-10-01", -30000, strom}, {"2025-10-01", -32000, strom}},
			RecurringCost{PrevDate: "2024-10-01", PrevCents: -30000, CategoryID: wohnen, CategoryName: "Wohnen"},
			-2000, true,
		},
		{
			// Younger than a year: compared with its first payment. With
			// as many transactions in each category, the newest decides.
			"Fitness", IntervalMonthly,
			[]tx{{"2026-08-01", -700, freizeit}, {"2026-09-01", -700, wohnen}},
			RecurringCost{PaidYearCents: -1400, PrevDate: "2026-08-01", PrevCents: -700, CategoryID: wohnen, CategoryName: "Wohnen"},
			0, true,
		},
		{
			"Neu", IntervalMonthly,
			[]tx{{"2026-09-01", -500, 0}},
			RecurringCost{PaidYearCents: -500},
			0, false,
		},
		{"Leer", "", nil, RecurringCost{}, 0, false},
	}
	n := 0
	for _, g := range groups {
		id, err := s.EnsureRecurringGroup(ctx, g.name)
		if err != nil {
			t.Fatal(err)
		}
		if err := s.SetRecurringInterval(ctx, id, g.interval); err != nil {
			t.Fatal(err)
		}
		for _, x := range g.txs {
			n++
			res, err := s.DB.Exec(`
				INSERT INTO transactions (account_id, raw_record_id, source, dedup_key, booking_date, amount_cents, recurring_group_id)
				VALUES (?, 1, 'test', ?, ?, ?, ?)`, account.ID, n, x.date, x.cents, id)
			if err != nil {
				t.Fatal(err)
			}
			if x.category == 0 {
				continue
			}
			txID, _ := res.LastInsertId()
			if _, err := s.DB.Exec(`INSERT INTO allocations (transaction_id, category_id, amount_cents, source) VALUES (?, ?, ?, 'manual')`,
				txID, x.category, x.cents); err != nil {
				t.Fatal(err)
			}
		}
	}

	costs, err := s.ListRecurringCosts(ctx, "2026-10-05")
	if err != nil {
		t.Fatal(err)
	}
	if len(costs) != len(groups) {
		t.Fatalf("%d groups listed, want %d", len(costs), len(groups))
	}
	byName := map[string]RecurringCost{}
	for _, c := range costs {
		byName[c.Name] = c
	}
	for _, g := range groups {
		got := byName[g.name]
		if got.Count != len(g.txs) {
			t.Errorf("%s: Count = %d, want %d", g.name, got.Count, len(g.txs))
		}
		figures := got
		figures.RecurringGroup = RecurringGroup{}
		if figures != g.want {
			t.Errorf("%s: figures = %+v, want %+v", g.name, figures, g.want)
		}
		if change, ok := got.ChangeCents(); change != g.change || ok != g.changed {
			t.Errorf("%s: ChangeCents = %d, %v; want %d, %v", g.name, change, ok, g.change, g.changed)
		}
	}

	// The change of one payment, for the payments of a year.
	unknown, ended := byName["Streaming"], byName["Streaming"]
	unknown.Interval, ended.Active = "", false
	yearly := []struct {
		name string
		cost RecurringCost
		want int64
		ok   bool
	}{
		{"monthly", byName["Streaming"], -2400, true},
		{"yearly", byName["Versicherung"], -2000, true},
		{"unchanged", byName["Fitness"], 0, true},
		{"no earlier payment", byName["Neu"], 0, false},
		{"unknown interval", unknown, 0, false},
		{"ended", ended, 0, false},
	}
	for _, tt := range yearly {
		if got, ok := tt.cost.YearlyChangeCents(); got != tt.want || ok != tt.ok {
			t.Errorf("YearlyChangeCents, %s = %d, %v; want %d, %v", tt.name, got, ok, tt.want, tt.ok)
		}
	}
}
