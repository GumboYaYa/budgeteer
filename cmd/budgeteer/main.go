// Command budgeteer is the single binary for the Budgeteer personal finance
// app. Commands are thin wrappers; the logic lives in internal/.
package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"text/tabwriter"

	"github.com/alecthomas/kong"

	"github.com/GumboYaYa/budgeteer/internal/importer"
	"github.com/GumboYaYa/budgeteer/internal/importer/finanzguru"
	"github.com/GumboYaYa/budgeteer/internal/store"
)

type cli struct {
	DB string `help:"Path to the SQLite database file." default:"./data/budgeteer.db" type:"path" env:"BUDGETEER_DB"`

	Migrate migrateCmd `cmd:"" help:"Create or update the database schema."`
	Account struct {
		Add  accountAddCmd  `cmd:"" help:"Add one of your own bank accounts."`
		List accountListCmd `cmd:"" help:"List accounts."`
	} `cmd:"" help:"Manage accounts."`
	Import struct {
		Finanzguru importFinanzguruCmd `cmd:"" help:"Import a Finanzguru export. Can be repeated with newer exports; only new transactions are added."`
	} `cmd:"" help:"Import transactions from a file."`
}

// dbPath is the database location, for commands that keep files next to it.
type dbPath string

// migrated lists the migrations applied while opening the database.
type migrated []string

type migrateCmd struct{}

func (migrateCmd) Run(ctx context.Context, st *store.Store, applied migrated) error {
	for _, name := range applied {
		fmt.Println("applied", name)
	}
	version, err := st.SchemaVersion(ctx)
	if err != nil {
		return err
	}
	fmt.Printf("database is at schema version %d\n", version)
	return nil
}

type accountAddCmd struct {
	Slug string `required:"" help:"Short key used on the command line, e.g. giro."`
	Name string `required:"" help:"Display name."`
	IBAN string `name:"iban" help:"IBAN of the account."`
	Bank string `help:"Bank identifier, e.g. dkb."`
}

func (c accountAddCmd) Run(ctx context.Context, st *store.Store) error {
	a, err := st.CreateAccount(ctx, store.Account{Slug: c.Slug, Name: c.Name, IBAN: c.IBAN, Bank: c.Bank})
	if err != nil {
		return err
	}
	fmt.Printf("added account %s (%s)\n", a.Slug, a.Name)
	return nil
}

type accountListCmd struct{}

func (accountListCmd) Run(ctx context.Context, st *store.Store) error {
	accounts, err := st.ListAccounts(ctx)
	if err != nil {
		return err
	}
	if len(accounts) == 0 {
		fmt.Println("no accounts yet; add one with: budgeteer account add --slug <slug> --name <name>")
		return nil
	}
	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "SLUG\tNAME\tIBAN\tBANK\tCURRENCY\tCUT-OVER")
	for _, a := range accounts {
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\n",
			a.Slug, a.Name, dash(a.IBAN), dash(a.Bank), a.Currency, dash(a.CutoverDate))
	}
	return w.Flush()
}

type importFinanzguruCmd struct {
	File    string `arg:"" type:"existingfile" help:"CSV export from Finanzguru."`
	Cutover string `placeholder:"YYYY-MM-DD" help:"Import only transactions up to this date and store it as the cut-over date of the accounts in the file."`
	Force   bool   `help:"Process the file even if it was imported before, and overwrite the details of existing transactions with those in the file. Categories and transfer flags set by hand are kept."`
}

func (c importFinanzguruCmd) Run(ctx context.Context, st *store.Store, db dbPath) error {
	f, err := os.Open(c.File)
	if err != nil {
		return err
	}
	defer f.Close()

	summary, err := importer.Run(ctx, st, finanzguru.Parser{}, f, c.File, importer.Options{
		RawDir:  filepath.Join(filepath.Dir(string(db)), "raw"),
		Cutover: c.Cutover,
		Force:   c.Force,
	})
	if err != nil {
		return err
	}
	printSummary(summary)
	if summary.AlreadyImported && c.Cutover != "" {
		fmt.Println("the cut-over date was not changed")
	}
	return nil
}

func printSummary(s importer.Summary) {
	if s.AlreadyImported {
		fmt.Println("this exact file was imported before; nothing changed (use --force to process it again)")
		return
	}
	fmt.Printf("rows in file:          %d\n", s.Rows)
	fmt.Printf("new transactions:      %d\n", s.New)
	fmt.Printf("already imported:      %d\n", s.Duplicates)
	fmt.Printf("categories refreshed:  %d\n", s.Updated)
	if s.FieldsUpdated > 0 {
		fmt.Printf("details overwritten:   %d\n", s.FieldsUpdated)
	}
	if s.SkippedCutover > 0 {
		fmt.Printf("skipped by cut-over:   %d\n", s.SkippedCutover)
	}
	if len(s.AccountsCreated) > 0 {
		fmt.Printf("accounts created:      %s\n", strings.Join(s.AccountsCreated, ", "))
	}
	if s.CategoriesCreated > 0 {
		fmt.Printf("categories created:    %d\n", s.CategoriesCreated)
	}
	if s.SkippedSuperseded > 0 {
		fmt.Printf("split originals:       %d (their parts are imported instead)\n", s.SkippedSuperseded)
	}
	if s.Removed > 0 {
		fmt.Printf("removed, now split:    %d\n", s.Removed)
	}
}

func dash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

func main() {
	var c cli
	kctx := kong.Parse(&c,
		kong.Name("budgeteer"),
		kong.Description("Local personal finance: import, categorize and analyze bank transactions."),
		kong.UsageOnError(),
	)

	ctx := context.Background()
	st, err := store.Open(c.DB)
	kctx.FatalIfErrorf(err)
	defer st.Close()

	// Every command works on a current schema, so there is no way to run
	// against a half-migrated database.
	applied, err := st.Migrate(ctx)
	kctx.FatalIfErrorf(err)

	kctx.BindTo(ctx, (*context.Context)(nil))
	kctx.FatalIfErrorf(kctx.Run(st, migrated(applied), dbPath(c.DB)))
}
