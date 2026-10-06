// Command budgeteer is the single binary for the Budgeteer personal finance
// app. Commands are thin wrappers; the logic lives in internal/.
package main

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"text/tabwriter"
	"time"

	"github.com/alecthomas/kong"

	"github.com/GumboYaYa/budgeteer/internal/export"
	"github.com/GumboYaYa/budgeteer/internal/importer"
	"github.com/GumboYaYa/budgeteer/internal/importer/dkb"
	"github.com/GumboYaYa/budgeteer/internal/importer/finanzguru"
	"github.com/GumboYaYa/budgeteer/internal/store"
	"github.com/GumboYaYa/budgeteer/internal/web"
)

type cli struct {
	DB string `help:"Path to the SQLite database file." default:"./data/budgeteer.db" type:"path" env:"BUDGETEER_DB"`

	Serve   serveCmd   `cmd:"" help:"Start the web UI."`
	Migrate migrateCmd `cmd:"" help:"Create or update the database schema."`
	Account struct {
		Add        accountAddCmd        `cmd:"" help:"Add one of your own bank accounts."`
		List       accountListCmd       `cmd:"" help:"List accounts."`
		SetCutover accountSetCutoverCmd `cmd:"" help:"Set the date up to which Finanzguru provides an account's transactions; bank exports take over after it."`
	} `cmd:"" help:"Manage accounts."`
	Import struct {
		Finanzguru importFinanzguruCmd `cmd:"" help:"Import a Finanzguru export. Can be repeated with newer exports; only new transactions are added."`
		DKB        importDKBCmd        `cmd:"" name:"dkb" help:"Import a DKB account export (CSV). Exports may overlap; only new transactions are added."`
	} `cmd:"" help:"Import transactions from a file."`
	Export exportCmd `cmd:"" help:"Write all data as CSV or Parquet files."`
	Backup backupCmd `cmd:"" help:"Write a consistent copy of the database."`
}

// dbPath is the database location, for commands that keep files next to it.
type dbPath string

// migrated lists the migrations applied while opening the database.
type migrated []string

type serveCmd struct {
	Addr string `default:"localhost:8080" help:"Address to listen on. Keep it on localhost: the UI has no login."`
}

func (c serveCmd) Run(ctx context.Context, st *store.Store, db dbPath) error {
	listener, err := net.Listen("tcp", c.Addr)
	if err != nil {
		return err
	}
	srv := &http.Server{
		Handler:           web.New(st, web.Options{RawDir: rawDir(db), Addr: c.Addr}),
		ReadHeaderTimeout: 10 * time.Second,
	}

	ctx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer stop()
	go func() {
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		srv.Shutdown(shutdown)
	}()

	fmt.Printf("Budgeteer is running at http://%s (Ctrl+C to stop)\n", listener.Addr())
	if err := srv.Serve(listener); !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

// rawDir is where the originals of imported files are kept.
func rawDir(db dbPath) string {
	return filepath.Join(filepath.Dir(string(db)), "raw")
}

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

type accountSetCutoverCmd struct {
	Slug string `required:"" help:"Account to change."`
	Date string `required:"" placeholder:"YYYY-MM-DD" help:"Last day covered by Finanzguru."`
}

func (c accountSetCutoverCmd) Run(ctx context.Context, st *store.Store) error {
	if err := importer.SetCutover(ctx, st, c.Slug, c.Date, finanzguru.Source); err != nil {
		return err
	}
	fmt.Printf("cut-over date of %s set to %s\n", c.Slug, c.Date)
	return nil
}

type importDKBCmd struct {
	File    string `arg:"" type:"existingfile" help:"CSV export of the account's transactions from DKB."`
	Account string `required:"" help:"Slug of the account the file belongs to (see: budgeteer account list)."`
	Force   bool   `help:"Process the file even if it was imported before, and overwrite the details of existing transactions with those in the file. Categories and transfer flags set by hand are kept."`
}

func (c importDKBCmd) Run(ctx context.Context, st *store.Store, db dbPath) error {
	f, err := os.Open(c.File)
	if err != nil {
		return err
	}
	defer f.Close()

	summary, err := importer.Run(ctx, st, dkb.Parser{Account: c.Account}, f, c.File, importer.Options{
		RawDir:  rawDir(db),
		Account: c.Account,
		Force:   c.Force,
	})
	if err != nil {
		return err
	}
	printSummary(summary)
	return nil
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
		RawDir:  rawDir(db),
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

type exportCmd struct {
	Format string `enum:"csv,parquet" default:"csv" help:"File format: csv or parquet."`
	Out    string `type:"path" placeholder:"DIR" help:"Directory to write to. Default: export/ next to the database."`
}

func (c exportCmd) Run(ctx context.Context, st *store.Store, db dbPath) error {
	dir := c.Out
	if dir == "" {
		dir = filepath.Join(filepath.Dir(string(db)), "export")
	}
	files, err := export.Run(ctx, st, dir, c.Format)
	if err != nil {
		return err
	}
	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	for _, f := range files {
		fmt.Fprintf(w, "%s\t%d rows\n", f.Path, f.Rows)
	}
	return w.Flush()
}

type backupCmd struct {
	Out string `type:"path" placeholder:"DIR" help:"Directory to write to. Default: backups/ next to the database."`
}

func (c backupCmd) Run(ctx context.Context, st *store.Store, db dbPath) error {
	dir := c.Out
	if dir == "" {
		dir = filepath.Join(filepath.Dir(string(db)), "backups")
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	path := filepath.Join(dir, "budgeteer-"+time.Now().Format("20060102-150405")+".db")
	if err := st.Backup(ctx, path); err != nil {
		return err
	}
	fmt.Println("backup written and verified:", path)
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
	if s.SkippedPending > 0 {
		fmt.Printf("not booked yet:        %d (they come with a later export)\n", s.SkippedPending)
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
