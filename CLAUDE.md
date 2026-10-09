# Budgeteer

Local, single-user personal finance app in Go: import bank transactions (Finanzguru export now, DKB CSV later), categorize them in a keyboard-driven web inbox, analyze and export.

## Where things are decided

- `specs/initial_spec.md` — the original spec. Do not edit it.
- `specs/implementation_plan.md` — the current plan. It **overrides the spec** where they differ (see its "Decisions" table). Work phase by phase, tick the checkboxes as steps are finished, and record new decisions there.
- `internal/store/migrations/` — the schema.

## Scope decisions that differ from the spec

- No split transactions: exactly one allocation per transaction, for the full amount. The `allocations` table stays.
- Automatic categorization (`rules.yaml`, suggestions, `categorize` command) is deferred. Categorization is manual in the inbox. Do not build it unless asked.
- Finanzguru is the main data source for now and is re-imported repeatedly as a full export. The DKB importer exists for the later switch; an account's cut-over date decides which source owns which days.

## Layout

```
cmd/budgeteer/      kong CLI wiring only, no logic
internal/store/     DB open, migrations, queries
internal/money/     cents parsing and formatting
internal/importer/  shared pipeline + one package per source
internal/slug/      slugs for accounts and categories
internal/reserve/   monthly amount for irregular expenses
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
- **Manual work is never overwritten** by an import, not even with `--force`: allocations with `source = 'manual'`, transfer flags with `is_transfer_manual = 1`, reserve marks (`is_reserve`), recurring groups chosen by hand (`recurring_manual = 1`) and the review of a recurring group (`mandatory`, `verdict`). Change the transfer flag only through `store.SetTransfer` and a transaction's group only through `store.SetRecurringGroup` (by hand) or `store.AssignContractGroup` (imports).
- **Recurring groups**: a monthly group cannot be covered by the reserve, and a mandatory group cannot have the verdict `cancel`; the setters in `internal/store/recurring.go` enforce both, so change these fields only through them. "Running expense" (active, known interval, newest transaction an outflow) is decided in one place, `runningExpense` in `internal/web/viewmodels.go`.
- **Frontend**: templ + HTMX + DaisyUI, server-rendered. No Node toolchain. Generated `*_templ.go` and built CSS are committed; rerun `make generate` and `make css` after template changes. In `.templ` files, a text line must not start with `if`, `for` or `switch` (templ parses it as a statement). Render amounts of income or spending with the `amount` component in `pages.templ`, which shows money coming in green; balances and the positive figures of the Reserve page stay plain. HTMX swaps a section with `outerHTML`; scroll anchoring is switched off in `assets/app.css` so that does not move the page. Keyboard handling lives in `internal/web/static/app.js`; verify changes to it in a real browser, the Go tests do not cover it.
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
make dev        # generate + css, then serve from the current sources
make generate   # templ generate, after editing *.templ
make css        # tailwind build, after changing classes or assets/app.css
```

## Git

`master` holds the Go project (the `go-rewrite` branch was merged and deleted). Work on a new feature branch off `master`.
