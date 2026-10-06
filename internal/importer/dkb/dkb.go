// Package dkb parses the CSV export of a DKB account ("Umsätze" export).
//
// A file covers one account and a date range. Exports of overlapping ranges
// are imported safely: a row is identified by a hash of its content plus its
// position among identical rows of the same file.
package dkb

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strconv"
	"strings"

	"github.com/GumboYaYa/budgeteer/internal/importer"
	"github.com/GumboYaYa/budgeteer/internal/money"
	"github.com/GumboYaYa/budgeteer/internal/store"
)

const Source = "dkb_csv"

const (
	colBookingDate = "Buchungsdatum"
	colValueDate   = "Wertstellung"
	colStatus      = "Status"
	colPayer       = "Zahlungspflichtige*r"
	colPayee       = "Zahlungsempfänger*in"
	colPurpose     = "Verwendungszweck"
	colType        = "Umsatztyp"
	colIBAN        = "IBAN"
	colAmount      = "Betrag (€)"
	colCreditorID  = "Gläubiger-ID"
	colMandateRef  = "Mandatsreferenz"
	colCustomerRef = "Kundenreferenz"
)

var requiredColumns = []string{
	colBookingDate, colValueDate, colStatus, colPayer, colPayee, colPurpose,
	colType, colIBAN, colAmount, colCreditorID, colMandateRef, colCustomerRef,
}

const (
	statusBooked  = "Gebucht"
	statusPending = "Vorgemerkt"
	typeOutgoing  = "Ausgang"
	typeIncoming  = "Eingang"
)

var (
	// Card payments carry the real purchase date in the purpose. The IBAN
	// column then holds the card issuer's clearing account, not the merchant.
	cardPurpose = regexp.MustCompile(`VISA Debitkartenumsatz vom (\d{2}\.\d{2}\.\d{4})`)
	// Preamble line: "Kontostand vom 05.10.2026:" followed by the balance.
	balanceLabel = regexp.MustCompile(`^Kontostand vom (\d{2}\.\d{2}\.\d{2,4}):?$`)
)

// Parser reads DKB exports for one account. Account is the account's slug; it
// is part of every row's dedup key, so it must not change between imports.
type Parser struct {
	Account string
}

func (Parser) Source() string             { return Source }
func (Parser) Side() importer.CutoverSide { return importer.AfterCutover }

// DKB has no transfer flag; a booking to or from another own account is one.
func (Parser) OwnAccountTransfers() bool { return true }

// Read accepts the file as DKB writes it (semicolon-separated) and as Google
// Sheets re-exports it (comma-separated). The header follows a short preamble
// with the account and its balance.
func (Parser) Read(r io.Reader) (importer.File, error) {
	content, err := io.ReadAll(r)
	if err != nil {
		return importer.File{}, err
	}
	var preamble [][]string
	var rows []importer.RawRow
	var firstErr error
	found := false
	for _, comma := range []rune{',', ';'} {
		preamble, rows, err = importer.ReadCSVFormat(strings.NewReader(string(content)), importer.CSVFormat{
			Comma: comma, HeaderStart: colBookingDate, Required: requiredColumns,
		})
		if err == nil {
			found = true
			break
		}
		// With the wrong separator the header is not found. An error from
		// after the header means the separator was right and says more.
		if firstErr == nil || !errors.Is(err, importer.ErrNoHeader) {
			firstErr = err
		}
	}
	if !found {
		if errors.Is(firstErr, importer.ErrNoHeader) {
			return importer.File{}, fmt.Errorf("unexpected file format: no header line starting with %q; is this a DKB export?", colBookingDate)
		}
		return importer.File{}, firstErr
	}

	file := importer.File{Rows: rows}
	for _, line := range preamble {
		if len(line) < 2 {
			continue
		}
		m := balanceLabel.FindStringSubmatch(strings.TrimSpace(line[0]))
		if m == nil {
			continue
		}
		// The balance is a convenience; a file without a readable one is
		// still imported.
		date, dateErr := importer.ParseDateDE(m[1])
		cents, amountErr := parseAmount(line[1])
		if dateErr == nil && amountErr == nil {
			file.Balance, file.BalanceDate = cents, date
		}
	}
	return file, nil
}

func (p Parser) Normalize(rows []importer.RawRow) ([]importer.Candidate, error) {
	if p.Account == "" {
		return nil, errors.New("dkb: no account given")
	}
	candidates := make([]importer.Candidate, 0, len(rows))
	seen := make(map[string]int)
	for _, row := range rows {
		c, err := p.normalize(row, seen)
		if err != nil {
			return nil, fmt.Errorf("line %d: %w", row.LineNo, err)
		}
		candidates = append(candidates, c)
	}
	return candidates, nil
}

// normalize maps one row. seen counts identical booked rows so far, which
// makes the dedup keys of true duplicates within a file distinct.
func (p Parser) normalize(row importer.RawRow, seen map[string]int) (importer.Candidate, error) {
	get := func(column string) string { return strings.TrimSpace(row.Fields[column]) }

	bookingDate, err := importer.ParseDateDE(get(colBookingDate))
	if err != nil {
		return importer.Candidate{}, fmt.Errorf("%s: %w", colBookingDate, err)
	}
	valueDate := ""
	if v := get(colValueDate); v != "" {
		if valueDate, err = importer.ParseDateDE(v); err != nil {
			return importer.Candidate{}, fmt.Errorf("%s: %w", colValueDate, err)
		}
	}
	amount, err := parseAmount(get(colAmount))
	if err != nil {
		return importer.Candidate{}, fmt.Errorf("%s: %w", colAmount, err)
	}

	purpose := get(colPurpose)
	t := store.Transaction{
		BookingDate:     bookingDate,
		ValueDate:       valueDate,
		PurchaseDate:    purchaseDate(purpose),
		AmountCents:     amount,
		Counterparty:    counterparty(get(colType), amount, get(colPayer), get(colPayee)),
		CounterpartyRef: get(colIBAN),
		Purpose:         purpose,
		CreditorID:      get(colCreditorID),
		MandateRef:      get(colMandateRef),
		CustomerRef:     get(colCustomerRef),
	}
	c := importer.Candidate{Row: row, Tx: t}

	switch status := get(colStatus); status {
	case statusBooked:
	case statusPending:
		c.Pending = true
		return c, nil
	default:
		return importer.Candidate{}, fmt.Errorf("%s: unexpected value %q", colStatus, status)
	}

	identity := strings.Join([]string{
		p.Account, t.BookingDate, strconv.FormatInt(t.AmountCents, 10), t.Counterparty, t.Purpose, t.CustomerRef,
	}, "|")
	n := seen[identity]
	seen[identity] = n + 1
	sum := sha256.Sum256([]byte(identity + "|" + strconv.Itoa(n)))
	c.Tx.DedupKey = "dkb:" + hex.EncodeToString(sum[:])
	return c, nil
}

// counterparty picks the other party: the payee of an outgoing booking, the
// payer of an incoming one. Rows without a type (account statements) fall
// back to the sign of the amount.
func counterparty(bookingType string, amount int64, payer, payee string) string {
	switch {
	case bookingType == typeOutgoing:
		return payee
	case bookingType == typeIncoming:
		return payer
	case amount < 0:
		return payee
	case amount > 0:
		return payer
	case payee != "":
		return payee
	}
	return payer
}

// parseAmount reads German notation, with or without a currency sign:
// "-9,99", "1.234,56", "-57", "4.602,28 €".
func parseAmount(s string) (int64, error) {
	s = strings.TrimSpace(strings.NewReplacer("€", "", " ", "", "EUR", "").Replace(s))
	return money.ParseDE(s)
}

func purchaseDate(purpose string) string {
	m := cardPurpose.FindStringSubmatch(purpose)
	if m == nil {
		return ""
	}
	date, err := importer.ParseDateDE(m[1])
	if err != nil {
		return ""
	}
	return date
}
