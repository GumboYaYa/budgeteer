package categorize

import (
	"context"
	"strconv"
	"testing"

	"github.com/GumboYaYa/budgeteer/internal/store"
)

// gtx is a transaction for the group tests. group "" means no group, "-" no
// group chosen by hand.
type gtx struct {
	date         string
	cents        int64
	counterparty string
	creditor     string
	mandate      string
	group        string
	transfer     bool
}

// groupTx adds a transaction and puts it into its group, creating the group
// as needed.
func (f *fixture) groupTx(x gtx) int64 {
	f.t.Helper()
	ctx := context.Background()
	f.n++
	raw, err := f.st.PutRawRecord(ctx, f.importID, f.n, "{}")
	if err != nil {
		f.t.Fatal(err)
	}
	id, err := f.st.InsertTransaction(ctx, store.Transaction{
		AccountID: f.account, RawRecordID: raw, Source: "test", DedupKey: "g:" + strconv.Itoa(f.n),
		BookingDate: x.date, AmountCents: x.cents, Counterparty: x.counterparty, CreditorID: x.creditor,
		MandateRef: x.mandate, IsTransfer: x.transfer,
	})
	if err != nil {
		f.t.Fatal(err)
	}
	switch x.group {
	case "":
	case "-":
		if err := f.st.SetRecurringGroup(ctx, id, 0); err != nil {
			f.t.Fatal(err)
		}
	default:
		groupID, err := f.st.EnsureRecurringGroup(ctx, x.group)
		if err != nil {
			f.t.Fatal(err)
		}
		if err := f.st.SetRecurringGroup(ctx, id, groupID); err != nil {
			f.t.Fatal(err)
		}
	}
	return id
}

// groupOf returns the name of the transaction's group, "" if it has none.
func (f *fixture) groupOf(txID int64) string {
	f.t.Helper()
	var name string
	err := f.st.DB.QueryRow(`
		SELECT COALESCE(g.name, '') FROM transactions t LEFT JOIN recurring_groups g ON g.id = t.recurring_group_id
		WHERE t.id = ?`, txID).Scan(&name)
	if err != nil {
		f.t.Fatal(err)
	}
	return name
}

func TestAssignGroups(t *testing.T) {
	const creditor = "DE00ZZZ00000000001"
	tests := []struct {
		name      string
		history   []gtx
		candidate gtx
		want      string // group of the candidate afterwards
		basis     string
	}{
		{
			name: "same mandate, whatever the amount and name",
			history: []gtx{
				{date: "2026-01-15", cents: -8000, counterparty: "Stadtwerke Beispiel", creditor: creditor, mandate: "M-1", group: "Strom"},
			},
			candidate: gtx{date: "2026-02-15", cents: -21000, counterparty: "SWB Abschlag", creditor: "de00zzz00000000001 ", mandate: "m-1"},
			want:      "Strom", basis: BasisMandate,
		},
		{
			name: "the newest transaction of the mandate is in no group",
			history: []gtx{
				{date: "2026-01-15", cents: -8000, counterparty: "Stadtwerke Beispiel", creditor: creditor, mandate: "M-1", group: "Strom"},
				{date: "2026-02-15", cents: -8000, counterparty: "Stadtwerke Beispiel", creditor: creditor, mandate: "M-1"},
				{date: "2026-02-20", cents: -1000, counterparty: "Stadtwerke Beispiel", group: "Strom"},
				{date: "2026-02-21", cents: -1000, counterparty: "Stadtwerke Beispiel", group: "Strom"},
			},
			candidate: gtx{date: "2026-03-15", cents: -1000, counterparty: "Stadtwerke Beispiel", creditor: creditor, mandate: "M-1"},
		},
		{
			name: "a new mandate falls back to the name",
			history: []gtx{
				{date: "2026-01-15", cents: -999, counterparty: "Streamingdienst GmbH", creditor: creditor, mandate: "M-1", group: "Streaming"},
				{date: "2026-02-15", cents: -999, counterparty: "Streamingdienst GmbH", creditor: creditor, mandate: "M-1", group: "Streaming"},
			},
			candidate: gtx{date: "2026-03-15", cents: -1199, counterparty: "STREAMINGDIENST", creditor: creditor, mandate: "M-2"},
			want:      "Streaming", basis: BasisName,
		},
		{
			name: "same name, amount more than a quarter off",
			history: []gtx{
				{date: "2026-01-15", cents: -999, counterparty: "Streamingdienst", group: "Streaming"},
				{date: "2026-02-15", cents: -999, counterparty: "Streamingdienst", group: "Streaming"},
			},
			candidate: gtx{date: "2026-03-15", cents: -1299, counterparty: "Streamingdienst"},
		},
		{
			name: "same name, opposite sign",
			history: []gtx{
				{date: "2026-01-15", cents: -999, counterparty: "Streamingdienst", group: "Streaming"},
				{date: "2026-02-15", cents: -999, counterparty: "Streamingdienst", group: "Streaming"},
			},
			candidate: gtx{date: "2026-03-15", cents: 999, counterparty: "Streamingdienst"},
		},
		{
			name: "only one earlier transaction",
			history: []gtx{
				{date: "2026-02-15", cents: -999, counterparty: "Streamingdienst", group: "Streaming"},
			},
			candidate: gtx{date: "2026-03-15", cents: -999, counterparty: "Streamingdienst"},
		},
		{
			name: "the newest earlier transaction is in no group",
			history: []gtx{
				{date: "2026-01-15", cents: -999, counterparty: "Versand Muster", group: "Mitgliedschaft"},
				{date: "2026-02-15", cents: -999, counterparty: "Versand Muster", group: "Mitgliedschaft"},
				{date: "2026-02-20", cents: -999, counterparty: "Versand Muster"},
			},
			candidate: gtx{date: "2026-03-15", cents: -999, counterparty: "Versand Muster"},
		},
		{
			name: "the two newest are in different groups",
			history: []gtx{
				{date: "2026-01-15", cents: -999, counterparty: "Versand Muster", group: "Mitgliedschaft"},
				{date: "2026-02-15", cents: -999, counterparty: "Versand Muster", group: "Musik"},
			},
			candidate: gtx{date: "2026-03-15", cents: -999, counterparty: "Versand Muster"},
		},
		{
			name: "an unknown name falls back to the creditor id",
			history: []gtx{
				{date: "2026-01-01", cents: -50000, counterparty: "Hausverwaltung Beispiel", creditor: creditor, group: "Miete"},
				{date: "2026-02-01", cents: -50000, counterparty: "Hausverwaltung Beispiel", creditor: creditor, group: "Miete"},
			},
			candidate: gtx{date: "2026-03-01", cents: -52000, counterparty: "HV Beispiel", creditor: creditor},
			want:      "Miete", basis: BasisCreditor,
		},
		{
			name: "earlier transactions after the candidate do not count",
			history: []gtx{
				{date: "2026-04-15", cents: -999, counterparty: "Streamingdienst", group: "Streaming"},
				{date: "2026-05-15", cents: -999, counterparty: "Streamingdienst", group: "Streaming"},
			},
			candidate: gtx{date: "2026-03-15", cents: -999, counterparty: "Streamingdienst"},
		},
		{
			name: "transfers are no evidence",
			history: []gtx{
				{date: "2026-01-15", cents: -999, counterparty: "Streamingdienst", group: "Streaming"},
				{date: "2026-02-15", cents: -999, counterparty: "Streamingdienst", group: "Streaming"},
				{date: "2026-02-20", cents: -999, counterparty: "Streamingdienst", transfer: true},
			},
			candidate: gtx{date: "2026-03-15", cents: -999, counterparty: "Streamingdienst"},
			want:      "Streaming", basis: BasisName,
		},
		{
			name: "no group chosen by hand is kept",
			history: []gtx{
				{date: "2026-01-15", cents: -999, counterparty: "Streamingdienst", group: "Streaming"},
				{date: "2026-02-15", cents: -999, counterparty: "Streamingdienst", group: "Streaming"},
			},
			candidate: gtx{date: "2026-03-15", cents: -999, counterparty: "Streamingdienst", group: "-"},
		},
		{
			name: "a transfer is not put into a group",
			history: []gtx{
				{date: "2026-01-15", cents: -999, counterparty: "Streamingdienst", group: "Streaming"},
				{date: "2026-02-15", cents: -999, counterparty: "Streamingdienst", group: "Streaming"},
			},
			candidate: gtx{date: "2026-03-15", cents: -999, counterparty: "Streamingdienst", transfer: true},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			f := newFixture(t)
			for _, x := range tt.history {
				f.groupTx(x)
			}
			id := f.groupTx(tt.candidate)

			got, err := AssignGroups(ctx, f.st, GroupScope{TxIDs: []int64{id}}, false)
			if err != nil {
				t.Fatal(err)
			}
			if group := f.groupOf(id); group != tt.want {
				t.Errorf("group = %q, want %q", group, tt.want)
			}
			wantCount := 0
			if tt.want != "" {
				wantCount = 1
			}
			if len(got) != wantCount {
				t.Fatalf("assignments = %+v, want %d", got, wantCount)
			}
			if wantCount == 1 && (got[0].TxID != id || got[0].Basis != tt.basis) {
				t.Errorf("assignment = %+v, want transaction %d by %s", got[0], id, tt.basis)
			}
		})
	}
}

func TestAssignGroupsScope(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)
	payment := func(date, group string) int64 {
		return f.groupTx(gtx{date: date, cents: -999, counterparty: "Streamingdienst", group: group})
	}
	payment("2026-01-15", "Streaming")
	payment("2026-02-15", "Streaming")
	march := payment("2026-03-15", "")
	april := payment("2026-04-15", "")
	may := payment("2026-05-15", "")

	// A dry run reports, but changes nothing; within one run a payment
	// learns from the one before.
	dry, err := AssignGroups(ctx, f.st, GroupScope{}, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(dry) != 3 {
		t.Errorf("dry run assigned %d, want 3", len(dry))
	}
	if group := f.groupOf(march); group != "" {
		t.Errorf("dry run changed the group to %q", group)
	}

	// Only the transactions asked for, and only from the date on. May's
	// newest earlier payment stays in no group, which speaks against it.
	got, err := AssignGroups(ctx, f.st, GroupScope{TxIDs: []int64{march, may}}, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].TxID != march {
		t.Errorf("assignments = %+v, want only transaction %d", got, march)
	}
	if got, err := AssignGroups(ctx, f.st, GroupScope{From: "2026-05-01"}, false); err != nil || len(got) != 0 {
		t.Errorf("from May: %+v, %v; want nothing", got, err)
	}
	if got, err := AssignGroups(ctx, f.st, GroupScope{TxIDs: []int64{}}, false); err != nil || len(got) != 0 {
		t.Errorf("empty scope: %+v, %v", got, err)
	}

	// Catching up from April fills the rest.
	got, err = AssignGroups(ctx, f.st, GroupScope{From: "2026-04-01"}, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Errorf("catching up assigned %d, want 2", len(got))
	}
	for id, want := range map[int64]string{march: "Streaming", april: "Streaming", may: "Streaming"} {
		if group := f.groupOf(id); group != want {
			t.Errorf("transaction %d: group %q, want %q", id, group, want)
		}
	}
	if again, err := AssignGroups(ctx, f.st, GroupScope{}, false); err != nil || len(again) != 0 {
		t.Errorf("second run: %+v, %v", again, err)
	}
}
