// Package importer is the shared import pipeline: file identity, the raw
// layer, deduplication and the cut-over between sources. Each source lives in
// a sub-package and only implements Parser.
package importer

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"github.com/GumboYaYa/budgeteer/internal/slug"
	"github.com/GumboYaYa/budgeteer/internal/store"
)

// CutoverSide says which side of an account's cut-over date a source owns.
type CutoverSide int

const (
	// UpToCutover sources provide history: rows with booking_date <= cut-over.
	UpToCutover CutoverSide = iota
	// AfterCutover sources take over afterwards: rows with booking_date > cut-over.
	AfterCutover
)

// Parser turns one source's file format into candidates. Implementations are
// pure: they do not touch the database.
type Parser interface {
	// Source is the value stored in imports.source and transactions.source.
	Source() string
	Side() CutoverSide
	// Read splits the file into its data rows.
	Read(r io.Reader) ([]RawRow, error)
	// Normalize maps every row to a candidate, in the same order.
	Normalize(rows []RawRow) ([]Candidate, error)
}

// RawRow is one data row exactly as it appears in the file.
type RawRow struct {
	LineNo int               // physical line in the file, 1-based
	Fields map[string]string // header → value
}

// Candidate is a normalized row before it is matched against the database.
type Candidate struct {
	Row RawRow

	// Account the row belongs to; it is matched by IBAN and created if missing.
	AccountIBAN string
	AccountName string

	// Tx holds the normalized fields. IDs are filled in by the pipeline.
	Tx store.Transaction

	// Superseded marks a row that other rows of the same file replace (the
	// original of a split). It is kept in the raw layer but must not exist as
	// a transaction.
	Superseded bool

	// DayEndBalance is the account balance after this row, set only on the
	// row that is the last of its day for the account, and only if the source
	// states balances.
	DayEndBalance    int64
	HasDayEndBalance bool

	// Category as named by the source; both empty means uncategorized.
	MainCategory string
	SubCategory  string
	Tags         []string
}

type Options struct {
	// RawDir receives a copy of the original file. Empty disables the copy.
	RawDir string
	// Cutover (YYYY-MM-DD), if set, becomes the cut-over date of every account
	// in the file before the rows are filtered.
	Cutover string
	// Force processes a file again even if the identical file was imported
	// before, and overwrites the source fields of transactions that already
	// exist. Data set by hand (category, transfer flag) is still kept.
	Force bool
}

// Summary reports what an import did.
type Summary struct {
	AlreadyImported   bool // the identical file was imported before; nothing changed
	Rows              int  // data rows in the file
	New               int  // transactions inserted
	Duplicates        int  // rows whose transaction already existed
	Updated           int  // existing transactions whose category was refreshed
	FieldsUpdated     int  // existing transactions whose details were overwritten (Force)
	SkippedCutover    int  // rows on the other side of the cut-over date
	SkippedSuperseded int  // rows replaced by other rows of the file (split originals)
	Removed           int  // existing transactions deleted because they are now superseded
	AccountsCreated   []string
	CategoriesCreated int
}

// Run imports one file. It is idempotent: the identical file is recognised by
// its SHA-256, and a row by its dedup key. Everything happens in one database
// transaction; on error nothing is changed.
func Run(ctx context.Context, st *store.Store, p Parser, file io.Reader, fileName string, opts Options) (Summary, error) {
	if opts.Cutover != "" {
		if _, err := time.Parse(time.DateOnly, opts.Cutover); err != nil {
			return Summary{}, fmt.Errorf("invalid cut-over date %q: want YYYY-MM-DD", opts.Cutover)
		}
	}
	content, err := io.ReadAll(file)
	if err != nil {
		return Summary{}, fmt.Errorf("read %s: %w", fileName, err)
	}
	sum := sha256.Sum256(content)
	hash := hex.EncodeToString(sum[:])
	fileName = filepath.Base(fileName)

	importID, err := st.ImportIDBySHA256(ctx, hash)
	if err != nil {
		return Summary{}, err
	}
	if importID != 0 && !opts.Force {
		return Summary{AlreadyImported: true}, nil
	}

	rows, err := p.Read(bytes.NewReader(content))
	if err != nil {
		return Summary{}, fmt.Errorf("%s: %w", fileName, err)
	}
	candidates, err := p.Normalize(rows)
	if err != nil {
		return Summary{}, fmt.Errorf("%s: %w", fileName, err)
	}

	var summary Summary
	err = st.InTx(ctx, func(tx *store.Store) error {
		run := &run{tx: tx, parser: p, opts: opts, importID: importID, summary: Summary{Rows: len(rows)}}
		if err := run.importAll(ctx, fileName, hash, candidates); err != nil {
			return err
		}
		// Written before the commit so that a stored import always has its
		// original on disk; a stray file after a failed commit is harmless.
		if opts.RawDir != "" {
			if err := saveOriginal(opts.RawDir, hash, fileName, content); err != nil {
				return err
			}
		}
		summary = run.summary
		return nil
	})
	if err != nil {
		return Summary{}, err
	}
	return summary, nil
}

func saveOriginal(dir, hash, fileName string, content []byte) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("keep original file: %w", err)
	}
	if err := os.WriteFile(filepath.Join(dir, hash[:12]+"_"+fileName), content, 0o644); err != nil {
		return fmt.Errorf("keep original file: %w", err)
	}
	return nil
}

// run is the state of one import inside its database transaction.
type run struct {
	tx      *store.Store
	parser  Parser
	opts    Options
	summary Summary

	importID   int64
	accounts   map[string]store.Account // by normalized IBAN
	categories map[string]int64         // by slug
	balances   map[int64]balance        // by account id: newest stated balance among imported days
}

type balance struct {
	cents int64
	date  string
}

func (r *run) importAll(ctx context.Context, fileName, hash string, candidates []Candidate) error {
	r.accounts = make(map[string]store.Account)
	r.categories = make(map[string]int64)
	r.balances = make(map[int64]balance)

	var err error
	// importID is already set when an identical file is processed again.
	if r.importID == 0 {
		r.importID, err = r.tx.CreateImport(ctx, store.Import{
			Source: r.parser.Source(), FileName: fileName, SHA256: hash, RowCount: r.summary.Rows,
		})
		if err != nil {
			return err
		}
	}
	known, err := r.tx.TransactionIDsByDedupKey(ctx, r.parser.Source())
	if err != nil {
		return err
	}

	for _, c := range candidates {
		account, err := r.account(ctx, c)
		if err != nil {
			return fmt.Errorf("line %d: %w", c.Row.LineNo, err)
		}

		if !r.ownsDate(account, c.Tx.BookingDate) {
			if _, err := r.storeRaw(ctx, c.Row); err != nil {
				return err
			}
			r.summary.SkippedCutover++
			continue
		}

		if c.HasDayEndBalance && c.Tx.BookingDate >= r.balances[account.ID].date {
			r.balances[account.ID] = balance{cents: c.DayEndBalance, date: c.Tx.BookingDate}
		}

		if c.Superseded {
			if _, err := r.storeRaw(ctx, c.Row); err != nil {
				return err
			}
			r.summary.SkippedSuperseded++
			// The row was imported as a transaction before it was split.
			if txID, ok := known[c.Tx.DedupKey]; ok {
				if err := r.tx.DeleteTransaction(ctx, txID); err != nil {
					return fmt.Errorf("line %d: %w", c.Row.LineNo, err)
				}
				delete(known, c.Tx.DedupKey)
				r.summary.Removed++
			}
			continue
		}

		categoryID, err := r.category(ctx, c)
		if err != nil {
			return fmt.Errorf("line %d: %w", c.Row.LineNo, err)
		}

		if txID, ok := known[c.Tx.DedupKey]; ok {
			r.summary.Duplicates++
			if r.opts.Force {
				if err := r.refreshFields(ctx, txID, account.ID, c.Tx); err != nil {
					return fmt.Errorf("line %d: %w", c.Row.LineNo, err)
				}
				for _, tag := range c.Tags {
					if err := r.tx.TagTransaction(ctx, txID, tag); err != nil {
						return fmt.Errorf("line %d: %w", c.Row.LineNo, err)
					}
				}
			}
			if err := r.refreshCategory(ctx, txID, c.Tx.AmountCents, categoryID); err != nil {
				return fmt.Errorf("line %d: %w", c.Row.LineNo, err)
			}
			continue
		}

		t := c.Tx
		t.AccountID = account.ID
		t.Source = r.parser.Source()
		if t.RawRecordID, err = r.storeRaw(ctx, c.Row); err != nil {
			return err
		}
		if t.ID, err = r.tx.InsertTransaction(ctx, t); err != nil {
			return fmt.Errorf("line %d: %w", c.Row.LineNo, err)
		}
		known[t.DedupKey] = t.ID
		r.summary.New++

		if categoryID != 0 {
			err := r.tx.InsertAllocation(ctx, store.Allocation{
				TransactionID: t.ID, CategoryID: categoryID, AmountCents: t.AmountCents, Source: store.SourceFinanzguru,
			})
			if err != nil {
				return fmt.Errorf("line %d: %w", c.Row.LineNo, err)
			}
		}
		for _, tag := range c.Tags {
			if err := r.tx.TagTransaction(ctx, t.ID, tag); err != nil {
				return fmt.Errorf("line %d: %w", c.Row.LineNo, err)
			}
		}
	}

	// Remember the newest balance the file states for each account. An older
	// file must not replace a newer balance.
	for _, account := range r.accounts {
		b, ok := r.balances[account.ID]
		if !ok || b.date < account.BalanceDate {
			continue
		}
		if err := r.tx.SetBalance(ctx, account.ID, b.cents, b.date); err != nil {
			return err
		}
	}
	return nil
}

// ownsDate reports whether this source is responsible for a booking date,
// given the account's cut-over date.
func (r *run) ownsDate(account store.Account, bookingDate string) bool {
	if account.CutoverDate == "" {
		return true
	}
	if r.parser.Side() == UpToCutover {
		return bookingDate <= account.CutoverDate
	}
	return bookingDate > account.CutoverDate
}

func (r *run) storeRaw(ctx context.Context, row RawRow) (int64, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(row.Fields); err != nil {
		return 0, fmt.Errorf("line %d: %w", row.LineNo, err)
	}
	return r.tx.PutRawRecord(ctx, r.importID, row.LineNo, string(bytes.TrimSpace(buf.Bytes())))
}

// account finds the candidate's account by IBAN or creates it, and applies a
// new cut-over date the first time the account is seen in this import.
func (r *run) account(ctx context.Context, c Candidate) (store.Account, error) {
	iban := store.NormalizeIBAN(c.AccountIBAN)
	if iban == "" {
		return store.Account{}, errors.New("row has no account IBAN")
	}
	if a, ok := r.accounts[iban]; ok {
		return a, nil
	}

	a, err := r.tx.AccountByIBAN(ctx, iban)
	if errors.Is(err, store.ErrNotFound) {
		a, err = r.createAccount(ctx, iban, c.AccountName)
	}
	if err != nil {
		return store.Account{}, err
	}

	if r.opts.Cutover != "" && r.opts.Cutover != a.CutoverDate {
		if err := r.checkCutover(ctx, a); err != nil {
			return store.Account{}, err
		}
		if err := r.tx.SetCutover(ctx, a.ID, r.opts.Cutover); err != nil {
			return store.Account{}, err
		}
		a.CutoverDate = r.opts.Cutover
	}
	r.accounts[iban] = a
	return a, nil
}

func (r *run) createAccount(ctx context.Context, iban, name string) (store.Account, error) {
	if name == "" {
		name = iban
	}
	base := slug.Make(name)
	if base == "" {
		base = "konto"
	}
	candidate := base
	for n := 2; ; n++ {
		_, err := r.tx.AccountBySlug(ctx, candidate)
		if errors.Is(err, store.ErrNotFound) {
			break
		}
		if err != nil {
			return store.Account{}, err
		}
		candidate = base + "-" + strconv.Itoa(n)
	}
	a, err := r.tx.CreateAccount(ctx, store.Account{Slug: candidate, Name: name, IBAN: iban})
	if err != nil {
		return store.Account{}, err
	}
	r.summary.AccountsCreated = append(r.summary.AccountsCreated, a.Slug)
	return a, nil
}

// checkCutover rejects a new cut-over date that would leave a day covered by
// two sources, because the same transaction would then exist twice.
func (r *run) checkCutover(ctx context.Context, a store.Account) error {
	cutover := r.opts.Cutover
	other, err := r.tx.EarliestBookingDateOtherSources(ctx, a.ID, r.parser.Source())
	if err != nil {
		return err
	}
	if other != "" && other <= cutover {
		return fmt.Errorf("cut-over %s rejected: account %s already has transactions from another source since %s; choose a date before that",
			cutover, a.Slug, other)
	}
	latest, err := r.tx.LatestBookingDate(ctx, a.ID, r.parser.Source())
	if err != nil {
		return err
	}
	if latest > cutover {
		return fmt.Errorf("cut-over %s rejected: account %s already has %s transactions up to %s; choose that date or a later one",
			cutover, a.Slug, r.parser.Source(), latest)
	}
	return nil
}

// category returns the id of the candidate's category (0 if uncategorized),
// creating the main and sub category as needed.
func (r *run) category(ctx context.Context, c Candidate) (int64, error) {
	main, sub := c.MainCategory, c.SubCategory
	if main == "" {
		main, sub = sub, ""
	}
	if main == "" {
		return 0, nil
	}

	mainSlug := slug.Make(main)
	if mainSlug == "" {
		return 0, fmt.Errorf("category name %q has no letters or digits", main)
	}
	id, err := r.ensureCategory(ctx, mainSlug, main, 0)
	if err != nil {
		return 0, err
	}
	if sub != "" {
		subSlug := slug.Make(sub)
		if subSlug == "" {
			return 0, fmt.Errorf("category name %q has no letters or digits", sub)
		}
		if id, err = r.ensureCategory(ctx, mainSlug+"/"+subSlug, sub, id); err != nil {
			return 0, err
		}
	}
	return id, nil
}

func (r *run) ensureCategory(ctx context.Context, slug, name string, parentID int64) (int64, error) {
	if id, ok := r.categories[slug]; ok {
		return id, nil
	}
	id, created, err := r.tx.EnsureCategory(ctx, slug, name, parentID)
	if err != nil {
		return 0, err
	}
	if created {
		r.summary.CategoriesCreated++
	}
	r.categories[slug] = id
	return id, nil
}

// refreshFields overwrites the source fields of an existing transaction with
// those of the file. A transfer flag that was set by hand is kept.
func (r *run) refreshFields(ctx context.Context, txID, accountID int64, from store.Transaction) error {
	current, err := r.tx.TransactionByID(ctx, txID)
	if err != nil {
		return err
	}
	want := from
	want.ID, want.RawRecordID = current.ID, current.RawRecordID
	want.Source, want.DedupKey = current.Source, current.DedupKey
	want.AccountID = accountID
	want.TransferManual = current.TransferManual
	if current.TransferManual {
		want.IsTransfer = current.IsTransfer
	}
	if want.Currency == "" {
		want.Currency = "EUR"
	}
	if want == current {
		return nil
	}
	if err := r.tx.UpdateTransaction(ctx, want); err != nil {
		return err
	}
	r.summary.FieldsUpdated++
	return nil
}

// refreshCategory brings an existing transaction's category in line with the
// source, unless it was categorized by hand. A category that the source no
// longer provides is kept.
func (r *run) refreshCategory(ctx context.Context, txID, amountCents, categoryID int64) error {
	if categoryID == 0 {
		return nil
	}
	allocations, err := r.tx.Allocations(ctx, txID)
	if err != nil {
		return err
	}
	switch {
	case len(allocations) == 0:
		err = r.tx.InsertAllocation(ctx, store.Allocation{
			TransactionID: txID, CategoryID: categoryID, AmountCents: amountCents, Source: store.SourceFinanzguru,
		})
	case len(allocations) == 1 && allocations[0].Source == store.SourceFinanzguru && allocations[0].CategoryID != categoryID:
		err = r.tx.SetAllocationCategory(ctx, allocations[0].ID, categoryID)
	default:
		return nil
	}
	if err != nil {
		return err
	}
	r.summary.Updated++
	return nil
}
