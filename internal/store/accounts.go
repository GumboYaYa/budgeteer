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
}

var slugPattern = regexp.MustCompile(`^[a-z0-9]+(-[a-z0-9]+)*$`)

const accountColumns = "id, slug, name, iban, bank, currency, cutover_date"

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

	res, err := s.DB.ExecContext(ctx,
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
	return scanAccount(s.DB.QueryRowContext(ctx,
		`SELECT `+accountColumns+` FROM accounts WHERE slug = ?`, slug))
}

// AccountByIBAN returns ErrNotFound if no account has the IBAN.
func (s *Store) AccountByIBAN(ctx context.Context, iban string) (Account, error) {
	return scanAccount(s.DB.QueryRowContext(ctx,
		`SELECT `+accountColumns+` FROM accounts WHERE iban = ?`, NormalizeIBAN(iban)))
}

// ListAccounts returns all accounts ordered by slug.
func (s *Store) ListAccounts(ctx context.Context) ([]Account, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT `+accountColumns+` FROM accounts ORDER BY slug`)
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
	res, err := s.DB.ExecContext(ctx,
		`UPDATE accounts SET cutover_date = ? WHERE id = ?`, nullable(date), accountID)
	if err != nil {
		return fmt.Errorf("store: set cut-over: %w", err)
	}
	if n, err := res.RowsAffected(); err == nil && n == 0 {
		return ErrNotFound
	}
	return nil
}

type scanner interface {
	Scan(dest ...any) error
}

func scanAccount(row scanner) (Account, error) {
	var a Account
	var iban, bank, cutover sql.NullString
	err := row.Scan(&a.ID, &a.Slug, &a.Name, &iban, &bank, &a.Currency, &cutover)
	if errors.Is(err, sql.ErrNoRows) {
		return Account{}, ErrNotFound
	}
	if err != nil {
		return Account{}, fmt.Errorf("store: read account: %w", err)
	}
	a.IBAN, a.Bank, a.CutoverDate = iban.String, bank.String, cutover.String
	return a, nil
}

// nullable maps the empty string to NULL.
func nullable(s string) any {
	if s == "" {
		return nil
	}
	return s
}
