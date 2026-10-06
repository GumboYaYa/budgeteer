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

// ReadCSV reads a comma-separated file whose first line is the header and
// returns one RawRow per data row. A UTF-8 byte order mark and completely
// empty rows are ignored. Every header in required must be present.
func ReadCSV(r io.Reader, required []string) ([]RawRow, error) {
	br := bufio.NewReader(r)
	if bom, err := br.Peek(3); err == nil && string(bom) == "\xEF\xBB\xBF" {
		br.Discard(3)
	}
	cr := csv.NewReader(br)
	cr.FieldsPerRecord = -1

	header, err := cr.Read()
	if errors.Is(err, io.EOF) {
		return nil, errors.New("file is empty")
	}
	if err != nil {
		return nil, fmt.Errorf("read header: %w", err)
	}
	columns := make(map[string]bool, len(header))
	for i, h := range header {
		header[i] = strings.TrimSpace(h)
		columns[header[i]] = true
	}
	var missing []string
	for _, name := range required {
		if !columns[name] {
			missing = append(missing, name)
		}
	}
	if len(missing) > 0 {
		return nil, fmt.Errorf("unexpected file format: missing column(s) %s", strings.Join(missing, ", "))
	}

	var rows []RawRow
	for {
		record, err := cr.Read()
		if errors.Is(err, io.EOF) {
			return rows, nil
		}
		if err != nil {
			return nil, fmt.Errorf("read row: %w", err)
		}
		line, _ := cr.FieldPos(0)
		if len(record) > len(header) {
			return nil, fmt.Errorf("line %d: %d fields, but the header has only %d", line, len(record), len(header))
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

// ParseDateDE converts dd.mm.yyyy to YYYY-MM-DD.
func ParseDateDE(s string) (string, error) {
	t, err := time.Parse("02.01.2006", strings.TrimSpace(s))
	if err != nil {
		return "", fmt.Errorf("invalid date %q: want dd.mm.yyyy", s)
	}
	return t.Format(time.DateOnly), nil
}
