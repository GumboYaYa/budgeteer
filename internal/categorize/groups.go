package categorize

import (
	"context"
	"strings"

	"github.com/GumboYaYa/budgeteer/internal/store"
)

// BasisMandate is the evidence of a direct debit: creditor id and mandate
// reference together identify one contract.
const BasisMandate = "mandate"

// groupBases are the kinds of evidence for a recurring group, in the order
// they are tried. The first kind that has history for a transaction decides.
var groupBases = []string{BasisMandate, BasisName, BasisCreditor}

// GroupScope chooses the transactions AssignGroups may put into a group.
type GroupScope struct {
	TxIDs []int64 // only these (an import's new transactions); nil = all
	From  string  // only those booked on or after this date; "" = no limit
}

// GroupAssignment is one transaction put into a group.
type GroupAssignment struct {
	TxID    int64
	GroupID int64
	Basis   string
}

// AssignGroups puts the transactions in scope that are in no group into the
// group their earlier counterparts are in. A transaction whose group, or "no
// group", was chosen by hand is left alone, and so are transfers. With dryRun
// nothing is changed. AssignGroups can be called on a store that is inside a
// transaction.
func AssignGroups(ctx context.Context, st *store.Store, scope GroupScope, dryRun bool) ([]GroupAssignment, error) {
	if scope.TxIDs != nil && len(scope.TxIDs) == 0 {
		return nil, nil
	}
	examples, err := st.GroupExamples(ctx)
	if err != nil {
		return nil, err
	}
	var wanted map[int64]bool
	if scope.TxIDs != nil {
		wanted = make(map[int64]bool, len(scope.TxIDs))
		for _, id := range scope.TxIDs {
			wanted[id] = true
		}
	}

	ix := newGroupIndex()
	var assigned []GroupAssignment
	for _, e := range examples {
		open := e.GroupID == 0 && !e.Manual &&
			(wanted == nil || wanted[e.TxID]) && (scope.From == "" || e.BookingDate >= scope.From)
		if open {
			if groupID, basis, ok := ix.predict(e); ok {
				if !dryRun {
					if err := st.SetAutoRecurringGroup(ctx, e.TxID, groupID); err != nil {
						return nil, err
					}
				}
				assigned = append(assigned, GroupAssignment{TxID: e.TxID, GroupID: groupID, Basis: basis})
				// A later payment of the same series learns from this one.
				e.GroupID = groupID
			}
		}
		ix.add(e)
	}
	return assigned, nil
}

// seen is an earlier transaction as far as group prediction needs it.
type seen struct {
	groupID     int64
	amountCents int64
}

// groupHistory keeps the two newest transactions for one key.
type groupHistory struct {
	newest, before seen
	count          int
}

// groupIndex holds the history group predictions are made from.
type groupIndex struct {
	byBasis map[string]map[string]*groupHistory
}

func newGroupIndex() *groupIndex {
	ix := &groupIndex{byBasis: make(map[string]map[string]*groupHistory, len(groupBases))}
	for _, b := range groupBases {
		ix.byBasis[b] = make(map[string]*groupHistory)
	}
	return ix
}

// groupKeys returns the key of the example for each kind of evidence; an
// empty key means the example has nothing to offer for that kind.
func groupKeys(e store.GroupExample) map[string]string {
	creditor := strings.ToUpper(strings.TrimSpace(e.CreditorID))
	mandate := strings.ToUpper(strings.TrimSpace(e.MandateRef))
	var mandateKey string
	if creditor != "" && mandate != "" {
		mandateKey = creditor + "|" + mandate
	}
	return map[string]string{
		BasisMandate:  mandateKey,
		BasisName:     strings.Join(Normalize(e.Counterparty), " "),
		BasisCreditor: creditor,
	}
}

// add records one more transaction, newer than all before, in a group or
// not.
func (ix *groupIndex) add(e store.GroupExample) {
	for basis, key := range groupKeys(e) {
		if key == "" {
			continue
		}
		h := ix.byBasis[basis][key]
		if h == nil {
			h = &groupHistory{}
			ix.byBasis[basis][key] = h
		}
		h.before, h.newest = h.newest, seen{groupID: e.GroupID, amountCents: e.AmountCents}
		h.count++
	}
}

// predict returns the group of the earlier transactions that share a key with
// e. The newest transaction with the same mandate decides on its own. A name
// or creditor id needs its two newest transactions in the same group, and e's
// amount must have the sign of the newest one and be within a quarter of it.
func (ix *groupIndex) predict(e store.GroupExample) (int64, string, bool) {
	k := groupKeys(e)
	for _, basis := range groupBases {
		h := ix.byBasis[basis][k[basis]]
		if k[basis] == "" || h == nil {
			continue
		}
		g := h.newest.groupID
		if g == 0 {
			return 0, "", false
		}
		if basis == BasisMandate {
			return g, basis, true
		}
		n := h.newest.amountCents
		if h.count < 2 || h.before.groupID != g || (e.AmountCents < 0) != (n < 0) || 4*abs(e.AmountCents-n) > abs(n) {
			return 0, "", false
		}
		return g, basis, true
	}
	return 0, "", false
}

func abs(n int64) int64 {
	if n < 0 {
		return -n
	}
	return n
}
