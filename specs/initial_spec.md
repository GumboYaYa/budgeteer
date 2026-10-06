# Budgeteer — Spec & Implementation Plan

Personal finance app to replace Finanzguru: import bank transactions, categorize them (largely automatically), and analyze spending — desktop-first, local, with fully owned and exportable data.

This file is the handover from the design discussion. Start here; `migrations/0001_init.sql` holds the agreed initial schema.

---

## 1. Goals & non-goals

**Goals**

- Import 3 years of Finanzguru history (5000+ categorized transactions) once, then switch to bank CSV exports.
- Show uncategorized transactions in an inbox and make categorizing them fast (keyboard-driven).
- Categorize most new transactions automatically via rules, with suggestions for the rest.
- Data is always exportable (CSV/Parquet) and readable by any tool — no lock-in, not even to this app.
- Easy to extend: new banks, new views, new analyses.

**Non-goals (for now)**

- Multi-user, auth, cloud hosting, mobile app.
- Automatic bank sync (FinTS/HBCI) — possible later as another importer.
- "Optimization" recommendations — analysis is done by the user (dashboards, SQL, notebooks).

---

## 2. Architecture decisions

| Decision | Choice | Reason |
|---|---|---|
| Language | Go | Single static binary, user's main backend language |
| Deployment | Localhost only (optionally Tailscale later) | Sensitive data, no ops, no cost |
| Binary layout | **One binary**, subcommands | One version, one schema, shared code |
| CLI framework | **kong** | Declarative struct-based, minimal deps |
| Database | **SQLite** via `modernc.org/sqlite` (pure Go, no CGO) | Single file, universally readable |
| Migrations | Embedded SQL files (`goose` or `golang-migrate`) | Schema versioned in git |
| UI | **templ + HTMX + DaisyUI** (Tailwind) | Server-rendered, minimal JS |
| Dashboards | Optional, external: **Metabase** (AGPL OSS) or **Evidence** (MIT) on the same SQLite file | No need to build exploratory BI |
| Categorization rules | `rules.yaml` in git | Readable, versioned, editable outside the app |

**SQLite connection pragmas (always):**

```
PRAGMA journal_mode = WAL;
PRAGMA busy_timeout = 5000;
PRAGMA foreign_keys = ON;
```

WAL + busy_timeout lets `budgeteer serve` and CLI commands (e.g. `import`) use the DB concurrently.

---

## 3. Data model — three layers

1. **Raw (immutable):** every imported file is recorded in `imports` (with SHA-256, so a file is never imported twice); each row is stored verbatim as JSON in `raw_records`. Original files are also kept on disk in `data/raw/`. Everything downstream can be rebuilt from this layer.
2. **Normalized:** `transactions` — one schema for all sources.
3. **Categorization:** `categories` (two-level tree), `allocations` (transaction → category with amount; multiple rows = split), `tags`. Categorization survives re-imports and re-parsing.

See `migrations/0001_init.sql` for the full DDL. Key conventions:

- Money: **INTEGER cents**, negative = outflow. Never floats.
- Dates: ISO `YYYY-MM-DD` TEXT; timestamps UTC `YYYY-MM-DDTHH:MM:SSZ`.
- `counterparty_ref` is free text — it can be an IBAN, a plain account number, or an e-mail address.
- `allocations.source` ∈ `finanzguru | rule | manual | suggested`. Only non-`suggested` allocations count as confirmed.
- A transaction is **categorized** when its confirmed allocations sum to its amount (view `uncategorized`). Transfers between own accounts (`is_transfer = 1`) are excluded. Zero-amount info rows never appear in the inbox.
- `categories.slug` (e.g. `essen-trinken/lebensmittel`) is the stable key referenced by `rules.yaml`.

**Open question:** splits are supported from day one. If the Finanzguru history contains no splits (`Split-Typ` empty everywhere), consider whether to keep this or simplify to one category per transaction. Keeping it is cheap; removing it later is not.

---

## 4. Import sources

### 4.1 Finanzguru export (one-time history import)

- Header in line 1, comma-separated, UTF-8.
- Dates: `dd.mm.yyyy`. `Betrag`: dot decimal (`-16.45`). `Kontostand`: English thousands format (`4,602.28`) — not stored.
- One file can contain multiple accounts (`Referenzkonto` = IBAN, `Name Referenzkonto`) → create/match `accounts` by IBAN.

| Finanzguru column | Target |
|---|---|
| Buchungstag | `booking_date` |
| Referenzkonto / Name Referenzkonto | `accounts.iban` / `accounts.name` |
| Betrag | `amount_cents` |
| Waehrung | `currency` |
| Beguenstigter/Auftraggeber | `counterparty` |
| IBAN Beguenstigter/Auftraggeber | `counterparty_ref` (may be e-mail) |
| Verwendungszweck | `purpose` |
| E-Ref / Mandatsreferenz / Glaeubiger-ID | `end_to_end_ref` / `mandate_ref` / `creditor_id` |
| Analyse-Hauptkategorie + Analyse-Unterkategorie | `categories` (create tree), `allocations` with `source = 'finanzguru'` |
| Analyse-Umbuchung (`ja`/`nein`) | `is_transfer` |
| Analyse-Vom frei verfuegbaren Einkommen ausgeschlossen | `categories.excluded_from_income` |
| Analyse-Vertrag (`ja`) | tag `vertrag` |
| Tags | `tags` |
| Buchungs-ID | `external_id`; dedup key = `fg:<Buchungs-ID>` |
| Referenz-Original-ID / Split-Typ | split handling → multiple `allocations` (inspect real data first) |
| Analyse-Woche/-Monat/-Quartal/-Jahr, Analyse-Betrag, Analyse-Umsatzart | not stored (derivable) |

Card payments: purpose looks like `2026-10-03T05:07 Debitk. … (POS|ECOM)` → parse the timestamp's date into `purchase_date`.

### 4.2 DKB CSV export (ongoing)

- **4 preamble lines** before the header: account name + (masked) IBAN, empty, `Kontostand vom dd.mm.yyyy:` + balance, empty. Header is line 5.
- Columns: `Buchungsdatum, Wertstellung, Status, Zahlungspflichtige*r, Zahlungsempfänger*in, Verwendungszweck, Umsatztyp, IBAN, Betrag (€), Gläubiger-ID, Mandatsreferenz, Kundenreferenz`.
- Dates: `dd.mm.yy`. Amounts: German format (`-9,99`, `1.234,56`, also bare `-57`).
- `Status`: import only `Gebucht`; skip `Vorgemerkt` (text can change before booking → duplicates).
- Counterparty: `Umsatztyp = Ausgang` → `Zahlungsempfänger*in`; `Eingang` → `Zahlungspflichtige*r`.
- Card payments: purpose `VISA Debitkartenumsatz vom dd.mm.yyyy` → `purchase_date`. The `IBAN` column then holds the card issuer's clearing IBAN, not the merchant → rules for card payments must match on counterparty name.
- Statement rows (`Abrechnung …`, amount `0`, `IBAN` = account number) are imported but never show up in the inbox.
- Account is passed explicitly: `budgeteer import dkb --account <slug> file.csv` (the IBAN in the preamble may be masked).
- Dedup key: `dkb:` + SHA-256 of `account | booking_date | amount_cents | counterparty | purpose | customer_ref | n`, where `n` is the occurrence index of identical tuples within the file. Overlapping export ranges thus import safely.

### 4.3 Cut-over between sources

Finanzguru and the DKB CSV describe the same transaction differently (other purpose text, counterparty names, even dates). Do **not** try to match them row by row. Instead, use a **cut-over date**:

- Finanzguru import: only rows with `booking_date <= cutover`.
- Bank CSV import: only rows with `booking_date > cutover`.
- Store the cut-over date per account (config or a `settings` table) and enforce it in the importers.

---

## 5. Categorization

**Order of application for a new transaction:**

1. **Rules** (`rules.yaml`) — first match wins → allocation with `source = 'rule'`, `rule_id`.
2. **Suggestions** — for unmatched transactions, suggest a category from history (start simple: most frequent category for the same normalized counterparty; later: text similarity / small classifier / LLM) → `source = 'suggested'`, `confidence`.
3. **Inbox** — user confirms or changes; optionally "create rule from this".

**`rules.yaml` sketch:**

```yaml
rules:
  - id: aldi
    match:
      counterparty: "(?i)^aldi"
    category: essen-trinken/lebensmittel

  - id: rundfunk
    match:
      creditor_id: "DE3000100000001272"
    category: wohnen/rundfunk

  - id: steam
    match:
      counterparty: "(?i)steam"
      amount_max: 0          # outflows only
    category: freizeit/gaming
    tags: [gaming]
```

Matchable fields: `counterparty`, `counterparty_ref`, `purpose` (regex), `creditor_id`, `mandate_ref` (exact), `amount_min`/`amount_max`, `account`. Rules never overwrite `manual` allocations.

---

## 6. CLI

Single binary `budgeteer`, kong-based. Global flag `--db` (default `./data/budgeteer.db`).

```
budgeteer serve [--addr localhost:8080]
budgeteer migrate
budgeteer account add --slug <slug> --name <name> [--iban <iban>] [--bank dkb]
budgeteer import finanzguru <file> [--cutover YYYY-MM-DD]
budgeteer import dkb --account <slug> <file>
budgeteer categorize [--dry-run]        # apply rules.yaml + suggestions
budgeteer export --format csv|parquet [--out dir]
budgeteer backup [--out dir]            # consistent copy via VACUUM INTO
```

Commands are thin wrappers; all logic lives in `internal/` so the web UI can call the same functions.

---

## 7. Web UI (MVP)

- **Inbox** (`/inbox`): uncategorized transactions, newest first. Per row: date, counterparty, purpose, amount, suggestion. Keyboard: `j/k` move, `Enter` accept suggestion, `c` category picker (fuzzy search), `r` create rule from row, `s` split, `t` mark as transfer. HTMX swaps the row out on confirm.
- **Transactions** (`/transactions`): filter by account, date range, category, text; edit categories.
- **Overview** (`/`): month view — income, spending by main category, uncategorized count. A few built-in charts (ECharts or Chart.js).
- **Categories** (`/categories`): manage tree (rename, move, merge).
- **Import** (`/import`): upload a file → same importer as the CLI → show summary (new / skipped duplicates / auto-categorized).

---

## 8. Repository layout

```
budgeteer/
├── cmd/budgeteer/main.go          # kong CLI wiring
├── internal/
│   ├── store/                     # DB open (pragmas), queries, migrations runner
│   │   └── migrations/0001_init.sql
│   ├── money/                     # cents parsing: German + English formats
│   ├── importer/
│   │   ├── importer.go            # interface, raw layer, dedup, cutover
│   │   ├── dkb/
│   │   └── finanzguru/
│   ├── categorize/                # rules engine, suggestions
│   ├── export/
│   └── web/                       # handlers, templ components, static assets
├── rules.yaml
├── testdata/                      # anonymized sample CSVs
├── data/                          # .gitignored: budgeteer.db, raw/, backups/
├── .gitignore
├── Makefile                       # build, test, templ generate, tailwind
└── SPEC.md
```

**Never commit:** `data/`, real CSV exports, the DB file. Test fixtures in `testdata/` must be anonymized (fake names, IBANs, references).

---

## 9. Implementation plan

Each phase ends in a working, tested state.

**Phase 1 — Foundation**
- [ ] `go mod init`, repo layout, `.gitignore` (incl. `data/`), Makefile
- [ ] `internal/store`: open DB with pragmas, embedded migrations, `0001_init.sql`
- [ ] kong skeleton with `migrate` and `account add`
- [ ] `internal/money`: parse `-9,99`, `1.234,56`, `-57`, `-16.45`, `4,602.28` → cents (table-driven tests)

**Phase 2 — Importers**
- [ ] Importer interface + raw layer (file hash, raw_records JSON, dedup, cutover)
- [ ] Finanzguru importer incl. category tree + allocations + transfer flag + tags
- [ ] DKB importer (preamble skipping, status filter, counterparty by direction, purchase-date parsing)
- [ ] Anonymized fixtures in `testdata/`; tests for re-import idempotency and overlapping ranges
- [ ] Import the real Finanzguru history; sanity-check totals per month against Finanzguru

**Phase 3 — Categorization**
- [ ] `rules.yaml` loader + matcher; `budgeteer categorize [--dry-run]`
- [ ] Simple suggestion engine (counterparty history)
- [ ] Report: how many uncategorized after rules + suggestions

**Phase 4 — Web UI**
- [ ] templ + HTMX + DaisyUI setup, `serve`
- [ ] Inbox with keyboard workflow
- [ ] Transaction list with filters
- [ ] Overview page with monthly charts
- [ ] Import via upload

**Phase 5 — Export & ops**
- [ ] `export` (CSV, Parquet), `backup` (`VACUUM INTO`)
- [ ] Optional: Metabase via Docker Compose pointing at the DB (read-only mount)

**Later**
- Further banks (one parser each), FinTS importer, budgets & savings goals, contract/subscription detection, better suggestion model.

---

## 10. Acceptance criteria for the MVP

- Importing the same file twice changes nothing.
- Importing two DKB exports with overlapping date ranges produces no duplicates.
- After importing the Finanzguru history, monthly spending per main category matches Finanzguru (allowing for transfers).
- A new DKB export is ≥ 70 % auto-categorized by rules; the rest is processable in the inbox without the mouse.
- `budgeteer export --format csv` produces files that open in a spreadsheet tool with correct amounts and categories.
