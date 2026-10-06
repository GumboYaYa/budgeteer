package store

import (
	"context"
	"errors"
	"path/filepath"
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
