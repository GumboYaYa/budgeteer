# Budgeteer — Implementation Plan

Derived from `specs/initial_spec.md` (the spec) and `internal/store/migrations/0001_init.sql` (the schema). Everything else in the repo is replaced.

Environment at time of writing: Go 1.27 installed; `templ`, `tailwindcss` not installed; repo still contains the old Django project.

**Scope change against the spec:** automatic categorization (`rules.yaml`, suggestions, `budgeteer categorize`) is **deferred** to a later feature. The MVP categorizes by hand in a keyboard-driven inbox. The schema keeps `allocations.source IN ('rule', 'suggested')`, `rule_id` and `confidence`, so nothing has to migrate when the feature arrives.

---

## 0. Decisions

| # | Topic | Decision | Why |
|---|---|---|---|
| D1 | Migration location | `internal/store/migrations/`, embedded by `internal/store` (`//go:embed migrations/*.sql`). | As in the spec's layout. Already moved. |
| D2 | Migration tool | **goose** (`pressly/goose/v3`) used as a library with the embedded FS. Forward-only: files get a `-- +goose Up` header and no `Down` section. | See "Migration tool" below. |
| D3 | Cut-over storage | Column `accounts.cutover_date` in `0001_init.sql`. | Already added; the migration was never applied. |
| D4 | Skipped rows and the raw layer | Rows go into `raw_records` when they are new or skipped by a filter (`Vorgemerkt`, outside cut-over). Rows whose transaction already exists are **not** stored again. The original file is always kept in `data/raw/`. | Finanzguru can only export everything, so each re-import would otherwise copy the whole history into `raw_records` again. |
| D5 | DKB dedup key account part | Account **slug**; the occurrence index `n` is counted over `Gebucht` rows only. | Slug survives a DB rebuild (ids do not); a `Vorgemerkt` twin must not shift the index. |
| D6 | Money parsing | Two explicit functions `ParseDE` / `ParseEN`, chosen by the importer. No format guessing. | `1.234` is ambiguous between the two formats. |
| D7 | Category rename vs. slug | Rename changes `name` only; `slug` stays. Move and merge do change slugs. | Slug is the stable key for the future `rules.yaml`. |
| D8 | `excluded_from_income` | **Not imported.** `categories.excluded_from_income` stays 0. | The real export shows the Finanzguru flag is not a category property: it equals the transfer flag on every row except the two split originals, and no category is excluded as a whole. `is_transfer` already carries it. |
| D9 | Transfers in DKB imports | `is_transfer = 1` automatically when the `IBAN` column equals the IBAN of another own account; everything else via `t` in the inbox. | Agreed. DKB has no transfer flag. |
| D10 | DKB CSV dialect | Comma-separated, UTF-8, RFC 4180 quoting (the file is exported from Google Sheets). The reader also accepts `;` (detected on the header line) and strips a BOM, so a direct DKB download works too. | Costs a few lines, avoids a silent failure later. |
| D11 | Small CLI addition | `account list`. | Needed to find the slugs of accounts auto-created by the Finanzguru import. |
| D12 | Frontend toolchain | No Node. `templ` via `go tool`, Tailwind standalone CLI + DaisyUI standalone plugin file, HTMX and Chart.js vendored into `internal/web/static/`, all embedded. Generated `*_templ.go` and built CSS are committed. | Keeps "single static binary" and `go build` working on a fresh clone. |
| D13 | Libraries | `alecthomas/kong`, `modernc.org/sqlite`, `pressly/goose/v3`, `a-h/templ`, `parquet-go/parquet-go`. HTTP routing with stdlib `net/http`. | Minimal deps, all pure Go. |
| D14 | Module path | `github.com/GumboYaYa/budgeteer` | Confirmed. |
| D16 | Repeated Finanzguru imports | Supported. Each export contains all transactions; rows are matched by `Buchungs-ID` (`dedup_key = fg:<id>`). New rows are inserted. For existing rows only the category is refreshed, and only while the allocation still has `source = 'finanzguru'` (or there is none); `manual` allocations are never touched. Amount, text, transfer flag and tags of existing rows are left alone. | Finanzguru stays the source of truth for what was categorized there, without overwriting work done in Budgeteer. |
| D17 | Moving the cut-over | A later Finanzguru import may pass a later `--cutover`. It is rejected if the account already has bank-CSV transactions on or before the new date. | Otherwise the same transaction would exist once from each source. |
| D18 | Finanzguru details not covered by the spec | (a) The `Tags` column is split on `,` and `;`, tags are lower-cased. (b) A category that a later export no longer contains is kept, not removed. (c) Two rows with the same `Buchungs-ID` in one file count as one transaction. (d) A cut-over date is also rejected when the account already has Finanzguru transactions after it. (e) Re-importing an identical file changes nothing, including the cut-over date. | (a) is an assumption to verify in 2.3. (d) mirrors D17: otherwise the bank CSV would import those days again. |
| D19 | `--force` and the manual transfer marker | `import finanzguru --force` processes a file even if the identical file was imported before (the existing `imports` row and raw records are reused), and overwrites the source fields of existing transactions (dates, amount, texts, references, transfer flag, tags added). Categories set by hand are kept as always. A transfer flag set by hand is kept through `transactions.is_transfer_manual` (migration `0002`), which `store.SetTransfer` sets. Without `--force`, D16 applies unchanged. | Lets an already imported file be reprocessed after the importer improves, without throwing away manual work. |
| D20 | Web UI details | (a) Besides blocking cross-origin requests, the server only answers requests whose `Host` is localhost or the `--addr` host, so a foreign domain pointed at this machine gets nothing. (b) Inbox actions are plain `fetch` calls from `app.js`; HTMX drives the filter form, pagination, import and the category page. (c) Undo is inbox-only and client-side for the row; the server reverts by clearing the category or the transfer flag. (d) The inbox shows the newest 200 uncategorized transactions and loads the next batch when they are done. (e) The overview opens on the newest month that has transactions. (f) Category move/merge do not report affected rules, since rules are deferred. | Decisions taken while building Phase 3. |
| D21 | Account balances | The transaction history does not start at zero, so a sum of transactions is not the account balance. Each account stores one known balance (`accounts.balance_cents` at the end of `accounts.balance_date`, migration `0003`), taken from the `Kontostand` of the newest imported day in a Finanzguru export. Balances for any other day are derived by taking back or adding transactions. An older file never replaces a newer known balance. The overview shows the balances at the end of the selected month (or as of the newest transaction); the monthly tile formerly called "Balance" is now "Net". The DKB importer (Phase 5) should set the balance from the file's preamble the same way. | The spec said `Kontostand` is "not stored"; without it the app cannot show a real balance. Verified on the real export: the balance column is consistent on all rows of both accounts. |
| D15 | Splits | **No split feature.** One category per transaction: the importer and the UI always write exactly one allocation for the full amount. The real export contains two old splits. Finanzguru exports a split as the original row (`Split-Typ = Original`) plus parts (`Teilbuchung`, `Restbetrag`) that add up to it; the parts are imported as transactions of their own and the original is left out (raw layer only). The import fails if parts do not add up to their original. A transaction that was imported whole and split later is replaced by its parts. | Matches Finanzguru's own figures exactly, keeps the account total right, and needs no split concept in the app. |

### Migration tool

Recommendation: **goose**.

- **goose** — one file per migration, embedded FS supported, works with `modernc.org/sqlite` (dialect `sqlite3`), tracks versions in its own table, runs each migration in a transaction. Cost: one `-- +goose Up` line per file and a larger `go.sum`.
- **golang-migrate** — forces `NNNN_name.up.sql` / `.down.sql` pairs and has no transaction per migration by default; its SQLite drivers are an extra thing to get right with a pure-Go build. More ceremony for no benefit here.
- **custom** — about 60 lines and no dependency, but we would own the edge cases (statement splitting, failed half-applied migrations, version table). Fine as a fallback, not worth it while goose fits.

---

## Phase 0 — Clean slate

- [x] Work on a branch `go-rewrite` (currently on `master`).
- [x] Delete the Django project: `apps/`, `project/`, `manage.py`, `Pipfile`, `Pipfile.lock`, old `README.md`. Keep `LICENSE`.
- [x] Replace `.gitignore` (done).
- [x] Move the migration to `internal/store/migrations/` (done).
- [x] Commit `specs/` and `internal/store/migrations/` (currently untracked).

**Done when:** `git status` is clean and the repo holds only `LICENSE`, `.gitignore`, `specs/`, `internal/store/migrations/`.

---

## Phase 1 — Foundation

Target layout (spec §8; the spec stays in `specs/`, `rules.yaml` and `internal/categorize` come later):

```
cmd/budgeteer/main.go
internal/
  store/            DB open, migrations runner, queries
    migrations/0001_init.sql
  money/
  importer/{importer.go, dkb/, finanzguru/}
  export/
  web/
testdata/   data/ (ignored)   Makefile   README.md
```

- [x] **1.1 Module & tooling.** `go mod init github.com/GumboYaYa/budgeteer`. `Makefile` targets: `build`, `test`, `vet`, `run`; `generate` (templ) and `css` (tailwind) are added in Phase 3. Short `README.md` (setup, commands).
- [x] **1.2 `internal/money`.**
  - `ParseDE(string) (int64, error)`: `-9,99`, `1.234,56`, `-57`.
  - `ParseEN(string) (int64, error)`: `-16.45`, `4,602.28`.
  - `Format(cents int64) string` → `-9.99` (for export) and `FormatDE` → `-9,99 €` (for UI).
  - Rules: optional sign, 0–2 decimals (`9,9` → 990), reject 3+ decimals, empty string, stray characters. Integer arithmetic only.
  - Table-driven tests with all five spec examples plus the error cases.
- [x] **1.3 `internal/store`.**
  - `Open(path)`: creates the parent dir, DSN with `_pragma=journal_mode(WAL)`, `_pragma=busy_timeout(5000)`, `_pragma=foreign_keys(1)` and `_txlock=immediate`, so the pragmas apply to every pooled connection.
  - Add `-- +goose Up` as the first line of `0001_init.sql`; `Migrate(db)` runs goose on the embedded FS. Idempotent.
  - Account queries: `CreateAccount`, `AccountBySlug`, `AccountByIBAN`, `ListAccounts`, `SetCutover`.
  - Tests: fresh DB migrates; second `Migrate` is a no-op; `foreign_keys` is actually on (insert with a bad FK fails); `uncategorized` view exists.
- [x] **1.4 CLI skeleton (`cmd/budgeteer`).** kong struct with global `--db` (default `./data/budgeteer.db`); commands `migrate`, `account add`, `account list`. Every command opens the store and applies pending migrations. Other commands are added in their phases.

**Done when:** `make test` passes; `budgeteer migrate && budgeteer account add --slug giro --name Giro --bank dkb && budgeteer account list` works in an empty directory.

---

## Phase 2 — Import pipeline and Finanzguru importer

Finanzguru stays the only data source until automatic categorization exists; the DKB importer is Phase 5.

- [x] **2.1 Shared pipeline (`internal/importer/importer.go`).**

  ```go
  type Parser interface {
      Source() string                                    // "finanzguru" | "dkb_csv"
      Read(r io.Reader) ([]RawRow, error)                // RawRow{LineNo, Fields map[string]string}
      Normalize(rows []RawRow, env Env) ([]Candidate, error)
  }
  type Candidate struct { Tx store.Transaction; Skip SkipReason; Extras ... } // categories, tags
  type Summary struct { Rows, New, Duplicates, Updated, SkippedStatus, SkippedCutover int; AlreadyImported bool }
  func Run(ctx, db, p Parser, file io.Reader, name string, opts Options) (Summary, error)
  ```

  `Run` does, inside **one DB transaction**:
  1. SHA-256 of the file; if present in `imports` → return `AlreadyImported`, change nothing.
  2. Copy the original to `<db dir>/raw/<sha12>_<name>`.
  3. Insert `imports` (`row_count` = all data rows in the file).
  4. Normalize; apply cut-over; look up existing `dedup_key`s. New and filtered rows: insert `raw_records` (JSON object `{header: value}`, `line_no` = physical line), then the transaction for new ones. Existing rows: count as duplicate, no raw record (D4).
  5. Source-specific extras (categories, allocations, tags), including the category refresh for existing Finanzguru rows (D16).

  Parsers are pure (no DB access) so they can be unit-tested and re-run on `raw_records` later.

- [x] **2.2 Shared helpers.** `slugify` (lowercase, `ä→ae ö→oe ü→ue ß→ss`, non-alphanumerics → `-`, collapse; `Essen & Trinken` → `essen-trinken`), date parser `dd.mm.yyyy` → ISO. Tests.

- [x] **2.3 Inspect the real Finanzguru export.** Done on the export of 2026-10-05 (5606 rows, 2 accounts, 2023-08 to 2026-10). Findings: two old splits (D15); "excluded from income" equals the transfer flag (D8); every row has a category; `Tags` holds a single multi-word tag, no separators seen; most card payments use the `VISA Debitkartenumsatz vom …` form; extra columns `Analyse-Vertragsturnus` and `Analyse-Vertrags-ID` stay in the raw layer only.
- [ ] **2.3b Booking-ID stability.** With a second export taken later: check that `Buchungs-ID` is the same for the same transaction, including transactions that were still pending in the first export. The import summary shows it: "new transactions" must equal the number of genuinely new rows.

- [x] **2.4 Finanzguru importer (`internal/importer/finanzguru`).** Column mapping exactly per spec §4.1. Specifics:
  - Accounts: match by IBAN, else create with `slug = slugify(Name Referenzkonto)`.
  - `--cutover YYYY-MM-DD`: stored in `accounts.cutover_date` for every account in the file; only rows with `booking_date <= cutover` become transactions. Without the flag: use the stored value, or import everything if none. Moving it later is checked per D17.
  - Re-import of a newer full export per D16: `Summary` reports new, duplicate and updated (category refreshed) rows.
  - `amount_cents` via `ParseEN`; `Kontostand` ignored.
  - `dedup_key = "fg:" + Buchungs-ID`, `external_id = Buchungs-ID`.
  - `purchase_date`: regex `^(\d{4}-\d{2}-\d{2})T\d{2}:\d{2} Debitk\.` on the purpose.
  - Categories: upsert main (`slug = slugify(main)`) and sub (`main/sub`, `parent_id` set); allocation for the full amount with `source = 'finanzguru'`. Rows without a category get no allocation → inbox.
  - `Analyse-Umbuchung = ja` → `is_transfer = 1`. `Analyse-Vertrag = ja` → tag `vertrag`. `Tags` column → tags.
  - Splits per D15.
  - `purchase_date` is also read from `VISA Debitkartenumsatz vom dd.mm.yyyy`, which is how most card payments appear in the real export.

- [x] **2.5 CLI.** `import finanzguru <file> [--cutover]`; prints the `Summary`.

- [x] **2.6 Fixtures & tests (`testdata/`, anonymized).**
  - `finanzguru_sample.csv`: two accounts, card payment, transfer, tagged row, contract row, e-mail as counterparty ref, uncategorized row.
  - `finanzguru_sample_v2.csv`: the same rows plus new ones, one row with a changed category, one formerly uncategorized row now categorized.
  - Tests: `finanzguru_sample` then `_v2` → only the new rows are added, `raw_records` grows only by those, the changed category is refreshed, a category set by hand in between is kept; same file twice → `AlreadyImported`, row counts unchanged; rows after the cut-over are skipped; a failing row rolls back the whole import.

- [x] **2.7 Real history import.** Run against the real export, then compare monthly sums per main category with Finanzguru (SQL query documented in the README). Fix discrepancies before moving on.

**Done when:** the real history is imported, a second full export adds only new transactions, and monthly sums match Finanzguru.

---

## Phase 3 — Web UI

- [x] **3.1 Setup.** `go get -tool github.com/a-h/templ/cmd/templ`; Tailwind standalone CLI + DaisyUI plugin file fetched by a `make tools` target into `./bin/` (ignored); HTMX and Chart.js vendored under `internal/web/static/`. `serve [--addr localhost:8080]`: stdlib mux, embedded static files, base layout with navigation. The app has no auth, so the mux is wrapped in `http.CrossOriginProtection` to stop other websites open in the browser from posting to it.
- [x] **3.2 Store functions for the UI.** Inbox page (view `uncategorized` + account), `SetCategory(txID, categoryID)`, `SetTransfer(txID, bool)` (exists since D19; it also sets the manual marker). `SetCategory` replaces the transaction's allocations with one `manual` allocation for the full amount, in one DB transaction. Transaction search (account, date range, category, text; paginated), month aggregates, category tree.
- [x] **3.3 Inbox (`/inbox`) — manual categorization, keyboard only.** Rows newest first: date, account, counterparty, purpose, amount. HTMX posts return an empty row (swap out) and focus moves to the next row.
  - `j` / `k` move focus.
  - `c` or `Enter` opens the category picker: fuzzy search over `name` + `slug`, client-side over the full list (it is small); the most recently used categories are listed first; `Enter` confirms.
  - `x` toggles selection of a row; with a selection, `c` applies one category to all selected rows.
  - `t` mark as transfer.
  - `u` undo the last action (re-inserts the row).
  - One small vanilla JS file for key handling; shortcuts are off while an input has focus. A `?` overlay lists the keys.
- [x] **3.4 Transactions (`/transactions`).** Filter form (HTMX, URL-backed so filters are bookmarkable), category edit reusing the inbox picker.
- [x] **3.5 Overview (`/`).** Month selector; income, spending per main category (bar chart), 12-month trend, uncategorized count linking to the inbox. Transfers and `excluded_from_income` categories are left out of the income and spending figures.
- [x] **3.6 Import (`/import`).** Upload a Finanzguru export (optional cut-over date) → `importer.Run` → summary (new / duplicates / updated / skipped), with a link to the inbox.
- [x] **3.7 Categories (`/categories`).** Tree view; add, rename, move, merge per D7. Two-level limit enforced in code. (In spec §7 but missing from the spec's phase checklist.)
- [x] **3.8 Tests.** `httptest` handler tests for every mutating endpoint against a temp DB (set category, bulk set, transfer, undo, upload, category merge). Manual keyboard walkthrough of the inbox in a browser.

**Done when:** a new Finanzguru export can be uploaded and its inbox emptied without touching the mouse.

---

## Phase 4 — Export & ops

- [ ] **4.1 `export --format csv|parquet [--out dir]`** (`internal/export`). Writes one file per table plus `transactions_flat`: one row per transaction (empty category when uncategorized) with account slug, dates, counterparty, purpose, amount, main category, sub category, source, tags (`;`-joined), `is_transfer`. Amounts as decimal with dot (`-9.99`) and additionally as cents. CSV: UTF-8, comma, RFC 4180 quoting. Parquet: same datasets, amounts as INT64 cents + DECIMAL(18,2). Default output `data/export/`.
- [ ] **4.2 `backup [--out dir]`**: `VACUUM INTO '<out>/budgeteer-YYYYMMDD-HHMMSS.db'`, default `data/backups/`.
- [ ] **4.3 Tests.** Export of the fixture DB: sum of `transactions_flat` amounts equals the DB sum; CSV round-trips through `encoding/csv`; Parquet file is readable back; backup opens and passes `PRAGMA integrity_check`.
- [ ] **4.4 Optional:** `docker-compose.yml` with Metabase and a read-only mount of `data/`.

**Done when:** the exported CSV opens in a spreadsheet with correct amounts and categories.

---

## Phase 5 — DKB importer

Needed for the switch from Finanzguru to DKB-only; can wait until then. Deduplication is built in: the same file is skipped by checksum, and overlapping exports are matched row by row through the dedup key.

- [ ] **5.1 DKB importer (`internal/importer/dkb`).** Plugs into the pipeline from 2.1. Per spec §4.2. Specifics:
  - Dialect per D10. Skip 4 preamble lines, header on line 5; fail with a clear message if expected header columns are missing.
  - Only `Status = Gebucht` becomes a transaction.
  - Counterparty: `Ausgang` → `Zahlungsempfänger*in`, `Eingang` → `Zahlungspflichtige*r`. `counterparty_ref` = `IBAN`, `value_date` = `Wertstellung`, `customer_ref` = `Kundenreferenz`.
  - `amount_cents` via `ParseDE` from `Betrag (€)`; dates `dd.mm.yy`.
  - `purchase_date`: regex `VISA Debitkartenumsatz vom (\d{2}\.\d{2}\.\d{4})`.
  - Zero-amount statement rows are imported as-is; the `uncategorized` view already hides them (0 = 0).
  - Dedup key per spec and D5. Cut-over: only `booking_date > accounts.cutover_date`.
  - Transfer detection per D9.
  - Date parser `dd.mm.yy` → ISO.
- [ ] **5.2 CLI and UI.** `import dkb --account <slug> <file>`; the `/import` page gets a source and account select.
- [ ] **5.3 Fixtures & tests (`testdata/`, anonymized, cut from a real Google Sheets export).** `dkb_a.csv`, `dkb_b.csv` with overlapping date ranges; include `Vorgemerkt`, a statement row, two identical transactions on one day, a transfer to the second own account, amounts `-9,99` / `1.234,56` / `-57`. One extra `;`-separated variant. Tests: same file twice → `AlreadyImported`; `dkb_a` then `dkb_b` → no duplicates and both identical same-day rows present; a `Vorgemerkt` row in A that is `Gebucht` in B is imported exactly once; rows on or before the cut-over are skipped; a later Finanzguru `--cutover` is rejected when DKB rows exist before it (D17).
- [ ] **5.4 Switch-over run.** Add `account set-cutover --slug <slug> --date <date>` with the checks from D17/D18, so the date can be set without a new export file. Last Finanzguru import with `--cutover <date>`, then the first real DKB export; check that the days around the cut-over contain every transaction exactly once.

**Done when:** two overlapping real DKB exports import without duplicates.

---

## Later — Automatic categorization (deferred)

Spec §5, unchanged in design: `rules.yaml` loader and matcher (first match wins, never overwrites `manual`), counterparty-history suggestions, `budgeteer categorize [--dry-run]`, inbox additions (`Enter` accepts a suggestion, `r` creates a rule from a row), auto-categorized count in the import summary. Further items from the spec's "Later" list stay there (more banks, FinTS, budgets, contract detection).

---

## Acceptance mapping (spec §10)

| Criterion | Verified by |
|---|---|
| Same file twice changes nothing | Tests in 2.6 and 5.3 (`AlreadyImported`) |
| A newer full Finanzguru export adds only the new transactions | Test in 2.6 (`finanzguru_sample_v2`) |
| Overlapping DKB exports, no duplicates | Test in 5.3 (`dkb_a` + `dkb_b`) |
| Monthly spending per main category matches Finanzguru | Manual check in 2.7 |
| ≥ 70 % of a new DKB export auto-categorized | **Deferred** with automatic categorization |
| Inbox processable without the mouse | Walkthrough in 3.8 |
| CSV export opens correctly in a spreadsheet | 4.3 + manual check |

## Risks

- **Repeated Finanzguru imports rely on a stable `Buchungs-ID`.** If Finanzguru changes the ID of a transaction between exports (possible for pending ones), it would be imported twice. Checked in 2.3 with two real exports.
- **DKB CSV format drift.** DKB has changed its export format before, and the file passes through Google Sheets; the header check in 5.1 turns a changed format into a clear error instead of wrong data.
- **Google Sheets may reformat values** (the bare `-57` in the spec is such a case). Dates or long numeric references could be affected as well; the fixtures in 5.3 are cut from a real Sheets export to catch this.

## Order and size

Phases are sequential; each ends with passing tests. Rough relative effort: Phase 0–1 small, Phase 2 medium (real data), Phase 3 largest (inbox is the bulk), Phase 4 small, Phase 5 medium.
