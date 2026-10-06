# Budgeteer

A local, single-user personal finance app: import bank transactions, categorize them, and analyze spending. The data lives in one SQLite file on your machine and can always be exported.

> **Status: early rewrite.** The project is being rebuilt in Go. Importing (Finanzguru and DKB), the web UI (inbox, transactions, overview, categories, import), CSV/Parquet export and backup work. The DKB importer has only been tested with made-up files so far. Automatic categorization is not built yet. See [the implementation plan](specs/implementation_plan.md) for progress.

## What it will do

- **Import** a Finanzguru export, as often as needed. Each export contains the full history; only new transactions are added.
- **Categorize** transactions by hand in an inbox that works entirely from the keyboard.
- **Analyze** with a monthly overview (income, spending per category) and a filterable transaction list.
- **Export** everything as CSV or Parquet, and back up the database with one command.

It also imports DKB account exports, for the time after Finanzguru. Planned for later: automatic categorization through rules and suggestions.

## How it is built

| | |
|---|---|
| Language | Go, one static binary with subcommands |
| Database | SQLite (pure Go driver, no CGO) |
| Web UI | templ + HTMX + DaisyUI, server-rendered |
| Runs on | localhost only, no accounts, no cloud |

## Usage

```
budgeteer migrate
budgeteer account add --slug <slug> --name <name> [--iban <iban>] [--bank dkb]
budgeteer account list
budgeteer account set-cutover --slug <slug> --date YYYY-MM-DD
budgeteer import finanzguru <file> [--cutover YYYY-MM-DD] [--force]
budgeteer import dkb --account <slug> <file> [--force]
budgeteer serve [--addr localhost:8080]
budgeteer export --format csv|parquet [--out dir]
budgeteer backup [--out dir]
```

All commands take `--db` (default `./data/budgeteer.db`).

`export` writes to `data/export/` and `backup` to `data/backups/` unless `--out` is given. The file to open in a spreadsheet is `transactions_flat.csv`: one row per transaction with account, category and tags. Its `amount` column uses a dot as decimal separator (`-9.99`); if your spreadsheet expects a comma, use `amount_cents` instead.

## Switching an account from Finanzguru to DKB

Finanzguru and DKB describe the same transaction differently, so they are not matched row by row. Instead each account gets a cut-over date: Finanzguru provides everything up to and including that day, DKB everything after it.

```
budgeteer import finanzguru data/finanzguru.csv          # one last full export
budgeteer account set-cutover --slug <slug> --date 2026-10-05   # the last day it covers
budgeteer import dkb --account <slug> data/dkb.csv       # may start earlier; older rows are skipped
```

DKB exports may overlap each other. Bookings that are still pending (`Vorgemerkt`) are left out and come with a later export.

## Querying the database

The database is a plain SQLite file. It is created at `data/budgeteer.db`, relative to the directory you run `budgeteer` from, on the first command. To pin it to one place, pass `--db` or set `BUDGETEER_DB`:

```
export BUDGETEER_DB=/path/to/budgeteer/data/budgeteer.db
```

Open an interactive session with the `sqlite3` tool (ships with macOS):

```
sqlite3 data/budgeteer.db
```

```
.tables                  -- list tables and views
.schema accounts         -- show how a table is defined
.mode box                -- readable table output
.headers on
SELECT * FROM accounts;
.quit
```

Or run a single query from the shell:

```
sqlite3 -box data/budgeteer.db "SELECT slug, name, iban FROM accounts"
```

Amounts are stored in cents; divide to get euros:

```sql
SELECT booking_date, counterparty, amount_cents / 100.0 AS eur FROM transactions;
```

Querying while `budgeteer` is running is safe.

## Development

Requires Go 1.27 or newer.

```
make build    # builds ./budgeteer
make test     # go test ./...
make vet
make run ARGS="account list"
make generate # after editing *.templ
make css      # after changing classes or assets/app.css (downloads the Tailwind CLI and DaisyUI into bin/ on first use)
```

The generated `*_templ.go` files and `internal/web/static/app.css` are committed, so `make build` needs neither tool.

```
specs/initial_spec.md           original spec
specs/implementation_plan.md    current plan and decisions
internal/store/migrations/      database schema
```

## Your data

Everything under `data/` (database, original import files, backups, exports) is ignored by git, as are all CSV files outside `testdata/`. Test fixtures are anonymized. Never commit real bank exports.

## License

See [LICENSE](LICENSE).
