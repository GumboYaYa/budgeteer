package categorize

import (
	"context"

	"github.com/GumboYaYa/budgeteer/internal/store"
)

// Result reports what a run did with the uncategorized transactions.
type Result struct {
	Applied   int // categorized, because history is sure
	Suggested int // history points to a category, but not surely enough
	Left      int // no history
}

// Run categorizes the uncategorized transactions whose history is sure. The
// others stay in the inbox. With dryRun nothing is changed. Run can be called
// on a store that is inside a transaction.
func Run(ctx context.Context, st *store.Store, dryRun bool) (Result, error) {
	var result Result
	predictions, open, err := predict(ctx, st, nil)
	if err != nil {
		return Result{}, err
	}
	for _, e := range open {
		p, ok := predictions[e.TxID]
		switch {
		case !ok:
			result.Left++
		case !p.Sure:
			result.Suggested++
		default:
			result.Applied++
			if dryRun {
				continue
			}
			if err := st.SetAutoCategory(ctx, e.TxID, p.CategoryID, p.RuleID()); err != nil {
				return Result{}, err
			}
		}
	}
	return result, nil
}

// Suggest returns the predictions for those of the given transactions that
// are uncategorized and have history, by transaction id.
func Suggest(ctx context.Context, st *store.Store, txIDs []int64) (map[int64]Prediction, error) {
	if len(txIDs) == 0 {
		return nil, nil
	}
	predictions, _, err := predict(ctx, st, txIDs)
	return predictions, err
}

// predict loads the history and the uncategorized transactions (all of them
// when txIDs is nil) and returns both the predictions and the transactions.
func predict(ctx context.Context, st *store.Store, txIDs []int64) (map[int64]Prediction, []store.CategoryExample, error) {
	history, err := st.CategoryExamples(ctx)
	if err != nil {
		return nil, nil, err
	}
	open, err := st.UncategorizedExamples(ctx, txIDs)
	if err != nil {
		return nil, nil, err
	}
	ix := NewIndex(history)
	predictions := make(map[int64]Prediction, len(open))
	for _, e := range open {
		if p, ok := ix.Predict(e); ok {
			predictions[e.TxID] = p
		}
	}
	return predictions, open, nil
}
