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
	// OwnAccountTransfers reports whether a row whose counterparty reference
	// is the IBAN of another own account should be flagged as a transfer.
	// Sources that state transfers themselves return false.
	OwnAccountTransfers() bool
	// Read splits the file into its data rows.
	Read(r io.Reader) (File, error)
	// Normalize maps every row to a candidate, in the same order.
	Normalize(rows []RawRow) ([]Candidate, error)
}

// File is the content of an import file.
type File struct {
	Rows []RawRow
	// Balance is the account balance at the end of BalanceDate, if the file
	// states one for the whole file (empty BalanceDate otherwise).
	Balance     int64
	BalanceDate string // YYYY-MM-DD
}

// RawRow is one data row exactly as it appears in the file.
type RawRow struct {
	LineNo int               // physical line in the file, 1-based
	Fields map[string]string // header → value
}

// Candidate is a normalized row before it is matched against the database.
type Candidate struct {
	Row RawRow

	// Account the row belongs to; it is matched by IBAN and created if
	// missing. Unused when Options.Account names the account.
	AccountIBAN string
	AccountName string

	// Pending marks a row the bank has not booked yet. It is kept in the raw
	// layer only: its text can still change, and it comes back as booked in a
	// later file.
	Pending bool

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

	// ContractID identifies the contract (a recurring series) the source
	// assigns the row to; empty if none. ContractInterval is one of the
	// store.Interval values, or empty if the source does not say.
	ContractID       string
	ContractInterval string
}

type Options struct {
	// Account is the slug of the account all rows belong to, for sources
	// whose files cover one account and do not identify it reliably.
	Account string
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
	SkippedPending    int  // rows the bank has not booked yet
	SkippedSuperseded int  // rows replaced by other rows of the file (split originals)
	Removed           int  // existing transactions deleted because they are now superseded
	AccountsCreated   []string
	CategoriesCreated int
	GroupsCreated     int // recurring groups created from contracts of the source
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

	parsed, err := p.Read(bytes.NewReader(content))
	if err != nil {
		return Summary{}, fmt.Errorf("%s: %w", fileName, err)
	}
	rows := parsed.Rows
	candidates, err := p.Normalize(rows)
	if err != nil {
		return Summary{}, fmt.Errorf("%s: %w", fileName, err)
	}

	var summary Summary
	err = st.InTx(ctx, func(tx *store.Store) error {
		run := &run{tx: tx, parser: p, opts: opts, importID: importID, summary: Summary{Rows: len(rows)}}
		if parsed.BalanceDate != "" {
			run.fileBalance = &balance{cents: parsed.Balance, date: parsed.BalanceDate}
		}
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
	contracts  map[string]int64         // by contract id: recurring group, 0 if its group was deleted
	balances   map[int64]balance        // by account id: newest stated balance among imported days
	ownIBANs   map[string]int64         // IBAN → account id, for transfer detection
	// fixed is the account of Options.Account; fileBalance its balance as
	// stated by the file.
	fixed       *store.Account
	fileBalance *balance
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
	var fixedID int64
	if r.opts.Account != "" {
		a, err := r.tx.AccountBySlug(ctx, r.opts.Account)
		if errors.Is(err, store.ErrNotFound) {
			return fmt.Errorf("account %q does not exist; create it with: budgeteer account add --slug %s --name <name>", r.opts.Account, r.opts.Account)
		}
		if err != nil {
			return err
		}
		if err := r.checkSourceMix(ctx, a); err != nil {
			return err
		}
		r.fixed, fixedID = &a, a.ID
		if r.fileBalance != nil {
			r.balances[a.ID] = *r.fileBalance
		}
	}
	if r.parser.OwnAccountTransfers() {
		accounts, err := r.tx.ListAccounts(ctx)
		if err != nil {
			return err
		}
		r.ownIBANs = make(map[string]int64)
		for _, a := range accounts {
			if a.IBAN != "" {
				r.ownIBANs[a.IBAN] = a.ID
			}
		}
	}
	// importID is already set when an identical file is processed again.
	if r.importID == 0 {
		r.importID, err = r.tx.CreateImport(ctx, store.Import{
			Source: r.parser.Source(), FileName: fileName, SHA256: hash, RowCount: r.summary.Rows, AccountID: fixedID,
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

		if c.Pending {
			if _, err := r.storeRaw(ctx, c.Row); err != nil {
				return err
			}
			r.summary.SkippedPending++
			continue
		}

		if other, ok := r.ownIBANs[store.NormalizeIBAN(c.Tx.CounterpartyRef)]; ok && other != account.ID {
			c.Tx.IsTransfer = true
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
			if err := r.contract(ctx, txID, c); err != nil {
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
		if err := r.contract(ctx, t.ID, c); err != nil {
			return fmt.Errorf("line %d: %w", c.Row.LineNo, err)
		}
	}

	// Remember the newest balance the file states for each account. An older
	// file must not replace a newer balance.
	if r.fixed != nil {
		r.accounts[r.fixed.IBAN] = *r.fixed
	}
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

// contract puts a transaction into the recurring group of the contract the
// source assigns it to. A contract seen for the first time gets a group named
// after the counterparty. A group chosen by hand is kept, and so is the
// user's decision to delete a contract's group.
func (r *run) contract(ctx context.Context, txID int64, c Candidate) error {
	if c.ContractID == "" {
		return nil
	}
	groupID, cached := r.contracts[c.ContractID]
	if !cached {
		var known bool
		var err error
		groupID, known, err = r.tx.ContractGroup(ctx, r.parser.Source(), c.ContractID)
		if err != nil {
			return err
		}
		if !known {
			groupID, err = r.tx.CreateContractGroup(ctx, r.parser.Source(), c.ContractID, c.Tx.Counterparty, c.ContractInterval)
			if err != nil {
				return err
			}
			r.summary.GroupsCreated++
		}
		if r.contracts == nil {
			r.contracts = make(map[string]int64)
		}
		r.contracts[c.ContractID] = groupID
	}
	if groupID == 0 {
		return nil
	}
	return r.tx.AssignContractGroup(ctx, txID, groupID)
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
	if r.fixed != nil {
		return *r.fixed, nil
	}
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
		if err := checkCutover(ctx, r.tx, a, r.opts.Cutover, r.parser.Source()); err != nil {
			return store.Account{}, err
		}
		if err := r.tx.SetCutover(ctx, a.ID, r.opts.Cutover); err != nil {
			return store.Account{}, err
		}
		a.CutoverDate = r.opts.Cutover
	}
	if err := r.checkSourceMix(ctx, a); err != nil {
		return store.Account{}, err
	}
	r.accounts[iban] = a
	return a, nil
}

// checkSourceMix refuses to add a second source to an account that has no
// cut-over date: nothing would say which source owns which days, and the same
// transactions would be imported from both.
func (r *run) checkSourceMix(ctx context.Context, a store.Account) error {
	if a.CutoverDate != "" {
		return nil
	}
	other, err := r.tx.EarliestBookingDateOtherSources(ctx, a.ID, r.parser.Source())
	if err != nil {
		return err
	}
	if other != "" {
		return fmt.Errorf("account %s already has transactions from another source and no cut-over date; set one first with: budgeteer account set-cutover --slug %s --date YYYY-MM-DD",
			a.Slug, a.Slug)
	}
	return nil
}

// SetCutover sets the cut-over date of an account by hand: the history source
// (the one importing rows up to the date) keeps everything up to and
// including it, every other source everything after. It is refused if
// existing transactions would end up on the wrong side.
func SetCutover(ctx context.Context, st *store.Store, accountSlug, date, historySource string) error {
	if _, err := time.Parse(time.DateOnly, date); err != nil {
		return fmt.Errorf("invalid cut-over date %q: want YYYY-MM-DD", date)
	}
	return st.InTx(ctx, func(tx *store.Store) error {
		a, err := tx.AccountBySlug(ctx, accountSlug)
		if errors.Is(err, store.ErrNotFound) {
			return fmt.Errorf("account %q does not exist", accountSlug)
		}
		if err != nil {
			return err
		}
		if err := checkCutover(ctx, tx, a, date, historySource); err != nil {
			return err
		}
		return tx.SetCutover(ctx, a.ID, date)
	})
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
func checkCutover(ctx context.Context, tx *store.Store, a store.Account, cutover, historySource string) error {
	other, err := tx.EarliestBookingDateOtherSources(ctx, a.ID, historySource)
	if err != nil {
		return err
	}
	if other != "" && other <= cutover {
		return fmt.Errorf("cut-over %s rejected: account %s already has transactions from another source since %s; choose a date before that",
			cutover, a.Slug, other)
	}
	latest, err := tx.LatestBookingDate(ctx, a.ID, historySource)
	if err != nil {
		return err
	}
	if latest > cutover {
		return fmt.Errorf("cut-over %s rejected: account %s already has %s transactions up to %s; choose that date or a later one",
			cutover, a.Slug, historySource, latest)
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
