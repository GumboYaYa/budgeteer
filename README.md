# Budgeteer

A local, single-user personal finance app: import bank transactions, categorize them, and analyze spending. The data lives in one SQLite file on your machine and can always be exported.

> **Status: early rewrite.** The project is being rebuilt in Go. Importing (Finanzguru and DKB), the web UI (inbox, transactions, overview, categories, import), CSV/Parquet export and backup work. The DKB importer has only been tested with made-up files so far. Automatic categorization is not built yet. See [the implementation plan](specs/implementation_plan.md) for progress.

## What it will do

- **Import** a Finanzguru export, as often as needed. Each export contains the full history; only new transactions are added.
- **Categorize** transactions by hand in an inbox that works entirely from the keyboard.
- **Analyze** with a monthly overview (income, spending per category) and a filterable transaction list.
- **Group recurring transactions** such as rent, subscriptions and insurances, see what each costs per month, and filter the transaction list by them.
- **Plan a reserve** for bills that come only once or twice a year: mark them, and Budgeteer tells you how much to move to your reserve account each month.
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
budgeteer account set-reserve --slug <slug> | --none
budgeteer import finanzguru <file> [--cutover YYYY-MM-DD] [--force]
budgeteer import dkb --account <slug> <file> [--force]
budgeteer serve [--addr localhost:8080]
budgeteer reserve
budgeteer export --format csv|parquet [--out dir]
budgeteer backup [--out dir]
```

All commands take `--db` (default `./data/budgeteer.db`).

`export` writes to `data/export/` and `backup` to `data/backups/` unless `--out` is given. The file to open in a spreadsheet is `transactions_flat.csv`: one row per transaction with account, category and tags. Its `amount` column uses a dot as decimal separator (`-9.99`); if your spreadsheet expects a comma, use `amount_cents` instead.

## Recurring transactions

Recurring transactions are collected in named groups, one per series ("Miete", "Strom", ...). Contracts that Finanzguru detects become groups when a file is imported; to fill in transactions imported earlier, import the last file once more with `--force`. Press `g` on a transaction in the inbox or the transaction list to put it into a group, create a new one by typing its name, or take it out. The transaction list has a "Recurring" filter for all groups or a single one.

The Recurring page lists the groups with their interval and cost per month, and lets you rename, merge and delete them. What you change by hand is kept by later imports, and a deleted group does not come back. A series that has ended, such as a cancelled subscription, can be switched to inactive: it keeps its transactions but no longer counts for the monthly cost, and the transaction list can show active or inactive groups only. Groups without a payment for more than two intervals are marked "ended?" as a hint. A group can be switched to "Reserve": all its transactions then count as irregular expenses for the reserve below.

## Reserve for irregular expenses

Taxes, insurances and similar bills come once or twice a year. Mark such a transaction with `r` in the inbox or the transaction list. Every bill marked in the past twelve months is expected again one year later with the same amount; a half-yearly bill is simply marked twice.

The Reserve page (and `budgeteer reserve`) shows the amount to move each month. Normally that is a twelfth of the bills of a year. If you choose the account the reserve is saved on, its balance counts as already saved, and the amount rises for as long as a bill would otherwise come due before the money is there. Everything is calculated as of the newest imported transaction.

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
make dev      # generate + css, then start the web UI from the current sources
make generate # after editing *.templ
make css      # after changing classes or assets/app.css (downloads the Tailwind CLI and DaisyUI into bin/ on first use)
```

The generated `*_templ.go` files and `internal/web/static/app.css` are committed, so `make build` needs neither tool.

Templates, CSS and scripts are compiled into the binary, so a change only shows up in the browser after a rebuild and a restart of the server. While working on the UI, use `make dev`: it regenerates everything and starts the server in one step (pass flags with `make dev ARGS="--addr localhost:8081"`). Stop it with Ctrl+C and run it again after each change.

```
specs/initial_spec.md           original spec
specs/implementation_plan.md    current plan and decisions
internal/store/migrations/      database schema
```

## Your data

Everything under `data/` (database, original import files, backups, exports) is ignored by git, as are all CSV files outside `testdata/`. Test fixtures are anonymized. Never commit real bank exports.

## License

See [LICENSE](LICENSE).
