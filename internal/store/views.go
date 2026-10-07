package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
)

// TxView is a transaction as shown in lists: joined with its account and its
// confirmed category.
type TxView struct {
	ID           int64
	BookingDate  string
	PurchaseDate string
	AccountSlug  string
	AccountName  string
	Counterparty string
	Purpose      string
	AmountCents  int64
	IsTransfer   bool
	IsReserve    bool // marked by hand as irregular expense for the reserve
	// The recurring group, if any. RecurringActive is false for a group that
	// has ended. ReserveViaGroup is set when the group makes the transaction
	// count for the reserve, which only an active group does.
	RecurringID     int64
	RecurringName   string
	RecurringActive bool
	ReserveViaGroup bool
	CategoryID      int64  // 0 when uncategorized
	CategoryName    string // "Main / Sub"
	CategorySource  string
}

// Status values for TxFilter.
const (
	StatusUncategorized = "uncategorized"
	StatusTransfer      = "transfer"
	StatusReserve       = "reserve"
)

// TxFilter.Recurring values besides a group id: in any group, in a group
// that is active, in one that has ended.
const (
	AnyRecurring      = -1
	ActiveRecurring   = -2
	InactiveRecurring = -3
)

// TxFilter selects transactions. Zero values mean "no restriction".
type TxFilter struct {
	AccountSlug string
	From, To    string // booking date range, inclusive, YYYY-MM-DD
	CategoryID  int64  // the category itself or any of its sub categories
	Text        string // substring of counterparty or purpose
	Status      string
	// Recurring selects one recurring group by id, or one of AnyRecurring,
	// ActiveRecurring and InactiveRecurring.
	Recurring int64
	Limit     int
	Offset    int
}

// TxPage is one page of a filtered list with totals over the whole filter.
type TxPage struct {
	Rows     []TxView
	Total    int
	SumCents int64
}

const txViewFrom = `
	FROM transactions t
	JOIN accounts ac ON ac.id = t.account_id
	LEFT JOIN allocations al ON al.transaction_id = t.id AND al.source <> 'suggested'
	LEFT JOIN categories c ON c.id = al.category_id
	LEFT JOIN categories p ON p.id = c.parent_id
	LEFT JOIN recurring_groups rg ON rg.id = t.recurring_group_id`

const txViewColumns = `
	t.id, t.booking_date, COALESCE(t.purchase_date, ''), ac.slug, ac.name,
	COALESCE(t.counterparty, ''), COALESCE(t.purpose, ''), t.amount_cents, t.is_transfer, t.is_reserve,
	COALESCE(c.id, 0),
	CASE WHEN p.id IS NOT NULL THEN p.name || ' / ' || c.name ELSE COALESCE(c.name, '') END,
	COALESCE(al.source, ''),
	COALESCE(rg.id, 0), COALESCE(rg.name, ''), COALESCE(rg.active, 0),
	COALESCE(rg.covers_reserve = 1 AND rg.active = 1, 0)`

func (f TxFilter) where() (string, []any) {
	var conds []string
	var args []any
	add := func(cond string, a ...any) {
		conds = append(conds, cond)
		args = append(args, a...)
	}
	if f.AccountSlug != "" {
		add("ac.slug = ?", f.AccountSlug)
	}
	if f.From != "" {
		add("t.booking_date >= ?", f.From)
	}
	if f.To != "" {
		add("t.booking_date <= ?", f.To)
	}
	if f.CategoryID != 0 {
		add("(c.id = ? OR c.parent_id = ?)", f.CategoryID, f.CategoryID)
	}
	if text := strings.TrimSpace(f.Text); text != "" {
		pattern := "%" + strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(text) + "%"
		add(`(t.counterparty LIKE ? ESCAPE '\' OR t.purpose LIKE ? ESCAPE '\')`, pattern, pattern)
	}
	switch f.Status {
	case StatusUncategorized:
		add("t.id IN (SELECT id FROM uncategorized)")
	case StatusTransfer:
		add("t.is_transfer = 1")
	case StatusReserve:
		add("(t.is_reserve = 1 OR (rg.covers_reserve = 1 AND rg.active = 1))")
	}
	switch {
	case f.Recurring == AnyRecurring:
		add("t.recurring_group_id IS NOT NULL")
	case f.Recurring == ActiveRecurring:
		add("rg.active = 1")
	case f.Recurring == InactiveRecurring:
		add("rg.active = 0")
	case f.Recurring > 0:
		add("t.recurring_group_id = ?", f.Recurring)
	}
	if len(conds) == 0 {
		return "", args
	}
	return " WHERE " + strings.Join(conds, " AND "), args
}

// ListTransactions returns the newest transactions matching the filter.
func (s *Store) ListTransactions(ctx context.Context, f TxFilter) (TxPage, error) {
	where, args := f.where()
	var page TxPage
	err := s.q.QueryRowContext(ctx,
		`SELECT count(*), COALESCE(sum(t.amount_cents), 0)`+txViewFrom+where, args...).Scan(&page.Total, &page.SumCents)
	if err != nil {
		return TxPage{}, fmt.Errorf("store: count transactions: %w", err)
	}

	limit := f.Limit
	if limit <= 0 {
		limit = 100
	}
	rows, err := s.q.QueryContext(ctx,
		`SELECT`+txViewColumns+txViewFrom+where+` ORDER BY t.booking_date DESC, t.id DESC LIMIT ? OFFSET ?`,
		append(args, limit, f.Offset)...)
	if err != nil {
		return TxPage{}, fmt.Errorf("store: list transactions: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		v, err := scanTxView(rows)
		if err != nil {
			return TxPage{}, err
		}
		page.Rows = append(page.Rows, v)
	}
	return page, rows.Err()
}

// TxViewByID returns ErrNotFound if the transaction does not exist.
func (s *Store) TxViewByID(ctx context.Context, id int64) (TxView, error) {
	v, err := scanTxView(s.q.QueryRowContext(ctx, `SELECT`+txViewColumns+txViewFrom+` WHERE t.id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return TxView{}, ErrNotFound
	}
	return v, err
}

func scanTxView(row scanner) (TxView, error) {
	var v TxView
	err := row.Scan(&v.ID, &v.BookingDate, &v.PurchaseDate, &v.AccountSlug, &v.AccountName,
		&v.Counterparty, &v.Purpose, &v.AmountCents, &v.IsTransfer, &v.IsReserve,
		&v.CategoryID, &v.CategoryName, &v.CategorySource,
		&v.RecurringID, &v.RecurringName, &v.RecurringActive, &v.ReserveViaGroup)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return TxView{}, err
		}
		return TxView{}, fmt.Errorf("store: read transaction: %w", err)
	}
	return v, nil
}

// CountUncategorized returns the size of the inbox.
func (s *Store) CountUncategorized(ctx context.Context) (int, error) {
	var n int
	if err := s.q.QueryRowContext(ctx, `SELECT count(*) FROM uncategorized`).Scan(&n); err != nil {
		return 0, fmt.Errorf("store: count uncategorized: %w", err)
	}
	return n, nil
}

// SetCategory categorizes a transaction by hand: its allocations are replaced
// by one manual allocation for the full amount.
func (s *Store) SetCategory(ctx context.Context, transactionID, categoryID int64) error {
	return s.atomic(ctx, func(tx *Store) error {
		var amount int64
		err := tx.q.QueryRowContext(ctx, `SELECT amount_cents FROM transactions WHERE id = ?`, transactionID).Scan(&amount)
		if errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return fmt.Errorf("store: set category: %w", err)
		}
		var exists int
		if err := tx.q.QueryRowContext(ctx, `SELECT count(*) FROM categories WHERE id = ?`, categoryID).Scan(&exists); err != nil {
			return fmt.Errorf("store: set category: %w", err)
		}
		if exists == 0 {
			return fmt.Errorf("category %d: %w", categoryID, ErrNotFound)
		}
		if _, err := tx.q.ExecContext(ctx, `DELETE FROM allocations WHERE transaction_id = ?`, transactionID); err != nil {
			return fmt.Errorf("store: set category: %w", err)
		}
		return tx.InsertAllocation(ctx, Allocation{
			TransactionID: transactionID, CategoryID: categoryID, AmountCents: amount, Source: SourceManual,
		})
	})
}

// ClearCategory removes a transaction's category, sending it back to the
// inbox.
func (s *Store) ClearCategory(ctx context.Context, transactionID int64) error {
	if _, err := s.q.ExecContext(ctx, `DELETE FROM allocations WHERE transaction_id = ?`, transactionID); err != nil {
		return fmt.Errorf("store: clear category: %w", err)
	}
	return nil
}

// MonthTotal is income and spending of one month, transfers excluded.
// SpendingCents is negative.
type MonthTotal struct {
	Month         string // YYYY-MM
	IncomeCents   int64
	SpendingCents int64
}

// MonthlyTotals returns one entry per month that has transactions, between
// the two months inclusive, oldest first.
func (s *Store) MonthlyTotals(ctx context.Context, fromMonth, toMonth string) ([]MonthTotal, error) {
	rows, err := s.q.QueryContext(ctx, `
		SELECT substr(booking_date, 1, 7) AS month,
		       COALESCE(sum(CASE WHEN amount_cents > 0 THEN amount_cents END), 0),
		       COALESCE(sum(CASE WHEN amount_cents < 0 THEN amount_cents END), 0)
		FROM transactions
		WHERE is_transfer = 0 AND substr(booking_date, 1, 7) BETWEEN ? AND ?
		GROUP BY month ORDER BY month`, fromMonth, toMonth)
	if err != nil {
		return nil, fmt.Errorf("store: monthly totals: %w", err)
	}
	defer rows.Close()
	var totals []MonthTotal
	for rows.Next() {
		var m MonthTotal
		if err := rows.Scan(&m.Month, &m.IncomeCents, &m.SpendingCents); err != nil {
			return nil, fmt.Errorf("store: monthly totals: %w", err)
		}
		totals = append(totals, m)
	}
	return totals, rows.Err()
}

// CategoryTotal is the net amount of one main category in a period.
// CategoryID 0 stands for transactions without a category.
type CategoryTotal struct {
	CategoryID int64
	Name       string
	Cents      int64
	Count      int
}

// MainCategoryTotals sums the transactions of a booking date range per main
// category, transfers excluded, most negative first.
func (s *Store) MainCategoryTotals(ctx context.Context, from, to string) ([]CategoryTotal, error) {
	rows, err := s.q.QueryContext(ctx, `
		SELECT COALESCE(p.id, c.id, 0), COALESCE(p.name, c.name, ''), sum(t.amount_cents), count(*)
		FROM transactions t
		LEFT JOIN allocations al ON al.transaction_id = t.id AND al.source <> 'suggested'
		LEFT JOIN categories c ON c.id = al.category_id
		LEFT JOIN categories p ON p.id = c.parent_id
		WHERE t.is_transfer = 0 AND t.booking_date BETWEEN ? AND ?
		GROUP BY 1 ORDER BY 3, 2`, from, to)
	if err != nil {
		return nil, fmt.Errorf("store: category totals: %w", err)
	}
	defer rows.Close()
	var totals []CategoryTotal
	for rows.Next() {
		var c CategoryTotal
		if err := rows.Scan(&c.CategoryID, &c.Name, &c.Cents, &c.Count); err != nil {
			return nil, fmt.Errorf("store: category totals: %w", err)
		}
		totals = append(totals, c)
	}
	return totals, rows.Err()
}

// EarliestDate returns the booking date of the oldest transaction, or "" if
// there are none.
func (s *Store) EarliestDate(ctx context.Context) (string, error) {
	return s.bookingDate(ctx, `SELECT min(booking_date) FROM transactions`)
}

// LatestDate returns the booking date of the newest transaction, or "" if
// there are none.
func (s *Store) LatestDate(ctx context.Context) (string, error) {
	return s.bookingDate(ctx, `SELECT max(booking_date) FROM transactions`)
}

// ReserveTransactions returns the transactions that count as irregular
// expenses, marked by hand or through their recurring group, with a booking
// date after from and up to to (YYYY-MM-DD), oldest first.
func (s *Store) ReserveTransactions(ctx context.Context, from, to string) ([]TxView, error) {
	rows, err := s.q.QueryContext(ctx,
		`SELECT`+txViewColumns+txViewFrom+`
		WHERE (t.is_reserve = 1 OR (rg.covers_reserve = 1 AND rg.active = 1)) AND t.booking_date > ? AND t.booking_date <= ?
		ORDER BY t.booking_date, t.id`, from, to)
	if err != nil {
		return nil, fmt.Errorf("store: reserve transactions: %w", err)
	}
	defer rows.Close()
	var out []TxView
	for rows.Next() {
		v, err := scanTxView(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}
