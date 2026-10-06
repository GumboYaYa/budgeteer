package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"regexp"
	"strings"
)

// Account is one of the user's own bank accounts. Empty IBAN, Bank and
// CutoverDate are stored as NULL.
type Account struct {
	ID          int64
	Slug        string
	Name        string
	IBAN        string
	Bank        string
	Currency    string
	CutoverDate string // YYYY-MM-DD
	// BalanceCents is the known balance at the end of BalanceDate; an empty
	// BalanceDate means no balance is known.
	BalanceCents int64
	BalanceDate  string
	// HoldsReserve marks the account the reserve for irregular expenses is
	// saved on. At most one account has it.
	HoldsReserve bool
}

var slugPattern = regexp.MustCompile(`^[a-z0-9]+(-[a-z0-9]+)*$`)

const accountColumns = "id, slug, name, iban, bank, currency, cutover_date, balance_cents, balance_date, holds_reserve"

// NormalizeIBAN removes whitespace and upper-cases, so that IBANs compare
// equal regardless of how a source formats them.
func NormalizeIBAN(iban string) string {
	return strings.ToUpper(strings.Join(strings.Fields(iban), ""))
}

// CreateAccount inserts a new account and returns it with its ID set.
func (s *Store) CreateAccount(ctx context.Context, a Account) (Account, error) {
	a.Name = strings.TrimSpace(a.Name)
	a.IBAN = NormalizeIBAN(a.IBAN)
	if !slugPattern.MatchString(a.Slug) {
		return Account{}, fmt.Errorf("invalid account slug %q: use lowercase letters, digits and single dashes", a.Slug)
	}
	if a.Name == "" {
		return Account{}, errors.New("account name must not be empty")
	}
	if a.Currency == "" {
		a.Currency = "EUR"
	}

	res, err := s.q.ExecContext(ctx,
		`INSERT INTO accounts (slug, name, iban, bank, currency, cutover_date) VALUES (?, ?, ?, ?, ?, ?)`,
		a.Slug, a.Name, nullable(a.IBAN), nullable(a.Bank), a.Currency, nullable(a.CutoverDate))
	if err != nil {
		switch {
		case strings.Contains(err.Error(), "UNIQUE constraint failed: accounts.slug"):
			return Account{}, fmt.Errorf("an account with slug %q already exists", a.Slug)
		case strings.Contains(err.Error(), "UNIQUE constraint failed: accounts.iban"):
			return Account{}, fmt.Errorf("an account with IBAN %s already exists", a.IBAN)
		}
		return Account{}, fmt.Errorf("store: create account: %w", err)
	}
	if a.ID, err = res.LastInsertId(); err != nil {
		return Account{}, fmt.Errorf("store: create account: %w", err)
	}
	return a, nil
}

// AccountBySlug returns ErrNotFound if no account has the slug.
func (s *Store) AccountBySlug(ctx context.Context, slug string) (Account, error) {
	return scanAccount(s.q.QueryRowContext(ctx,
		`SELECT `+accountColumns+` FROM accounts WHERE slug = ?`, slug))
}

// AccountByIBAN returns ErrNotFound if no account has the IBAN.
func (s *Store) AccountByIBAN(ctx context.Context, iban string) (Account, error) {
	return scanAccount(s.q.QueryRowContext(ctx,
		`SELECT `+accountColumns+` FROM accounts WHERE iban = ?`, NormalizeIBAN(iban)))
}

// ListAccounts returns all accounts ordered by slug.
func (s *Store) ListAccounts(ctx context.Context) ([]Account, error) {
	rows, err := s.q.QueryContext(ctx, `SELECT `+accountColumns+` FROM accounts ORDER BY slug`)
	if err != nil {
		return nil, fmt.Errorf("store: list accounts: %w", err)
	}
	defer rows.Close()

	var accounts []Account
	for rows.Next() {
		a, err := scanAccount(rows)
		if err != nil {
			return nil, err
		}
		accounts = append(accounts, a)
	}
	return accounts, rows.Err()
}

// SetCutover stores the cut-over date (YYYY-MM-DD) of an account; an empty
// date clears it.
func (s *Store) SetCutover(ctx context.Context, accountID int64, date string) error {
	res, err := s.q.ExecContext(ctx,
		`UPDATE accounts SET cutover_date = ? WHERE id = ?`, nullable(date), accountID)
	if err != nil {
		return fmt.Errorf("store: set cut-over: %w", err)
	}
	if n, err := res.RowsAffected(); err == nil && n == 0 {
		return ErrNotFound
	}
	return nil
}

// SetBalance records the known balance of an account at the end of a day.
func (s *Store) SetBalance(ctx context.Context, accountID, cents int64, date string) error {
	_, err := s.q.ExecContext(ctx,
		`UPDATE accounts SET balance_cents = ?, balance_date = ? WHERE id = ?`, cents, date, accountID)
	if err != nil {
		return fmt.Errorf("store: set balance: %w", err)
	}
	return nil
}

// SetReserveAccount makes the account the one that holds the reserve for
// irregular expenses; any other account loses the mark. accountID 0 means no
// account holds it.
func (s *Store) SetReserveAccount(ctx context.Context, accountID int64) error {
	return s.atomic(ctx, func(tx *Store) error {
		if _, err := tx.q.ExecContext(ctx, `UPDATE accounts SET holds_reserve = 0 WHERE holds_reserve = 1`); err != nil {
			return fmt.Errorf("store: set reserve account: %w", err)
		}
		if accountID == 0 {
			return nil
		}
		res, err := tx.q.ExecContext(ctx, `UPDATE accounts SET holds_reserve = 1 WHERE id = ?`, accountID)
		if err != nil {
			return fmt.Errorf("store: set reserve account: %w", err)
		}
		if n, err := res.RowsAffected(); err == nil && n == 0 {
			return ErrNotFound
		}
		return nil
	})
}

// AccountBalance is the balance of an account at the end of a day.
type AccountBalance struct {
	Slug  string
	Name  string
	Cents int64
}

// AccountBalances returns the balance of every account with a known balance
// at the end of the given day. It starts from the known balance and takes
// back the transactions booked after that day, or adds those booked since.
func (s *Store) AccountBalances(ctx context.Context, date string) ([]AccountBalance, error) {
	rows, err := s.q.QueryContext(ctx, `
		SELECT a.slug, a.name,
		       a.balance_cents
		       - COALESCE((SELECT sum(t.amount_cents) FROM transactions t
		                   WHERE t.account_id = a.id AND t.booking_date > ?1 AND t.booking_date <= a.balance_date), 0)
		       + COALESCE((SELECT sum(t.amount_cents) FROM transactions t
		                   WHERE t.account_id = a.id AND t.booking_date > a.balance_date AND t.booking_date <= ?1), 0)
		FROM accounts a
		WHERE a.balance_date IS NOT NULL
		ORDER BY a.name COLLATE NOCASE`, date)
	if err != nil {
		return nil, fmt.Errorf("store: account balances: %w", err)
	}
	defer rows.Close()
	var balances []AccountBalance
	for rows.Next() {
		var b AccountBalance
		if err := rows.Scan(&b.Slug, &b.Name, &b.Cents); err != nil {
			return nil, fmt.Errorf("store: account balances: %w", err)
		}
		balances = append(balances, b)
	}
	return balances, rows.Err()
}

type scanner interface {
	Scan(dest ...any) error
}

func scanAccount(row scanner) (Account, error) {
	var a Account
	var iban, bank, cutover, balanceDate sql.NullString
	var balance sql.NullInt64
	err := row.Scan(&a.ID, &a.Slug, &a.Name, &iban, &bank, &a.Currency, &cutover, &balance, &balanceDate, &a.HoldsReserve)
	if errors.Is(err, sql.ErrNoRows) {
		return Account{}, ErrNotFound
	}
	if err != nil {
		return Account{}, fmt.Errorf("store: read account: %w", err)
	}
	a.IBAN, a.Bank, a.CutoverDate = iban.String, bank.String, cutover.String
	a.BalanceCents, a.BalanceDate = balance.Int64, balanceDate.String
	return a, nil
}

// nullable maps the empty string to NULL.
func nullable(s string) any {
	if s == "" {
		return nil
	}
	return s
}
