// Package finanzguru parses the Finanzguru export. Each export contains the
// full history of all accounts, so it is imported repeatedly; rows are matched
// by their Buchungs-ID.
package finanzguru

import (
	"fmt"
	"io"
	"regexp"
	"strings"
	"time"

	"github.com/GumboYaYa/budgeteer/internal/importer"
	"github.com/GumboYaYa/budgeteer/internal/money"
	"github.com/GumboYaYa/budgeteer/internal/store"
)

const Source = "finanzguru"

// Column headers of the export. Columns not listed here (balance, derived
// analysis columns) are kept in the raw layer only.
const (
	colBookingDate     = "Buchungstag"
	colAccountIBAN     = "Referenzkonto"
	colAccountName     = "Name Referenzkonto"
	colAmount          = "Betrag"
	colCurrency        = "Waehrung"
	colCounterparty    = "Beguenstigter/Auftraggeber"
	colCounterpartyRef = "IBAN Beguenstigter/Auftraggeber"
	colPurpose         = "Verwendungszweck"
	colEndToEndRef     = "E-Ref"
	colMandateRef      = "Mandatsreferenz"
	colCreditorID      = "Glaeubiger-ID"
	colMainCategory    = "Analyse-Hauptkategorie"
	colSubCategory     = "Analyse-Unterkategorie"
	colTransfer        = "Analyse-Umbuchung"
	colContract        = "Analyse-Vertrag"
	colTags            = "Tags"
	colBookingID       = "Buchungs-ID"
	colSplitOriginal   = "Referenz-Original-ID"
	colSplitType       = "Split-Typ"
)

var requiredColumns = []string{
	colBookingDate, colAccountIBAN, colAccountName, colAmount, colCurrency,
	colCounterparty, colCounterpartyRef, colPurpose, colEndToEndRef, colMandateRef,
	colCreditorID, colMainCategory, colSubCategory, colTransfer, colContract, colTags, colBookingID, colSplitOriginal, colSplitType,
}

// Values of the Split-Typ column. A split shows up as the original row with
// the full amount plus its parts, which add up to that amount: one or more
// Teilbuchung rows and one Restbetrag row, each pointing to the original
// through Referenz-Original-ID. The parts are imported as transactions of
// their own and the original is not, which is how Finanzguru itself counts
// them.
const (
	splitOriginal  = "Original"
	splitPart      = "Teilbuchung"
	splitRemainder = "Restbetrag"
)

// TagContract marks transactions that Finanzguru recognised as a contract.
const TagContract = "vertrag"

// Card payments carry the purchase date in the purpose, in one of two forms
// depending on the bank: "2026-10-03T05:07 Debitk.12 ... (POS)" or
// "VISA Debitkartenumsatz vom 03.10.2026".
var (
	cardTimestamp = regexp.MustCompile(`^(\d{4}-\d{2}-\d{2})T\d{2}:\d{2} Debitk\.`)
	cardVisa      = regexp.MustCompile(`VISA Debitkartenumsatz vom (\d{2}\.\d{2}\.\d{4})`)
)

type Parser struct{}

func (Parser) Source() string             { return Source }
func (Parser) Side() importer.CutoverSide { return importer.UpToCutover }

func (Parser) Read(r io.Reader) ([]importer.RawRow, error) {
	return importer.ReadCSV(r, requiredColumns)
}

func (Parser) Normalize(rows []importer.RawRow) ([]importer.Candidate, error) {
	candidates := make([]importer.Candidate, 0, len(rows))
	for _, row := range rows {
		c, err := normalize(row)
		if err != nil {
			return nil, fmt.Errorf("line %d: %w", row.LineNo, err)
		}
		candidates = append(candidates, c)
	}
	if err := checkSplits(candidates); err != nil {
		return nil, err
	}
	return candidates, nil
}

// checkSplits makes sure that leaving out the split originals loses no money:
// every original must be fully covered by its parts, and every part must
// belong to an original in the same file.
func checkSplits(candidates []importer.Candidate) error {
	type original struct {
		line      int
		amount    int64
		parts     int
		partsSum  int64
		accountID string
	}
	originals := make(map[string]*original)
	for _, c := range candidates {
		if c.Superseded {
			originals[c.Tx.ExternalID] = &original{line: c.Row.LineNo, amount: c.Tx.AmountCents, accountID: c.AccountIBAN}
		}
	}
	for _, c := range candidates {
		ref := strings.TrimSpace(c.Row.Fields[colSplitOriginal])
		if ref == "" {
			continue
		}
		o := originals[ref]
		if o == nil {
			return fmt.Errorf("line %d: split part refers to %s %q, which is not a split original in this file",
				c.Row.LineNo, colSplitOriginal, ref)
		}
		if c.AccountIBAN != o.accountID {
			return fmt.Errorf("line %d: split part is on a different account than its original (line %d)", c.Row.LineNo, o.line)
		}
		o.parts++
		o.partsSum += c.Tx.AmountCents
	}
	for _, o := range originals {
		if o.parts == 0 || o.partsSum != o.amount {
			return fmt.Errorf("line %d: split original of %s is not covered by its parts (%d parts, sum %s)",
				o.line, money.Format(o.amount), o.parts, money.Format(o.partsSum))
		}
	}
	return nil
}

func normalize(row importer.RawRow) (importer.Candidate, error) {
	get := func(column string) string { return strings.TrimSpace(row.Fields[column]) }

	superseded := false
	switch splitType, ref := get(colSplitType), get(colSplitOriginal); {
	case splitType == "" && ref == "":
	case splitType == splitOriginal && ref == "":
		superseded = true
	case (splitType == splitPart || splitType == splitRemainder) && ref != "":
	default:
		return importer.Candidate{}, fmt.Errorf("unexpected split columns (%s=%q, %s=%q)",
			colSplitType, splitType, colSplitOriginal, ref)
	}

	bookingID := get(colBookingID)
	if bookingID == "" {
		return importer.Candidate{}, fmt.Errorf("%s is empty", colBookingID)
	}
	bookingDate, err := importer.ParseDateDE(get(colBookingDate))
	if err != nil {
		return importer.Candidate{}, fmt.Errorf("%s: %w", colBookingDate, err)
	}
	amount, err := money.ParseEN(get(colAmount))
	if err != nil {
		return importer.Candidate{}, fmt.Errorf("%s: %w", colAmount, err)
	}
	transfer, err := yesNo(get(colTransfer))
	if err != nil {
		return importer.Candidate{}, fmt.Errorf("%s: %w", colTransfer, err)
	}
	contract, err := yesNo(get(colContract))
	if err != nil {
		return importer.Candidate{}, fmt.Errorf("%s: %w", colContract, err)
	}

	tags := splitTags(get(colTags))
	if contract {
		tags = append(tags, TagContract)
	}

	purpose := get(colPurpose)
	return importer.Candidate{
		Row:         row,
		Superseded:  superseded,
		AccountIBAN: get(colAccountIBAN),
		AccountName: get(colAccountName),
		Tx: store.Transaction{
			ExternalID:      bookingID,
			DedupKey:        "fg:" + bookingID,
			BookingDate:     bookingDate,
			PurchaseDate:    purchaseDate(purpose),
			AmountCents:     amount,
			Currency:        get(colCurrency),
			Counterparty:    get(colCounterparty),
			CounterpartyRef: get(colCounterpartyRef),
			Purpose:         purpose,
			CreditorID:      get(colCreditorID),
			MandateRef:      get(colMandateRef),
			EndToEndRef:     get(colEndToEndRef),
			IsTransfer:      transfer,
		},
		MainCategory: get(colMainCategory),
		SubCategory:  get(colSubCategory),
		Tags:         tags,
	}, nil
}

func purchaseDate(purpose string) string {
	if m := cardTimestamp.FindStringSubmatch(purpose); m != nil {
		if _, err := time.Parse(time.DateOnly, m[1]); err == nil {
			return m[1]
		}
	}
	if m := cardVisa.FindStringSubmatch(purpose); m != nil {
		if date, err := importer.ParseDateDE(m[1]); err == nil {
			return date
		}
	}
	return ""
}

func yesNo(s string) (bool, error) {
	switch strings.ToLower(s) {
	case "ja":
		return true, nil
	case "nein", "":
		return false, nil
	}
	return false, fmt.Errorf("expected ja or nein, got %q", s)
}

// splitTags splits the Tags column on commas and semicolons.
func splitTags(s string) []string {
	var tags []string
	seen := make(map[string]bool)
	for _, tag := range strings.FieldsFunc(s, func(r rune) bool { return r == ',' || r == ';' }) {
		tag = strings.ToLower(strings.TrimSpace(tag))
		if tag != "" && !seen[tag] {
			seen[tag] = true
			tags = append(tags, tag)
		}
	}
	return tags
}
