package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

// Allocation sources. Only non-suggested allocations count as confirmed.
const (
	SourceFinanzguru = "finanzguru"
	SourceManual     = "manual"
)

// Allocation assigns a transaction to a category. There is exactly one per
// categorized transaction, for the full amount.
type Allocation struct {
	ID            int64
	TransactionID int64
	CategoryID    int64
	AmountCents   int64
	Source        string
}

// EnsureCategory returns the id of the category with the given slug, creating
// it if needed. parentID 0 means a main category. An existing category keeps
// its name and parent.
func (s *Store) EnsureCategory(ctx context.Context, slug, name string, parentID int64) (id int64, created bool, err error) {
	err = s.q.QueryRowContext(ctx, `SELECT id FROM categories WHERE slug = ?`, slug).Scan(&id)
	if err == nil {
		return id, false, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return 0, false, fmt.Errorf("store: look up category %s: %w", slug, err)
	}

	var parent any
	if parentID != 0 {
		parent = parentID
	}
	res, err := s.q.ExecContext(ctx,
		`INSERT INTO categories (parent_id, name, slug) VALUES (?, ?, ?)`, parent, name, slug)
	if err != nil {
		return 0, false, fmt.Errorf("store: create category %s: %w", slug, err)
	}
	id, err = res.LastInsertId()
	return id, true, err
}

// Allocations returns the allocations of one transaction.
func (s *Store) Allocations(ctx context.Context, transactionID int64) ([]Allocation, error) {
	rows, err := s.q.QueryContext(ctx,
		`SELECT id, transaction_id, category_id, amount_cents, source FROM allocations WHERE transaction_id = ? ORDER BY id`,
		transactionID)
	if err != nil {
		return nil, fmt.Errorf("store: load allocations: %w", err)
	}
	defer rows.Close()

	var allocations []Allocation
	for rows.Next() {
		var a Allocation
		if err := rows.Scan(&a.ID, &a.TransactionID, &a.CategoryID, &a.AmountCents, &a.Source); err != nil {
			return nil, fmt.Errorf("store: load allocations: %w", err)
		}
		allocations = append(allocations, a)
	}
	return allocations, rows.Err()
}

func (s *Store) InsertAllocation(ctx context.Context, a Allocation) error {
	_, err := s.q.ExecContext(ctx,
		`INSERT INTO allocations (transaction_id, category_id, amount_cents, source) VALUES (?, ?, ?, ?)`,
		a.TransactionID, a.CategoryID, a.AmountCents, a.Source)
	if err != nil {
		return fmt.Errorf("store: insert allocation: %w", err)
	}
	return nil
}

// SetAllocationCategory moves an existing allocation to another category.
func (s *Store) SetAllocationCategory(ctx context.Context, allocationID, categoryID int64) error {
	_, err := s.q.ExecContext(ctx, `
		UPDATE allocations
		SET category_id = ?, updated_at = strftime('%Y-%m-%dT%H:%M:%SZ', 'now')
		WHERE id = ?`, categoryID, allocationID)
	if err != nil {
		return fmt.Errorf("store: update allocation: %w", err)
	}
	return nil
}

// TagTransaction attaches the tag to the transaction, creating the tag if
// needed. Attaching twice is a no-op.
func (s *Store) TagTransaction(ctx context.Context, transactionID int64, tag string) error {
	if _, err := s.q.ExecContext(ctx, `INSERT INTO tags (name) VALUES (?) ON CONFLICT (name) DO NOTHING`, tag); err != nil {
		return fmt.Errorf("store: create tag %s: %w", tag, err)
	}
	_, err := s.q.ExecContext(ctx, `
		INSERT INTO transaction_tags (transaction_id, tag_id)
		SELECT ?, id FROM tags WHERE name = ?
		ON CONFLICT DO NOTHING`, transactionID, tag)
	if err != nil {
		return fmt.Errorf("store: tag transaction: %w", err)
	}
	return nil
}
