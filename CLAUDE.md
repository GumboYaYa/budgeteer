# Budgeteer

Local, single-user personal finance app in Go: import bank transactions (Finanzguru export now, DKB CSV later), categorize them in a keyboard-driven web inbox, analyze and export.

## Where things are decided

- `specs/initial_spec.md` — the original spec. Do not edit it.
- `specs/implementation_plan.md` — the current plan. It **overrides the spec** where they differ (see its "Decisions" table). Work phase by phase, tick the checkboxes as steps are finished, and record new decisions there.
- `internal/store/migrations/` — the schema.

## Scope decisions that differ from the spec

- No split transactions: exactly one allocation per transaction, for the full amount. The `allocations` table stays.
- Automatic categorization (`rules.yaml`, suggestions, `categorize` command) is deferred. Categorization is manual in the inbox. Do not build it unless asked.
- Finanzguru is the only data source for now and is re-imported repeatedly as a full export. The DKB importer is the last phase.

## Layout

```
cmd/budgeteer/      kong CLI wiring only, no logic
internal/store/     DB open, migrations, queries
internal/money/     cents parsing and formatting
internal/importer/  shared pipeline + one package per source
internal/slug/      slugs for accounts and categories
internal/export/    CSV / Parquet
internal/web/       handlers, templ components, embedded static assets
testdata/           anonymized fixtures
data/               local DB, raw imports, backups (ignored)
```

CLI commands and web handlers are thin wrappers; logic lives in `internal/` so both use the same functions.

## Conventions

- **Money** is `int64` cents, negative = outflow. Never floats. Parse with `money.ParseDE` / `money.ParseEN`, chosen explicitly by the caller; never guess the format.
- **Dates** are ISO `YYYY-MM-DD` text; timestamps UTC `YYYY-MM-DDTHH:MM:SSZ`.
- **SQLite** via `modernc.org/sqlite` (pure Go, no CGO). Every connection uses `journal_mode=WAL`, `busy_timeout=5000`, `foreign_keys=ON`.
- **Migrations** use goose with embedded SQL files, forward-only. Once a migration has been applied to a real database, never edit it; add a new file.
- **Imports** are idempotent: a file is identified by its SHA-256, a row by its `dedup_key`. An import runs in one DB transaction. Parsers are pure functions without DB access.
- **Manual categorization is never overwritten** by an import.
- **Frontend**: templ + HTMX + DaisyUI, server-rendered. No Node toolchain. Generated `*_templ.go` and built CSS are committed.
- **Dependencies**: keep them minimal and pure Go. Use the standard library for HTTP routing.
- **Tests**: table-driven; DB tests run against a temporary database file. Each phase ends with `go test ./...` passing.

## Privacy

This repo handles real bank data. Never commit `data/`, a database file, or a real CSV export. Fixtures in `testdata/` must be anonymized: invented names, IBANs and references. Do not paste real transaction rows into code, tests, commit messages or docs.

## Commands

```
make build      # build ./budgeteer
make test       # go test ./...
make vet
make run ARGS="account list"
make generate   # templ generate (from Phase 3)
make css        # tailwind build (from Phase 3)
```

## Git

Development happens on `go-rewrite`; `master` still holds the old Django project.
