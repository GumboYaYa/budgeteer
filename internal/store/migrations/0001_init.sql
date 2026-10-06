-- +goose Up
-- 0001_init.sql — initial schema for Budgeteer
-- Conventions:
--   * dates are ISO-8601 TEXT (YYYY-MM-DD), timestamps UTC (YYYY-MM-DDTHH:MM:SSZ)
--   * money is INTEGER cents, negative = outflow; never REAL
--   * open the DB with: PRAGMA journal_mode=WAL; PRAGMA busy_timeout=5000; PRAGMA foreign_keys=ON;

CREATE TABLE accounts (
    id        INTEGER PRIMARY KEY,
    slug      TEXT NOT NULL UNIQUE,          -- used on the CLI: --account gemeinschaft
    name      TEXT NOT NULL,
    iban      TEXT UNIQUE,
    bank      TEXT,                          -- 'dkb', ...
    currency  TEXT NOT NULL DEFAULT 'EUR',
    cutover_date TEXT                        -- Finanzguru rows <= this date, bank CSV rows > this date; NULL = no cut-over
);

-- Layer 1: raw, immutable ------------------------------------------------------

CREATE TABLE imports (
    id           INTEGER PRIMARY KEY,
    source       TEXT NOT NULL,              -- 'finanzguru' | 'dkb_csv' | ...
    file_name    TEXT NOT NULL,
    file_sha256  TEXT NOT NULL UNIQUE,       -- the same file is never imported twice
    account_id   INTEGER REFERENCES accounts(id),  -- NULL when the file spans accounts (Finanzguru)
    imported_at  TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%SZ', 'now')),
    row_count    INTEGER NOT NULL
);

CREATE TABLE raw_records (
    id         INTEGER PRIMARY KEY,
    import_id  INTEGER NOT NULL REFERENCES imports(id),
    line_no    INTEGER NOT NULL,
    data       TEXT NOT NULL,                -- original row as JSON object {header: value}
    UNIQUE (import_id, line_no)
);

-- Layer 2: normalized transactions --------------------------------------------

CREATE TABLE transactions (
    id                INTEGER PRIMARY KEY,
    account_id        INTEGER NOT NULL REFERENCES accounts(id),
    raw_record_id     INTEGER NOT NULL REFERENCES raw_records(id),
    source            TEXT NOT NULL,
    external_id       TEXT,                  -- Finanzguru Buchungs-ID; NULL for bank CSV
    dedup_key         TEXT NOT NULL UNIQUE,
    booking_date      TEXT NOT NULL,
    value_date        TEXT,
    purchase_date     TEXT,                  -- card payments: real purchase date parsed from purpose
    amount_cents      INTEGER NOT NULL,
    currency          TEXT NOT NULL DEFAULT 'EUR',
    counterparty      TEXT,
    counterparty_ref  TEXT,                  -- IBAN, account number or e-mail — not always an IBAN
    purpose           TEXT,
    creditor_id       TEXT,                  -- Gläubiger-ID
    mandate_ref       TEXT,                  -- Mandatsreferenz
    end_to_end_ref    TEXT,                  -- E-Ref
    customer_ref      TEXT,                  -- Kundenreferenz
    is_transfer       INTEGER NOT NULL DEFAULT 0 CHECK (is_transfer IN (0, 1)),  -- between own accounts
    created_at        TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%SZ', 'now'))
);

CREATE INDEX idx_tx_account_date ON transactions (account_id, booking_date);
CREATE INDEX idx_tx_counterparty ON transactions (counterparty);

-- Layer 3: categorization ------------------------------------------------------

CREATE TABLE categories (
    id         INTEGER PRIMARY KEY,
    parent_id  INTEGER REFERENCES categories(id),
    name       TEXT NOT NULL,                -- 'Lebensmittel'
    slug       TEXT NOT NULL UNIQUE,         -- 'essen-trinken/lebensmittel' — stable key for rules.yaml
    excluded_from_income INTEGER NOT NULL DEFAULT 0 CHECK (excluded_from_income IN (0, 1))
);

-- One row per transaction normally; several rows = split transaction.
-- A transaction counts as categorized when its confirmed allocations sum to its amount.
CREATE TABLE allocations (
    id              INTEGER PRIMARY KEY,
    transaction_id  INTEGER NOT NULL REFERENCES transactions(id) ON DELETE CASCADE,
    category_id     INTEGER NOT NULL REFERENCES categories(id),
    amount_cents    INTEGER NOT NULL,
    source          TEXT NOT NULL CHECK (source IN ('finanzguru', 'rule', 'manual', 'suggested')),
    rule_id         TEXT,                    -- id from rules.yaml when source = 'rule'
    confidence      REAL,                    -- only for 'suggested'
    updated_at      TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%SZ', 'now'))
);

CREATE INDEX idx_alloc_tx ON allocations (transaction_id);

CREATE TABLE tags (
    id    INTEGER PRIMARY KEY,
    name  TEXT NOT NULL UNIQUE               -- e.g. 'vertrag', 'urlaub-2026'
);

CREATE TABLE transaction_tags (
    transaction_id  INTEGER NOT NULL REFERENCES transactions(id) ON DELETE CASCADE,
    tag_id          INTEGER NOT NULL REFERENCES tags(id),
    PRIMARY KEY (transaction_id, tag_id)
);

-- The categorization inbox ------------------------------------------------------

CREATE VIEW uncategorized AS
SELECT t.*
FROM transactions t
LEFT JOIN allocations a
       ON a.transaction_id = t.id
      AND a.source <> 'suggested'
WHERE t.is_transfer = 0
GROUP BY t.id
HAVING COALESCE(SUM(a.amount_cents), 0) <> t.amount_cents;
