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
	"sort"
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

	// Category as named by the source; both empty means uncategorized.
	MainCategory       string
	SubCategory        string
	ExcludedFromIncome bool
	Tags               []string
}

type Options struct {
	// RawDir receives a copy of the original file. Empty disables the copy.
	RawDir string
	// Cutover (YYYY-MM-DD), if set, becomes the cut-over date of every account
	// in the file before the rows are filtered.
	Cutover string
}

// Summary reports what an import did.
type Summary struct {
	AlreadyImported   bool // the identical file was imported before; nothing changed
	Rows              int  // data rows in the file
	New               int  // transactions inserted
	Duplicates        int  // rows whose transaction already existed
	Updated           int  // existing transactions whose category was refreshed
	SkippedCutover    int  // rows on the other side of the cut-over date
	AccountsCreated   []string
	CategoriesCreated int
	Warnings          []string
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

	exists, err := st.ImportExists(ctx, hash)
	if err != nil {
		return Summary{}, err
	}
	if exists {
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
		run := &run{tx: tx, parser: p, opts: opts, summary: Summary{Rows: len(rows)}}
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
	excluded   map[int64]*tally         // by category id
	names      map[int64]string         // category id → slug, for warnings
}

// tally counts how many rows of a category are excluded from income.
type tally struct{ excluded, total int }

func (r *run) importAll(ctx context.Context, fileName, hash string, candidates []Candidate) error {
	r.accounts = make(map[string]store.Account)
	r.categories = make(map[string]int64)
	r.excluded = make(map[int64]*tally)
	r.names = make(map[int64]string)

	var err error
	r.importID, err = r.tx.CreateImport(ctx, store.Import{
		Source: r.parser.Source(), FileName: fileName, SHA256: hash, RowCount: r.summary.Rows,
	})
	if err != nil {
		return err
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

		categoryID, err := r.category(ctx, c)
		if err != nil {
			return fmt.Errorf("line %d: %w", c.Row.LineNo, err)
		}

		if txID, ok := known[c.Tx.DedupKey]; ok {
			r.summary.Duplicates++
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

	return r.applyExcludedFromIncome(ctx)
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
	return r.tx.InsertRawRecord(ctx, r.importID, row.LineNo, string(bytes.TrimSpace(buf.Bytes())))
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
// creating the main and sub category as needed, and records the row's
// excluded-from-income flag for the majority vote.
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
	r.vote(id, c.ExcludedFromIncome)

	if sub != "" {
		subSlug := slug.Make(sub)
		if subSlug == "" {
			return 0, fmt.Errorf("category name %q has no letters or digits", sub)
		}
		if id, err = r.ensureCategory(ctx, mainSlug+"/"+subSlug, sub, id); err != nil {
			return 0, err
		}
		r.vote(id, c.ExcludedFromIncome)
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
	r.names[id] = slug
	return id, nil
}

func (r *run) vote(categoryID int64, excluded bool) {
	t := r.excluded[categoryID]
	if t == nil {
		t = &tally{}
		r.excluded[categoryID] = t
	}
	t.total++
	if excluded {
		t.excluded++
	}
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

// applyExcludedFromIncome sets each category's flag by majority of its rows
// in this file. The source has the flag per row, the schema per category;
// categories whose rows disagree are reported.
func (r *run) applyExcludedFromIncome(ctx context.Context) error {
	ids := make([]int64, 0, len(r.excluded))
	for id := range r.excluded {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return r.names[ids[i]] < r.names[ids[j]] })

	for _, id := range ids {
		t := r.excluded[id]
		excluded := t.excluded*2 > t.total
		if err := r.tx.SetCategoryExcludedFromIncome(ctx, id, excluded); err != nil {
			return err
		}
		if t.excluded != 0 && t.excluded != t.total {
			state := "counted as income/spending"
			if excluded {
				state = "excluded from income"
			}
			r.summary.Warnings = append(r.summary.Warnings, fmt.Sprintf(
				"category %s: %d of %d rows are marked as excluded from income; category is %s",
				r.names[id], t.excluded, t.total, state))
		}
	}
	return nil
}
