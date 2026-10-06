package store

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
)

// Kind is the value type of an exported column.
type Kind int

const (
	KindText  Kind = iota
	KindInt        // int64
	KindFloat      // float64
	KindCents      // int64 amount in cents, to be rendered as a decimal
)

type Column struct {
	Name string
	Kind Kind
}

// Dataset is a table ready for export. Row values are int64, float64, string
// or nil, matching the column kinds.
type Dataset struct {
	Name    string
	Columns []Column
	Rows    [][]any
}

// exportTables are dumped as they are, in this order.
var exportTables = []string{
	"accounts", "categories", "transactions", "allocations",
	"tags", "transaction_tags", "imports", "raw_records",
}

// transactionsFlat is the dataset meant for spreadsheets and notebooks: one
// row per transaction with everything joined in.
const transactionsFlat = `
	SELECT t.id, ac.slug AS account, t.booking_date, t.value_date, t.purchase_date,
	       t.amount_cents AS amount, t.amount_cents, t.currency,
	       t.counterparty, t.counterparty_ref, t.purpose,
	       COALESCE(p.name, c.name) AS main_category,
	       CASE WHEN p.id IS NOT NULL THEN c.name END AS sub_category,
	       c.slug AS category_slug, al.source AS category_source,
	       t.is_transfer, t.is_reserve,
	       (SELECT group_concat(name, ';') FROM (
	            SELECT g.name FROM transaction_tags tt JOIN tags g ON g.id = tt.tag_id
	            WHERE tt.transaction_id = t.id ORDER BY g.name)) AS tags,
	       t.creditor_id, t.mandate_ref, t.end_to_end_ref, t.customer_ref,
	       t.source, t.external_id
	FROM transactions t
	JOIN accounts ac ON ac.id = t.account_id
	LEFT JOIN allocations al ON al.transaction_id = t.id AND al.source <> 'suggested'
	LEFT JOIN categories c ON c.id = al.category_id
	LEFT JOIN categories p ON p.id = c.parent_id
	ORDER BY t.booking_date, t.id`

var transactionsFlatKinds = map[string]Kind{
	"id": KindInt, "amount": KindCents, "amount_cents": KindInt, "is_transfer": KindInt, "is_reserve": KindInt,
}

// ExportDatasets reads every table plus the flat transaction list, all from
// one consistent snapshot.
func (s *Store) ExportDatasets(ctx context.Context) ([]Dataset, error) {
	var datasets []Dataset
	err := s.atomic(ctx, func(tx *Store) error {
		flat, err := tx.dataset(ctx, "transactions_flat", transactionsFlat, transactionsFlatKinds)
		if err != nil {
			return err
		}
		datasets = append(datasets, flat)
		for _, table := range exportTables {
			d, err := tx.dataset(ctx, table, `SELECT * FROM `+table+` ORDER BY 1, 2`, nil)
			if err != nil {
				return err
			}
			datasets = append(datasets, d)
		}
		return nil
	})
	return datasets, err
}

// dataset runs a query. A column's kind comes from kinds if listed there,
// otherwise from its declared SQL type (text when there is none).
func (s *Store) dataset(ctx context.Context, name, query string, kinds map[string]Kind) (Dataset, error) {
	rows, err := s.q.QueryContext(ctx, query)
	if err != nil {
		return Dataset{}, fmt.Errorf("store: export %s: %w", name, err)
	}
	defer rows.Close()

	types, err := rows.ColumnTypes()
	if err != nil {
		return Dataset{}, fmt.Errorf("store: export %s: %w", name, err)
	}
	d := Dataset{Name: name}
	for _, t := range types {
		kind, listed := kinds[t.Name()]
		if !listed {
			switch strings.ToUpper(t.DatabaseTypeName()) {
			case "INTEGER":
				kind = KindInt
			case "REAL":
				kind = KindFloat
			default:
				kind = KindText
			}
		}
		d.Columns = append(d.Columns, Column{Name: t.Name(), Kind: kind})
	}

	for rows.Next() {
		values := make([]any, len(d.Columns))
		targets := make([]any, len(d.Columns))
		for i, col := range d.Columns {
			switch col.Kind {
			case KindInt, KindCents:
				targets[i] = new(sql.NullInt64)
			case KindFloat:
				targets[i] = new(sql.NullFloat64)
			default:
				targets[i] = new(sql.NullString)
			}
		}
		if err := rows.Scan(targets...); err != nil {
			return Dataset{}, fmt.Errorf("store: export %s: %w", name, err)
		}
		for i, target := range targets {
			switch v := target.(type) {
			case *sql.NullInt64:
				if v.Valid {
					values[i] = v.Int64
				}
			case *sql.NullFloat64:
				if v.Valid {
					values[i] = v.Float64
				}
			case *sql.NullString:
				if v.Valid {
					values[i] = v.String
				}
			}
		}
		d.Rows = append(d.Rows, values)
	}
	return d, rows.Err()
}

// Backup writes a consistent copy of the whole database to path, which must
// not exist yet, and checks the copy's integrity.
func (s *Store) Backup(ctx context.Context, path string) error {
	if _, err := s.DB.ExecContext(ctx, `VACUUM INTO ?`, path); err != nil {
		return fmt.Errorf("store: backup: %w", err)
	}
	copy, err := sql.Open("sqlite", "file:"+path+"?mode=ro")
	if err != nil {
		return fmt.Errorf("store: check backup: %w", err)
	}
	defer copy.Close()
	var result string
	if err := copy.QueryRowContext(ctx, `PRAGMA integrity_check`).Scan(&result); err != nil {
		return fmt.Errorf("store: check backup: %w", err)
	}
	if result != "ok" {
		return fmt.Errorf("store: backup %s failed its integrity check: %s", path, result)
	}
	return nil
}
