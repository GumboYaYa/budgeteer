package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

// Intervals of a recurring group. The empty string means "not known".
const (
	IntervalMonthly    = "monthly"
	IntervalQuarterly  = "quarterly"
	IntervalHalfYearly = "half-yearly"
	IntervalYearly     = "yearly"
)

// Intervals lists the known intervals, shortest first.
var Intervals = []string{IntervalMonthly, IntervalQuarterly, IntervalHalfYearly, IntervalYearly}

// IntervalMonths returns the length of an interval in months, 0 if it is not
// known.
func IntervalMonths(interval string) int {
	switch interval {
	case IntervalMonthly:
		return 1
	case IntervalQuarterly:
		return 3
	case IntervalHalfYearly:
		return 6
	case IntervalYearly:
		return 12
	}
	return 0
}

// Verdicts of the review of a recurring group. The empty string means
// "undecided".
const (
	VerdictKeep   = "keep"
	VerdictCancel = "cancel"
)

// RecurringGroup is a named series of recurring transactions.
type RecurringGroup struct {
	ID       int64
	Name     string
	Interval string
	// CoversReserve makes every transaction of the group count as an
	// irregular expense for the reserve.
	CoversReserve bool
	// Active is false for a series that has ended. An inactive group keeps
	// its transactions but no longer counts for the monthly cost or the
	// reserve.
	Active bool
	// Mandatory marks a group that cannot be cancelled, such as a loan. It
	// is left out when costs are reviewed.
	Mandatory bool
	// Verdict is what the review decided: VerdictKeep, VerdictCancel or ""
	// while undecided.
	Verdict string

	// Filled by ListRecurringGroups: number of transactions, and date and
	// amount of the newest one.
	Count     int
	LastDate  string
	LastCents int64
}

// Lapsed reports whether an active group looks as if it has ended: by asOf
// (YYYY-MM-DD) more than two of its intervals have passed since its newest
// transaction.
func (g RecurringGroup) Lapsed(asOf string) bool {
	months := IntervalMonths(g.Interval)
	if !g.Active || months == 0 || g.Count == 0 {
		return false
	}
	last, err := time.Parse(time.DateOnly, g.LastDate)
	if err != nil {
		return false
	}
	return last.AddDate(0, 2*months, 0).Format(time.DateOnly) < asOf
}

// MonthlyCents is what the group costs per month, judged by its newest
// transaction; ok is false if the group is inactive or empty, or its interval
// is not known.
func (g RecurringGroup) MonthlyCents() (cents int64, ok bool) {
	months := int64(IntervalMonths(g.Interval))
	if !g.Active || months == 0 || g.Count == 0 {
		return 0, false
	}
	if g.LastCents < 0 {
		return -((-g.LastCents + months/2) / months), true
	}
	return (g.LastCents + months/2) / months, true
}

// YearlyCents is what the group costs per year, judged by its newest
// transaction; ok is as for MonthlyCents.
func (g RecurringGroup) YearlyCents() (cents int64, ok bool) {
	months := int64(IntervalMonths(g.Interval))
	if !g.Active || months == 0 || g.Count == 0 {
		return 0, false
	}
	return g.LastCents * (12 / months), true
}

// ListRecurringGroups returns all groups, the active ones first, each part
// ordered by name.
func (s *Store) ListRecurringGroups(ctx context.Context) ([]RecurringGroup, error) {
	rows, err := s.q.QueryContext(ctx, `
		SELECT g.id, g.name, COALESCE(g.interval, ''), g.covers_reserve, g.active, g.mandatory, COALESCE(g.verdict, ''),
		       (SELECT count(*) FROM transactions t WHERE t.recurring_group_id = g.id),
		       COALESCE((SELECT t.booking_date FROM transactions t WHERE t.recurring_group_id = g.id
		                 ORDER BY t.booking_date DESC, t.id DESC LIMIT 1), ''),
		       COALESCE((SELECT t.amount_cents FROM transactions t WHERE t.recurring_group_id = g.id
		                 ORDER BY t.booking_date DESC, t.id DESC LIMIT 1), 0)
		FROM recurring_groups g
		ORDER BY g.active DESC, g.name COLLATE NOCASE`)
	if err != nil {
		return nil, fmt.Errorf("store: list recurring groups: %w", err)
	}
	defer rows.Close()
	var groups []RecurringGroup
	for rows.Next() {
		var g RecurringGroup
		if err := rows.Scan(&g.ID, &g.Name, &g.Interval, &g.CoversReserve, &g.Active, &g.Mandatory, &g.Verdict, &g.Count, &g.LastDate, &g.LastCents); err != nil {
			return nil, fmt.Errorf("store: list recurring groups: %w", err)
		}
		groups = append(groups, g)
	}
	return groups, rows.Err()
}

// EnsureRecurringGroup returns the id of the group with the given name
// (compared without regard to case), creating it if needed.
func (s *Store) EnsureRecurringGroup(ctx context.Context, name string) (int64, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return 0, errors.New("the group needs a name")
	}
	var id int64
	err := s.q.QueryRowContext(ctx, `SELECT id FROM recurring_groups WHERE name = ?`, name).Scan(&id)
	if err == nil {
		return id, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return 0, fmt.Errorf("store: look up recurring group: %w", err)
	}
	res, err := s.q.ExecContext(ctx, `INSERT INTO recurring_groups (name) VALUES (?)`, name)
	if err != nil {
		return 0, fmt.Errorf("store: create recurring group: %w", err)
	}
	return res.LastInsertId()
}

// SetRecurringGroup puts a transaction into a group by hand; groupID 0 takes
// it out of its group. Imports no longer change the group afterwards.
func (s *Store) SetRecurringGroup(ctx context.Context, transactionID, groupID int64) error {
	var group any
	if groupID != 0 {
		var exists int
		if err := s.q.QueryRowContext(ctx, `SELECT count(*) FROM recurring_groups WHERE id = ?`, groupID).Scan(&exists); err != nil {
			return fmt.Errorf("store: set recurring group: %w", err)
		}
		if exists == 0 {
			return fmt.Errorf("recurring group %d: %w", groupID, ErrNotFound)
		}
		group = groupID
	}
	res, err := s.q.ExecContext(ctx,
		`UPDATE transactions SET recurring_group_id = ?, recurring_manual = 1 WHERE id = ?`, group, transactionID)
	if err != nil {
		return fmt.Errorf("store: set recurring group: %w", err)
	}
	if n, err := res.RowsAffected(); err == nil && n == 0 {
		return ErrNotFound
	}
	return nil
}

// RenameRecurringGroup refuses an empty name and a name another group has.
func (s *Store) RenameRecurringGroup(ctx context.Context, id int64, name string) error {
	name = strings.TrimSpace(name)
	if name == "" {
		return errors.New("the group needs a name")
	}
	res, err := s.q.ExecContext(ctx, `UPDATE recurring_groups SET name = ? WHERE id = ?`, name, id)
	if err != nil {
		if strings.Contains(err.Error(), "UNIQUE constraint failed") {
			return fmt.Errorf("another group is already called %q; merge the two instead", name)
		}
		return fmt.Errorf("store: rename recurring group: %w", err)
	}
	return found(res)
}

// SetRecurringInterval sets the interval of a group; "" means not known. A
// group that becomes monthly is no longer covered by the reserve.
func (s *Store) SetRecurringInterval(ctx context.Context, id int64, interval string) error {
	if interval != "" && IntervalMonths(interval) == 0 {
		return fmt.Errorf("unknown interval %q", interval)
	}
	res, err := s.q.ExecContext(ctx, `
		UPDATE recurring_groups
		SET interval = ?1, covers_reserve = CASE WHEN ?1 = 'monthly' THEN 0 ELSE covers_reserve END
		WHERE id = ?2`, nullable(interval), id)
	if err != nil {
		return fmt.Errorf("store: set recurring interval: %w", err)
	}
	return found(res)
}

// SetRecurringReserve switches whether the group's transactions count as
// irregular expenses for the reserve. A monthly group cannot be covered: the
// reserve is for what comes less often.
func (s *Store) SetRecurringReserve(ctx context.Context, id int64, covers bool) error {
	return s.atomic(ctx, func(tx *Store) error {
		var interval string
		err := tx.q.QueryRowContext(ctx, `SELECT COALESCE(interval, '') FROM recurring_groups WHERE id = ?`, id).Scan(&interval)
		if errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return fmt.Errorf("store: set recurring reserve: %w", err)
		}
		if covers && interval == IntervalMonthly {
			return errors.New("a monthly group cannot be covered by the reserve; that is for expenses that come less often")
		}
		if _, err := tx.q.ExecContext(ctx, `UPDATE recurring_groups SET covers_reserve = ? WHERE id = ?`, covers, id); err != nil {
			return fmt.Errorf("store: set recurring reserve: %w", err)
		}
		return nil
	})
}

// SetRecurringActive marks a group as running or as ended.
func (s *Store) SetRecurringActive(ctx context.Context, id int64, active bool) error {
	res, err := s.q.ExecContext(ctx, `UPDATE recurring_groups SET active = ? WHERE id = ?`, active, id)
	if err != nil {
		return fmt.Errorf("store: set recurring active: %w", err)
	}
	return found(res)
}

// SetRecurringMandatory marks a group as one that cannot be cancelled. A
// "cancel" verdict does not survive that.
func (s *Store) SetRecurringMandatory(ctx context.Context, id int64, mandatory bool) error {
	res, err := s.q.ExecContext(ctx, `
		UPDATE recurring_groups
		SET mandatory = ?1, verdict = CASE WHEN ?1 AND verdict = 'cancel' THEN NULL ELSE verdict END
		WHERE id = ?2`, mandatory, id)
	if err != nil {
		return fmt.Errorf("store: set recurring mandatory: %w", err)
	}
	return found(res)
}

// SetRecurringVerdict records what the review decided for a group; "" means
// undecided. A mandatory group cannot be marked for cancelling.
func (s *Store) SetRecurringVerdict(ctx context.Context, id int64, verdict string) error {
	if verdict != "" && verdict != VerdictKeep && verdict != VerdictCancel {
		return fmt.Errorf("unknown verdict %q", verdict)
	}
	return s.atomic(ctx, func(tx *Store) error {
		var mandatory bool
		err := tx.q.QueryRowContext(ctx, `SELECT mandatory FROM recurring_groups WHERE id = ?`, id).Scan(&mandatory)
		if errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return fmt.Errorf("store: set recurring verdict: %w", err)
		}
		if mandatory && verdict == VerdictCancel {
			return errors.New("a mandatory group cannot be cancelled; switch off mandatory first")
		}
		if _, err := tx.q.ExecContext(ctx, `UPDATE recurring_groups SET verdict = ? WHERE id = ?`, nullable(verdict), id); err != nil {
			return fmt.Errorf("store: set recurring verdict: %w", err)
		}
		return nil
	})
}

// RecurringCost is a group with the figures for reviewing what it costs.
type RecurringCost struct {
	RecurringGroup
	// PaidYearCents sums the group's transactions of the twelve months up
	// to the date asked for.
	PaidYearCents int64
	// PrevDate and PrevCents are the payment the newest one is compared
	// with: the newest that is at least eleven months older, or else the
	// group's first. PrevDate is "" for a group with fewer than two
	// transactions.
	PrevDate  string
	PrevCents int64
	// CategoryID and CategoryName are the main category most of the group's
	// transactions are booked to; 0 and "" if most are uncategorized.
	CategoryID   int64
	CategoryName string
}

// ChangeCents is the newest amount minus the one it is compared with; ok is
// false if there is no earlier payment.
func (c RecurringCost) ChangeCents() (cents int64, ok bool) {
	if c.PrevDate == "" {
		return 0, false
	}
	return c.LastCents - c.PrevCents, true
}

// YearlyChangeCents is ChangeCents for a whole year of payments; ok is false
// as well if the group is inactive or its interval is not known.
func (c RecurringCost) YearlyChangeCents() (cents int64, ok bool) {
	months := int64(IntervalMonths(c.Interval))
	change, ok := c.ChangeCents()
	if !c.Active || months == 0 || !ok {
		return 0, false
	}
	return change * (12 / months), true
}

// ListRecurringCosts returns all groups as ListRecurringGroups does, with
// their cost figures as of asOf (YYYY-MM-DD).
func (s *Store) ListRecurringCosts(ctx context.Context, asOf string) ([]RecurringCost, error) {
	groups, err := s.ListRecurringGroups(ctx)
	if err != nil {
		return nil, err
	}
	costs := make([]RecurringCost, len(groups))
	byID := make(map[int64]*RecurringCost, len(groups))
	for i, g := range groups {
		costs[i].RecurringGroup = g
		byID[g.ID] = &costs[i]
	}
	yearBefore := ""
	if day, err := time.Parse(time.DateOnly, asOf); err == nil {
		yearBefore = day.AddDate(-1, 0, 0).Format(time.DateOnly)
	}

	// Newest first within a group, in the order ListRecurringGroups takes
	// the newest transaction.
	rows, err := s.q.QueryContext(ctx, `
		SELECT t.recurring_group_id, t.booking_date, t.amount_cents, COALESCE(p.id, c.id, 0), COALESCE(p.name, c.name, '')
		FROM transactions t
		LEFT JOIN allocations al ON al.transaction_id = t.id AND al.source <> 'suggested'
		LEFT JOIN categories c ON c.id = al.category_id
		LEFT JOIN categories p ON p.id = c.parent_id
		WHERE t.recurring_group_id IS NOT NULL
		ORDER BY t.recurring_group_id, t.booking_date DESC, t.id DESC`)
	if err != nil {
		return nil, fmt.Errorf("store: list recurring costs: %w", err)
	}
	defer rows.Close()
	var (
		current  *RecurringCost
		seen     int
		before   string        // a payment up to this date is old enough to compare with
		compared bool          // such a payment was found
		uses     map[int64]int // transactions per main category
	)
	for rows.Next() {
		var (
			groupID, cents, categoryID int64
			date, categoryName         string
		)
		if err := rows.Scan(&groupID, &date, &cents, &categoryID, &categoryName); err != nil {
			return nil, fmt.Errorf("store: list recurring costs: %w", err)
		}
		if current == nil || current.ID != groupID {
			current, seen, compared, uses = byID[groupID], 0, false, map[int64]int{}
			before = ""
			if day, err := time.Parse(time.DateOnly, date); err == nil {
				before = day.AddDate(0, -11, 0).Format(time.DateOnly)
			}
		}
		if current == nil {
			continue
		}
		seen++
		if date > yearBefore && date <= asOf {
			current.PaidYearCents += cents
		}
		if seen > 1 && !compared {
			current.PrevDate, current.PrevCents = date, cents
			compared = date <= before
		}
		// The newest transaction decides between categories used equally often.
		uses[categoryID]++
		if seen == 1 || uses[categoryID] > uses[current.CategoryID] {
			current.CategoryID, current.CategoryName = categoryID, categoryName
		}
	}
	return costs, rows.Err()
}

// MergeRecurringGroup moves the transactions and source contracts of one
// group into another and deletes the first.
func (s *Store) MergeRecurringGroup(ctx context.Context, fromID, intoID int64) error {
	if fromID == intoID {
		return errors.New("a group cannot be merged into itself")
	}
	return s.atomic(ctx, func(tx *Store) error {
		var n int
		err := tx.q.QueryRowContext(ctx, `SELECT count(*) FROM recurring_groups WHERE id IN (?, ?)`, fromID, intoID).Scan(&n)
		if err != nil {
			return fmt.Errorf("store: merge recurring groups: %w", err)
		}
		if n != 2 {
			return ErrNotFound
		}
		for _, query := range []string{
			`UPDATE transactions SET recurring_group_id = ?2 WHERE recurring_group_id = ?1`,
			`UPDATE recurring_contracts SET group_id = ?2 WHERE group_id = ?1`,
		} {
			if _, err := tx.q.ExecContext(ctx, query, fromID, intoID); err != nil {
				return fmt.Errorf("store: merge recurring groups: %w", err)
			}
		}
		if _, err := tx.q.ExecContext(ctx, `DELETE FROM recurring_groups WHERE id = ?`, fromID); err != nil {
			return fmt.Errorf("store: merge recurring groups: %w", err)
		}
		return nil
	})
}

// DeleteRecurringGroup removes a group; its transactions are in no group
// afterwards. Contracts of a source that fed the group are remembered as
// dismissed, so a later import does not bring the group back.
func (s *Store) DeleteRecurringGroup(ctx context.Context, id int64) error {
	return s.atomic(ctx, func(tx *Store) error {
		for _, query := range []string{
			`UPDATE transactions SET recurring_group_id = NULL WHERE recurring_group_id = ?`,
			`UPDATE recurring_contracts SET group_id = NULL WHERE group_id = ?`,
		} {
			if _, err := tx.q.ExecContext(ctx, query, id); err != nil {
				return fmt.Errorf("store: delete recurring group: %w", err)
			}
		}
		res, err := tx.q.ExecContext(ctx, `DELETE FROM recurring_groups WHERE id = ?`, id)
		if err != nil {
			return fmt.Errorf("store: delete recurring group: %w", err)
		}
		return found(res)
	})
}

// ContractGroup returns the group a source's contract belongs to. known is
// false if the contract was never seen; groupID 0 with known true means its
// group was deleted.
func (s *Store) ContractGroup(ctx context.Context, source, externalID string) (groupID int64, known bool, err error) {
	var id sql.NullInt64
	err = s.q.QueryRowContext(ctx,
		`SELECT group_id FROM recurring_contracts WHERE source = ? AND external_id = ?`, source, externalID).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, false, nil
	}
	if err != nil {
		return 0, false, fmt.Errorf("store: look up contract: %w", err)
	}
	return id.Int64, true, nil
}

// CreateContractGroup creates a group for a contract first seen in a source.
// A name that is taken gets a number appended.
func (s *Store) CreateContractGroup(ctx context.Context, source, externalID, name, interval string) (int64, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		name = "Contract " + externalID
	}
	if IntervalMonths(interval) == 0 {
		interval = ""
	}
	var id int64
	for n := 1; ; n++ {
		candidate := name
		if n > 1 {
			candidate = fmt.Sprintf("%s %d", name, n)
		}
		res, err := s.q.ExecContext(ctx,
			`INSERT INTO recurring_groups (name, interval) VALUES (?, ?) ON CONFLICT (name) DO NOTHING`, candidate, nullable(interval))
		if err != nil {
			return 0, fmt.Errorf("store: create recurring group: %w", err)
		}
		if rows, _ := res.RowsAffected(); rows == 1 {
			if id, err = res.LastInsertId(); err != nil {
				return 0, fmt.Errorf("store: create recurring group: %w", err)
			}
			break
		}
	}
	_, err := s.q.ExecContext(ctx,
		`INSERT INTO recurring_contracts (source, external_id, group_id) VALUES (?, ?, ?)`, source, externalID, id)
	if err != nil {
		return 0, fmt.Errorf("store: remember contract: %w", err)
	}
	return id, nil
}

// AssignContractGroup puts a transaction into the group of its contract,
// unless its group was chosen by hand.
func (s *Store) AssignContractGroup(ctx context.Context, transactionID, groupID int64) error {
	_, err := s.q.ExecContext(ctx, `
		UPDATE transactions SET recurring_group_id = ?1
		WHERE id = ?2 AND recurring_manual = 0 AND recurring_group_id IS NOT ?1`, groupID, transactionID)
	if err != nil {
		return fmt.Errorf("store: assign recurring group: %w", err)
	}
	return nil
}

// found turns "no row changed" into ErrNotFound.
func found(res sql.Result) error {
	if n, err := res.RowsAffected(); err == nil && n == 0 {
		return ErrNotFound
	}
	return nil
}
