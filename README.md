# Budgeteer

A local, single-user personal finance app: import bank transactions, categorize them, and analyze spending. The data lives in one SQLite file on your machine and can always be exported.

> **Status: early rewrite.** The project is being rebuilt in Go. So far `migrate`, `account add`, `account list` and `import finanzguru` work; the web UI and export are not built yet. See [the implementation plan](specs/implementation_plan.md) for progress.

## What it will do

- **Import** a Finanzguru export, as often as needed. Each export contains the full history; only new transactions are added.
- **Categorize** transactions by hand in an inbox that works entirely from the keyboard.
- **Analyze** with a monthly overview (income, spending per category) and a filterable transaction list.
- **Export** everything as CSV or Parquet, and back up the database with one command.

Planned for later: a DKB CSV importer, and automatic categorization through rules and suggestions.

## How it is built

| | |
|---|---|
| Language | Go, one static binary with subcommands |
| Database | SQLite (pure Go driver, no CGO) |
| Web UI | templ + HTMX + DaisyUI, server-rendered |
| Runs on | localhost only, no accounts, no cloud |

## Usage (planned)

```
budgeteer migrate
budgeteer account add --slug <slug> --name <name> [--iban <iban>] [--bank dkb]
budgeteer account list
budgeteer import finanzguru <file> [--cutover YYYY-MM-DD]
budgeteer serve [--addr localhost:8080]
budgeteer export --format csv|parquet [--out dir]
budgeteer backup [--out dir]
```

All commands take `--db` (default `./data/budgeteer.db`).

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
```

```
specs/initial_spec.md           original spec
specs/implementation_plan.md    current plan and decisions
internal/store/migrations/      database schema
```

## Your data

Everything under `data/` (database, original import files, backups, exports) is ignored by git, as are all CSV files outside `testdata/`. Test fixtures are anonymized. Never commit real bank exports.

## License

See [LICENSE](LICENSE).
