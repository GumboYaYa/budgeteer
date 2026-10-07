package reserve

import (
	"context"

	"github.com/GumboYaYa/budgeteer/internal/store"
)

// Status is the plan together with where its saved amount comes from.
type Status struct {
	Plan
	// Account is the account that holds the reserve; HasAccount is false if
	// none is chosen. Without an account, or while its balance is not known,
	// the plan counts with nothing saved.
	Account      store.Account
	HasAccount   bool
	BalanceKnown bool
}

// Load builds the plan as of the newest transaction in the database, so that
// the bills and the balance of the reserve account refer to the same day.
// With an empty database the plan is empty and has no AsOf.
func Load(ctx context.Context, st *store.Store) (Status, error) {
	var s Status
	accounts, err := st.ListAccounts(ctx)
	if err != nil {
		return Status{}, err
	}
	for _, a := range accounts {
		if a.HoldsReserve {
			s.Account, s.HasAccount = a, true
		}
	}
	asOf, err := st.LatestDate(ctx)
	if err != nil || asOf == "" {
		return s, err
	}

	var saved int64
	if s.HasAccount {
		balances, err := st.AccountBalances(ctx, asOf)
		if err != nil {
			return Status{}, err
		}
		for _, b := range balances {
			if b.Slug == s.Account.Slug {
				saved, s.BalanceKnown = b.Cents, true
			}
		}
	}
	from, err := Window(asOf)
	if err != nil {
		return Status{}, err
	}
	paid, err := st.ReserveTransactions(ctx, from, asOf)
	if err != nil {
		return Status{}, err
	}
	s.Plan, err = Build(asOf, paid, saved)
	return s, err
}
