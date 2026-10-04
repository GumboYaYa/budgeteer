// Package store owns the SQLite database: opening it, migrating it and all
// queries.
package store

import (
	"context"
	"database/sql"
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/pressly/goose/v3"
	_ "modernc.org/sqlite"
)

//go:embed migrations/*.sql
var migrationFiles embed.FS

// ErrNotFound is returned when a requested row does not exist.
var ErrNotFound = errors.New("not found")

// pragmas are applied to every pooled connection. WAL and busy_timeout let
// `serve` and CLI commands use the database at the same time; _txlock makes
// write transactions take the lock up front instead of failing on upgrade.
const pragmas = "_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)&_pragma=foreign_keys(1)&_txlock=immediate"

type Store struct {
	DB *sql.DB
}

// Open opens the database at path, creating the file and its directory if
// needed. It does not migrate; call Migrate.
func Open(path string) (*Store, error) {
	if dir := filepath.Dir(path); dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, fmt.Errorf("store: create database directory: %w", err)
		}
	}
	db, err := sql.Open("sqlite", "file:"+path+"?"+pragmas)
	if err != nil {
		return nil, fmt.Errorf("store: open %s: %w", path, err)
	}
	if err := db.Ping(); err != nil {
		db.Close()
		return nil, fmt.Errorf("store: open %s: %w", path, err)
	}
	return &Store{DB: db}, nil
}

func (s *Store) Close() error {
	return s.DB.Close()
}

// Migrate applies all pending migrations and returns the file names of those
// it applied. It is a no-op on an up-to-date database.
func (s *Store) Migrate(ctx context.Context) ([]string, error) {
	files, err := fs.Sub(migrationFiles, "migrations")
	if err != nil {
		return nil, err
	}
	provider, err := goose.NewProvider(goose.DialectSQLite3, s.DB, files)
	if err != nil {
		return nil, fmt.Errorf("store: load migrations: %w", err)
	}
	results, err := provider.Up(ctx)
	if err != nil {
		return nil, fmt.Errorf("store: migrate: %w", err)
	}
	applied := make([]string, 0, len(results))
	for _, r := range results {
		applied = append(applied, filepath.Base(r.Source.Path))
	}
	return applied, nil
}

// SchemaVersion returns the version of the last applied migration.
func (s *Store) SchemaVersion(ctx context.Context) (int64, error) {
	files, err := fs.Sub(migrationFiles, "migrations")
	if err != nil {
		return 0, err
	}
	provider, err := goose.NewProvider(goose.DialectSQLite3, s.DB, files)
	if err != nil {
		return 0, fmt.Errorf("store: load migrations: %w", err)
	}
	return provider.GetDBVersion(ctx)
}
