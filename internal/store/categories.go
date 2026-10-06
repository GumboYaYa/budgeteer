package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/GumboYaYa/budgeteer/internal/slug"
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

// Category is a node of the two-level category tree. ParentID 0 means a main
// category.
type Category struct {
	ID         int64
	ParentID   int64
	Name       string
	Slug       string
	ParentName string
	Uses       int // transactions allocated to it
}

// FullName is "Main / Sub" for a sub category and "Main" for a main one.
func (c Category) FullName() string {
	if c.ParentID != 0 {
		return c.ParentName + " / " + c.Name
	}
	return c.Name
}

const categorySelect = `
	SELECT c.id, COALESCE(c.parent_id, 0), c.name, c.slug, COALESCE(p.name, ''),
	       (SELECT count(*) FROM allocations a WHERE a.category_id = c.id)
	FROM categories c LEFT JOIN categories p ON p.id = c.parent_id`

// ListCategories returns the tree flattened: every main category followed by
// its sub categories, each level sorted by name.
func (s *Store) ListCategories(ctx context.Context) ([]Category, error) {
	rows, err := s.q.QueryContext(ctx, categorySelect+`
		ORDER BY COALESCE(p.name, c.name) COLLATE NOCASE, COALESCE(p.id, c.id), c.parent_id IS NOT NULL, c.name COLLATE NOCASE`)
	if err != nil {
		return nil, fmt.Errorf("store: list categories: %w", err)
	}
	defer rows.Close()
	var categories []Category
	for rows.Next() {
		c, err := scanCategory(rows)
		if err != nil {
			return nil, err
		}
		categories = append(categories, c)
	}
	return categories, rows.Err()
}

// CategoryByID returns ErrNotFound if the category does not exist.
func (s *Store) CategoryByID(ctx context.Context, id int64) (Category, error) {
	c, err := scanCategory(s.q.QueryRowContext(ctx, categorySelect+` WHERE c.id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return Category{}, ErrNotFound
	}
	return c, err
}

func scanCategory(row scanner) (Category, error) {
	var c Category
	err := row.Scan(&c.ID, &c.ParentID, &c.Name, &c.Slug, &c.ParentName, &c.Uses)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return Category{}, fmt.Errorf("store: read category: %w", err)
	}
	return c, err
}

// RecentCategoryIDs returns the categories most recently assigned by hand,
// newest first.
func (s *Store) RecentCategoryIDs(ctx context.Context, limit int) ([]int64, error) {
	rows, err := s.q.QueryContext(ctx, `
		SELECT category_id FROM allocations WHERE source = 'manual'
		GROUP BY category_id ORDER BY max(updated_at) DESC, max(id) DESC LIMIT ?`, limit)
	if err != nil {
		return nil, fmt.Errorf("store: recent categories: %w", err)
	}
	defer rows.Close()
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("store: recent categories: %w", err)
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// CreateCategory adds a category under parentID, or a main category when
// parentID is 0.
func (s *Store) CreateCategory(ctx context.Context, name string, parentID int64) (Category, error) {
	var created Category
	err := s.atomic(ctx, func(tx *Store) error {
		name = strings.TrimSpace(name)
		if name == "" {
			return errors.New("category name must not be empty")
		}
		newSlug, err := tx.categorySlug(ctx, slug.Make(name), parentID)
		if err != nil {
			return err
		}
		id, wasCreated, err := tx.EnsureCategory(ctx, newSlug, name, parentID)
		if err != nil {
			return err
		}
		if !wasCreated {
			return fmt.Errorf("a category with key %q already exists", newSlug)
		}
		created, err = tx.CategoryByID(ctx, id)
		return err
	})
	return created, err
}

// categorySlug builds the slug of a category with the given last segment
// under parentID, checking that the parent is a main category.
func (s *Store) categorySlug(ctx context.Context, leaf string, parentID int64) (string, error) {
	if leaf == "" {
		return "", errors.New("category name needs at least one letter or digit")
	}
	if parentID == 0 {
		return leaf, nil
	}
	parent, err := s.CategoryByID(ctx, parentID)
	if errors.Is(err, ErrNotFound) {
		return "", errors.New("parent category does not exist")
	}
	if err != nil {
		return "", err
	}
	if parent.ParentID != 0 {
		return "", fmt.Errorf("%q is a sub category and cannot have sub categories itself", parent.FullName())
	}
	return parent.Slug + "/" + leaf, nil
}

// RenameCategory changes the display name. The slug stays, because it is the
// stable key of the category.
func (s *Store) RenameCategory(ctx context.Context, id int64, name string) error {
	name = strings.TrimSpace(name)
	if name == "" {
		return errors.New("category name must not be empty")
	}
	res, err := s.q.ExecContext(ctx, `UPDATE categories SET name = ? WHERE id = ?`, name, id)
	if err != nil {
		return fmt.Errorf("store: rename category: %w", err)
	}
	if n, err := res.RowsAffected(); err == nil && n == 0 {
		return ErrNotFound
	}
	return nil
}

// MoveCategory puts a category under another main category, or makes it a
// main category when parentID is 0. Its slug changes accordingly. A category
// that has sub categories cannot be moved under another one.
func (s *Store) MoveCategory(ctx context.Context, id, parentID int64) error {
	return s.atomic(ctx, func(tx *Store) error {
		c, err := tx.CategoryByID(ctx, id)
		if err != nil {
			return err
		}
		if c.ParentID == parentID {
			return nil
		}
		if parentID == id {
			return errors.New("a category cannot be moved under itself")
		}
		if parentID != 0 {
			if n, err := tx.countChildren(ctx, id); err != nil {
				return err
			} else if n > 0 {
				return fmt.Errorf("%q has sub categories; move or merge them first", c.Name)
			}
		}
		leaf := c.Slug[strings.LastIndex(c.Slug, "/")+1:]
		newSlug, err := tx.categorySlug(ctx, leaf, parentID)
		if err != nil {
			return err
		}
		var parent any
		if parentID != 0 {
			parent = parentID
		}
		_, err = tx.q.ExecContext(ctx, `UPDATE categories SET parent_id = ?, slug = ? WHERE id = ?`, parent, newSlug, id)
		if err != nil {
			if strings.Contains(err.Error(), "UNIQUE constraint failed") {
				return fmt.Errorf("a category with key %q already exists; merge the two instead", newSlug)
			}
			return fmt.Errorf("store: move category: %w", err)
		}
		return nil
	})
}

// MergeCategory moves all transactions of one category to another and
// deletes the first.
func (s *Store) MergeCategory(ctx context.Context, fromID, intoID int64) error {
	return s.atomic(ctx, func(tx *Store) error {
		if fromID == intoID {
			return errors.New("a category cannot be merged into itself")
		}
		from, err := tx.CategoryByID(ctx, fromID)
		if err != nil {
			return err
		}
		if _, err := tx.CategoryByID(ctx, intoID); err != nil {
			return err
		}
		if n, err := tx.countChildren(ctx, fromID); err != nil {
			return err
		} else if n > 0 {
			return fmt.Errorf("%q has sub categories; move or merge them first", from.Name)
		}
		if _, err := tx.q.ExecContext(ctx, `UPDATE allocations SET category_id = ? WHERE category_id = ?`, intoID, fromID); err != nil {
			return fmt.Errorf("store: merge category: %w", err)
		}
		if _, err := tx.q.ExecContext(ctx, `DELETE FROM categories WHERE id = ?`, fromID); err != nil {
			return fmt.Errorf("store: merge category: %w", err)
		}
		return nil
	})
}

func (s *Store) countChildren(ctx context.Context, id int64) (int, error) {
	var n int
	if err := s.q.QueryRowContext(ctx, `SELECT count(*) FROM categories WHERE parent_id = ?`, id).Scan(&n); err != nil {
		return 0, fmt.Errorf("store: count sub categories: %w", err)
	}
	return n, nil
}
