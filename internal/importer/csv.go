package importer

import (
	"bufio"
	"encoding/csv"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"
)

// CSVFormat describes how to read a source's CSV file.
type CSVFormat struct {
	// Comma is the field separator; ',' if zero.
	Comma rune
	// HeaderStart is the first column name of the header line. Lines before
	// that line are returned as preamble. If empty, the first line is the
	// header.
	HeaderStart string
	// Required lists the columns that must be present.
	Required []string
}

// ErrNoHeader is returned by ReadCSVFormat when no readable line starts with
// CSVFormat.HeaderStart, typically because the separator is a different one.
var ErrNoHeader = errors.New("header line not found")

// ReadCSV reads a comma-separated file whose first line is the header and
// returns one RawRow per data row. A UTF-8 byte order mark and completely
// empty rows are ignored. Every header in required must be present.
func ReadCSV(r io.Reader, required []string) ([]RawRow, error) {
	_, rows, err := ReadCSVFormat(r, CSVFormat{Required: required})
	return rows, err
}

// ReadCSVFormat reads a CSV file as described by format. It returns the
// lines before the header and one RawRow per data row. A UTF-8 byte order
// mark and completely empty rows are ignored.
func ReadCSVFormat(r io.Reader, format CSVFormat) (preamble [][]string, rows []RawRow, err error) {
	br := bufio.NewReader(r)
	if bom, err := br.Peek(3); err == nil && string(bom) == "\xEF\xBB\xBF" {
		br.Discard(3)
	}
	cr := csv.NewReader(br)
	cr.FieldsPerRecord = -1
	if format.Comma != 0 {
		cr.Comma = format.Comma
	}

	var header []string
	for header == nil {
		record, err := cr.Read()
		if errors.Is(err, io.EOF) {
			if format.HeaderStart == "" {
				return nil, nil, errors.New("file is empty")
			}
			return nil, nil, ErrNoHeader
		}
		if err != nil {
			if format.HeaderStart != "" {
				// Unreadable before any header was seen: most likely the
				// wrong separator, so let the caller try another one.
				return nil, nil, fmt.Errorf("%w: %v", ErrNoHeader, err)
			}
			return nil, nil, fmt.Errorf("read header: %w", err)
		}
		if format.HeaderStart == "" || strings.TrimSpace(record[0]) == format.HeaderStart {
			header = record
		} else {
			preamble = append(preamble, record)
		}
	}
	columns := make(map[string]bool, len(header))
	for i, h := range header {
		header[i] = strings.TrimSpace(h)
		columns[header[i]] = true
	}
	var missing []string
	for _, name := range format.Required {
		if !columns[name] {
			missing = append(missing, name)
		}
	}
	if len(missing) > 0 {
		return nil, nil, fmt.Errorf("unexpected file format: missing column(s) %s", strings.Join(missing, ", "))
	}

	for {
		record, err := cr.Read()
		if errors.Is(err, io.EOF) {
			return preamble, rows, nil
		}
		if err != nil {
			return nil, nil, fmt.Errorf("read row: %w", err)
		}
		line, _ := cr.FieldPos(0)
		if len(record) > len(header) {
			return nil, nil, fmt.Errorf("line %d: %d fields, but the header has only %d", line, len(record), len(header))
		}
		fields := make(map[string]string, len(header))
		empty := true
		for i, name := range header {
			if name == "" {
				continue
			}
			value := ""
			if i < len(record) {
				value = record[i]
			}
			if value != "" {
				empty = false
			}
			fields[name] = value
		}
		if empty {
			continue
		}
		rows = append(rows, RawRow{LineNo: line, Fields: fields})
	}
}

// ParseDateDE converts dd.mm.yyyy or dd.mm.yy to YYYY-MM-DD.
func ParseDateDE(s string) (string, error) {
	s = strings.TrimSpace(s)
	layout := "02.01.2006"
	if len(s) == len("02.01.06") {
		layout = "02.01.06"
	}
	t, err := time.Parse(layout, s)
	if err != nil {
		return "", fmt.Errorf("invalid date %q: want dd.mm.yyyy", s)
	}
	return t.Format(time.DateOnly), nil
}
