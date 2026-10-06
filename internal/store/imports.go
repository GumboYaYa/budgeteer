package store

import (
	"context"
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
}

// ImportExists reports whether a file with this SHA-256 was imported before.
func (s *Store) ImportExists(ctx context.Context, sha256 string) (bool, error) {
	var n int
	err := s.q.QueryRowContext(ctx, `SELECT count(*) FROM imports WHERE file_sha256 = ?`, sha256).Scan(&n)
	if err != nil {
		return false, fmt.Errorf("store: look up import: %w", err)
	}
	return n > 0, nil
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

// InsertRawRecord stores one original row; data is a JSON object
// {header: value}.
func (s *Store) InsertRawRecord(ctx context.Context, importID int64, lineNo int, data string) (int64, error) {
	res, err := s.q.ExecContext(ctx,
		`INSERT INTO raw_records (import_id, line_no, data) VALUES (?, ?, ?)`, importID, lineNo, data)
	if err != nil {
		return 0, fmt.Errorf("store: insert raw record (line %d): %w", lineNo, err)
	}
	return res.LastInsertId()
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
