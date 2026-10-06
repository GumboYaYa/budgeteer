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
	colExcluded        = "Analyse-Vom frei verfuegbaren Einkommen ausgeschlossen"
	colContract        = "Analyse-Vertrag"
	colTags            = "Tags"
	colBookingID       = "Buchungs-ID"
	colSplitOriginal   = "Referenz-Original-ID"
	colSplitType       = "Split-Typ"
)

var requiredColumns = []string{
	colBookingDate, colAccountIBAN, colAccountName, colAmount, colCurrency,
	colCounterparty, colCounterpartyRef, colPurpose, colEndToEndRef, colMandateRef,
	colCreditorID, colMainCategory, colSubCategory, colTransfer, colExcluded,
	colContract, colTags, colBookingID, colSplitOriginal, colSplitType,
}

// TagContract marks transactions that Finanzguru recognised as a contract.
const TagContract = "vertrag"

// Card payments start with the purchase timestamp:
// "2026-10-03T05:07 Debitk.12 ... (POS)".
var cardPurpose = regexp.MustCompile(`^(\d{4}-\d{2}-\d{2})T\d{2}:\d{2} Debitk\.`)

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
	return candidates, nil
}

func normalize(row importer.RawRow) (importer.Candidate, error) {
	get := func(column string) string { return strings.TrimSpace(row.Fields[column]) }

	// Split transactions are not supported (plan D15). Importing a part as a
	// whole transaction would be silently wrong, so stop instead.
	if get(colSplitType) != "" || get(colSplitOriginal) != "" {
		return importer.Candidate{}, fmt.Errorf("split transactions are not supported (%s=%q, %s=%q)",
			colSplitType, get(colSplitType), colSplitOriginal, get(colSplitOriginal))
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
	excluded, err := yesNo(get(colExcluded))
	if err != nil {
		return importer.Candidate{}, fmt.Errorf("%s: %w", colExcluded, err)
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
		MainCategory:       get(colMainCategory),
		SubCategory:        get(colSubCategory),
		ExcludedFromIncome: excluded,
		Tags:               tags,
	}, nil
}

func purchaseDate(purpose string) string {
	m := cardPurpose.FindStringSubmatch(purpose)
	if m == nil {
		return ""
	}
	if _, err := time.Parse(time.DateOnly, m[1]); err != nil {
		return ""
	}
	return m[1]
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
