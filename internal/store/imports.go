package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

// Import records one imported file. AccountID is 0 when the file spans
// several accounts.
type Import struct {
	Source    string
	FileName  string
	SHA256    string
	AccountID int64
	RowCount  int
}

// Transaction is one normalized bank transaction. Empty optional strings are
// stored as NULL.
type Transaction struct {
	ID              int64
	AccountID       int64
	RawRecordID     int64
	Source          string
	ExternalID      string
	DedupKey        string
	BookingDate     string // YYYY-MM-DD
	ValueDate       string
	PurchaseDate    string
	AmountCents     int64
	Currency        string
	Counterparty    string
	CounterpartyRef string
	Purpose         string
	CreditorID      string
	MandateRef      string
	EndToEndRef     string
	CustomerRef     string
	IsTransfer      bool
	// TransferManual is set once IsTransfer was changed by hand; imports then
	// leave IsTransfer alone.
	TransferManual bool
}

// ImportIDBySHA256 returns the id of the import of the file with this
// SHA-256, or 0 if the file was never imported.
func (s *Store) ImportIDBySHA256(ctx context.Context, sha256 string) (int64, error) {
	var id int64
	err := s.q.QueryRowContext(ctx, `SELECT id FROM imports WHERE file_sha256 = ?`, sha256).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	if err != nil {
		return 0, fmt.Errorf("store: look up import: %w", err)
	}
	return id, nil
}

func (s *Store) CreateImport(ctx context.Context, i Import) (int64, error) {
	var accountID any
	if i.AccountID != 0 {
		accountID = i.AccountID
	}
	res, err := s.q.ExecContext(ctx,
		`INSERT INTO imports (source, file_name, file_sha256, account_id, row_count) VALUES (?, ?, ?, ?, ?)`,
		i.Source, i.FileName, i.SHA256, accountID, i.RowCount)
	if err != nil {
		return 0, fmt.Errorf("store: create import: %w", err)
	}
	return res.LastInsertId()
}

// PutRawRecord stores one original row and returns its id; data is a JSON
// object {header: value}. A line that is already stored for this import (a
// file being processed again) keeps its id.
func (s *Store) PutRawRecord(ctx context.Context, importID int64, lineNo int, data string) (int64, error) {
	var id int64
	err := s.q.QueryRowContext(ctx, `
		INSERT INTO raw_records (import_id, line_no, data) VALUES (?, ?, ?)
		ON CONFLICT (import_id, line_no) DO UPDATE SET data = excluded.data
		RETURNING id`, importID, lineNo, data).Scan(&id)
	if err != nil {
		return 0, fmt.Errorf("store: store raw record (line %d): %w", lineNo, err)
	}
	return id, nil
}

func (s *Store) InsertTransaction(ctx context.Context, t Transaction) (int64, error) {
	currency := t.Currency
	if currency == "" {
		currency = "EUR"
	}
	res, err := s.q.ExecContext(ctx, `
		INSERT INTO transactions (
			account_id, raw_record_id, source, external_id, dedup_key,
			booking_date, value_date, purchase_date, amount_cents, currency,
			counterparty, counterparty_ref, purpose, creditor_id, mandate_ref,
			end_to_end_ref, customer_ref, is_transfer
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		t.AccountID, t.RawRecordID, t.Source, nullable(t.ExternalID), t.DedupKey,
		t.BookingDate, nullable(t.ValueDate), nullable(t.PurchaseDate), t.AmountCents, currency,
		nullable(t.Counterparty), nullable(t.CounterpartyRef), nullable(t.Purpose), nullable(t.CreditorID), nullable(t.MandateRef),
		nullable(t.EndToEndRef), nullable(t.CustomerRef), t.IsTransfer)
	if err != nil {
		return 0, fmt.Errorf("store: insert transaction %s: %w", t.DedupKey, err)
	}
	return res.LastInsertId()
}

const transactionColumns = `id, account_id, raw_record_id, source, external_id, dedup_key,
	booking_date, value_date, purchase_date, amount_cents, currency,
	counterparty, counterparty_ref, purpose, creditor_id, mandate_ref,
	end_to_end_ref, customer_ref, is_transfer, is_transfer_manual`

// TransactionByID returns ErrNotFound if the transaction does not exist.
func (s *Store) TransactionByID(ctx context.Context, id int64) (Transaction, error) {
	var t Transaction
	var externalID, valueDate, purchaseDate, counterparty, counterpartyRef, purpose,
		creditorID, mandateRef, endToEndRef, customerRef sql.NullString
	err := s.q.QueryRowContext(ctx, `SELECT `+transactionColumns+` FROM transactions WHERE id = ?`, id).Scan(
		&t.ID, &t.AccountID, &t.RawRecordID, &t.Source, &externalID, &t.DedupKey,
		&t.BookingDate, &valueDate, &purchaseDate, &t.AmountCents, &t.Currency,
		&counterparty, &counterpartyRef, &purpose, &creditorID, &mandateRef,
		&endToEndRef, &customerRef, &t.IsTransfer, &t.TransferManual)
	if errors.Is(err, sql.ErrNoRows) {
		return Transaction{}, ErrNotFound
	}
	if err != nil {
		return Transaction{}, fmt.Errorf("store: read transaction: %w", err)
	}
	t.ExternalID, t.ValueDate, t.PurchaseDate = externalID.String, valueDate.String, purchaseDate.String
	t.Counterparty, t.CounterpartyRef, t.Purpose = counterparty.String, counterpartyRef.String, purpose.String
	t.CreditorID, t.MandateRef, t.EndToEndRef, t.CustomerRef = creditorID.String, mandateRef.String, endToEndRef.String, customerRef.String
	return t, nil
}

// UpdateTransaction overwrites the fields that come from the source file. It
// keeps the transaction's allocation at the full amount. Identity (id,
// source, dedup key, raw record) and TransferManual are not changed.
func (s *Store) UpdateTransaction(ctx context.Context, t Transaction) error {
	_, err := s.q.ExecContext(ctx, `
		UPDATE transactions SET
			account_id = ?, external_id = ?, booking_date = ?, value_date = ?, purchase_date = ?,
			amount_cents = ?, currency = ?, counterparty = ?, counterparty_ref = ?, purpose = ?,
			creditor_id = ?, mandate_ref = ?, end_to_end_ref = ?, customer_ref = ?, is_transfer = ?
		WHERE id = ?`,
		t.AccountID, nullable(t.ExternalID), t.BookingDate, nullable(t.ValueDate), nullable(t.PurchaseDate),
		t.AmountCents, t.Currency, nullable(t.Counterparty), nullable(t.CounterpartyRef), nullable(t.Purpose),
		nullable(t.CreditorID), nullable(t.MandateRef), nullable(t.EndToEndRef), nullable(t.CustomerRef), t.IsTransfer,
		t.ID)
	if err != nil {
		return fmt.Errorf("store: update transaction: %w", err)
	}
	_, err = s.q.ExecContext(ctx, `
		UPDATE allocations SET amount_cents = ?, updated_at = strftime('%Y-%m-%dT%H:%M:%SZ', 'now')
		WHERE transaction_id = ? AND amount_cents <> ?`, t.AmountCents, t.ID, t.AmountCents)
	if err != nil {
		return fmt.Errorf("store: update allocation amount: %w", err)
	}
	return nil
}

// SetTransfer marks or unmarks a transaction as a transfer between own
// accounts by hand. Imports no longer change the flag afterwards.
func (s *Store) SetTransfer(ctx context.Context, id int64, transfer bool) error {
	res, err := s.q.ExecContext(ctx,
		`UPDATE transactions SET is_transfer = ?, is_transfer_manual = 1 WHERE id = ?`, transfer, id)
	if err != nil {
		return fmt.Errorf("store: set transfer: %w", err)
	}
	if n, err := res.RowsAffected(); err == nil && n == 0 {
		return ErrNotFound
	}
	return nil
}

// SetReserve marks or unmarks a transaction as an irregular expense that the
// reserve has to cover. Imports never change the mark.
func (s *Store) SetReserve(ctx context.Context, id int64, reserve bool) error {
	res, err := s.q.ExecContext(ctx, `UPDATE transactions SET is_reserve = ? WHERE id = ?`, reserve, id)
	if err != nil {
		return fmt.Errorf("store: set reserve: %w", err)
	}
	if n, err := res.RowsAffected(); err == nil && n == 0 {
		return ErrNotFound
	}
	return nil
}

// DeleteTransaction removes a transaction with its allocations and tags. Its
// raw record is kept.
func (s *Store) DeleteTransaction(ctx context.Context, id int64) error {
	if _, err := s.q.ExecContext(ctx, `DELETE FROM transactions WHERE id = ?`, id); err != nil {
		return fmt.Errorf("store: delete transaction: %w", err)
	}
	return nil
}

// TransactionIDsByDedupKey returns dedup_key → transaction id for all
// transactions of one source.
func (s *Store) TransactionIDsByDedupKey(ctx context.Context, source string) (map[string]int64, error) {
	rows, err := s.q.QueryContext(ctx, `SELECT dedup_key, id FROM transactions WHERE source = ?`, source)
	if err != nil {
		return nil, fmt.Errorf("store: load dedup keys: %w", err)
	}
	defer rows.Close()

	ids := make(map[string]int64)
	for rows.Next() {
		var key string
		var id int64
		if err := rows.Scan(&key, &id); err != nil {
			return nil, fmt.Errorf("store: load dedup keys: %w", err)
		}
		ids[key] = id
	}
	return ids, rows.Err()
}

// LatestBookingDate returns the newest booking date the account has from the
// given source, or "" if there is none.
func (s *Store) LatestBookingDate(ctx context.Context, accountID int64, source string) (string, error) {
	return s.bookingDate(ctx, `SELECT max(booking_date) FROM transactions WHERE account_id = ? AND source = ?`, accountID, source)
}

// EarliestBookingDateOtherSources returns the oldest booking date the account
// has from any source except the given one, or "" if there is none.
func (s *Store) EarliestBookingDateOtherSources(ctx context.Context, accountID int64, source string) (string, error) {
	return s.bookingDate(ctx, `SELECT min(booking_date) FROM transactions WHERE account_id = ? AND source <> ?`, accountID, source)
}

func (s *Store) bookingDate(ctx context.Context, query string, args ...any) (string, error) {
	var date *string
	if err := s.q.QueryRowContext(ctx, query, args...).Scan(&date); err != nil {
		return "", fmt.Errorf("store: read booking date: %w", err)
	}
	if date == nil {
		return "", nil
	}
	return *date, nil
}
