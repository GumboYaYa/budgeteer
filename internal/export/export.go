// Package export writes the database out as CSV or Parquet files, so the data
// stays usable without this app.
package export

import (
	"context"
	"encoding/csv"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strconv"

	"github.com/parquet-go/parquet-go"

	"github.com/GumboYaYa/budgeteer/internal/money"
	"github.com/GumboYaYa/budgeteer/internal/store"
)

const (
	CSV     = "csv"
	Parquet = "parquet"
)

// File describes one written file.
type File struct {
	Path string
	Rows int
}

// Run writes every dataset into dir, one file each, replacing files of the
// same name. Files are written completely or not at all.
func Run(ctx context.Context, st *store.Store, dir, format string) ([]File, error) {
	var write func(path string, d store.Dataset) error
	switch format {
	case CSV:
		write = writeCSV
	case Parquet:
		write = writeParquet
	default:
		return nil, fmt.Errorf("unknown export format %q: use csv or parquet", format)
	}

	datasets, err := st.ExportDatasets(ctx)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("export: %w", err)
	}
	var files []File
	for _, d := range datasets {
		path := filepath.Join(dir, d.Name+"."+format)
		tmp := path + ".tmp"
		if err := write(tmp, d); err != nil {
			os.Remove(tmp)
			return nil, fmt.Errorf("export %s: %w", d.Name, err)
		}
		if err := os.Rename(tmp, path); err != nil {
			os.Remove(tmp)
			return nil, fmt.Errorf("export %s: %w", d.Name, err)
		}
		files = append(files, File{Path: path, Rows: len(d.Rows)})
	}
	return files, nil
}

// writeCSV writes UTF-8, comma-separated, RFC 4180 quoted. NULL becomes an
// empty field; amounts are plain decimals with a dot ("-9.99").
func writeCSV(path string, d store.Dataset) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()

	w := csv.NewWriter(f)
	record := make([]string, len(d.Columns))
	for i, col := range d.Columns {
		record[i] = col.Name
	}
	if err := w.Write(record); err != nil {
		return err
	}
	for _, row := range d.Rows {
		for i, value := range row {
			record[i] = csvField(d.Columns[i].Kind, value)
		}
		if err := w.Write(record); err != nil {
			return err
		}
	}
	w.Flush()
	if err := w.Error(); err != nil {
		return err
	}
	return f.Close()
}

func csvField(kind store.Kind, value any) string {
	switch v := value.(type) {
	case nil:
		return ""
	case int64:
		if kind == store.KindCents {
			return money.Format(v)
		}
		return strconv.FormatInt(v, 10)
	case float64:
		return strconv.FormatFloat(v, 'f', -1, 64)
	case string:
		return v
	}
	return fmt.Sprint(value)
}

// writeParquet writes one row group with all columns optional. Amounts are
// DECIMAL(18,2) backed by the int64 cents, so no precision is lost.
func writeParquet(path string, d store.Dataset) error {
	// parquet-go derives the schema from a struct type; build one that
	// matches the dataset so the column order is kept.
	fields := make([]reflect.StructField, len(d.Columns))
	for i, col := range d.Columns {
		tag := col.Name
		var typ reflect.Type
		switch col.Kind {
		case store.KindInt:
			typ = reflect.TypeFor[*int64]()
		case store.KindCents:
			typ = reflect.TypeFor[*int64]()
			tag += ",decimal(2:18)"
		case store.KindFloat:
			typ = reflect.TypeFor[*float64]()
		default:
			typ = reflect.TypeFor[*string]()
		}
		fields[i] = reflect.StructField{
			Name: "F" + strconv.Itoa(i),
			Type: typ,
			Tag:  reflect.StructTag(`parquet:` + strconv.Quote(tag)),
		}
	}
	rowType := reflect.StructOf(fields)

	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()

	w := parquet.NewWriter(f, parquet.SchemaOf(reflect.New(rowType).Interface()))
	for _, row := range d.Rows {
		record := reflect.New(rowType)
		for i, value := range row {
			field := record.Elem().Field(i)
			switch v := value.(type) {
			case int64:
				field.Set(reflect.ValueOf(&v))
			case float64:
				field.Set(reflect.ValueOf(&v))
			case string:
				field.Set(reflect.ValueOf(&v))
			}
		}
		if err := w.Write(record.Interface()); err != nil {
			return err
		}
	}
	if err := w.Close(); err != nil {
		return err
	}
	return f.Close()
}
