-- +goose Up
-- Recurring transactions (rent, subscriptions, insurances, ...) are collected
-- in named groups. A transaction belongs to at most one group.
CREATE TABLE recurring_groups (
    id              INTEGER PRIMARY KEY,
    name            TEXT NOT NULL COLLATE NOCASE UNIQUE,
    interval        TEXT CHECK (interval IN ('monthly', 'quarterly', 'half-yearly', 'yearly')),  -- NULL = not known
    covers_reserve  INTEGER NOT NULL DEFAULT 0 CHECK (covers_reserve IN (0, 1))  -- its transactions count as irregular expenses for the reserve
);

-- Contracts as detected by a source (Finanzguru: Analyse-Vertrags-ID) and the
-- group their transactions go to. group_id NULL means the user deleted the
-- group: imports leave that contract alone from then on.
CREATE TABLE recurring_contracts (
    source       TEXT NOT NULL,
    external_id  TEXT NOT NULL,
    group_id     INTEGER REFERENCES recurring_groups(id),
    PRIMARY KEY (source, external_id)
);

-- recurring_manual marks a group (or "no group") chosen by hand; imports do
-- not change the group of such a transaction.
ALTER TABLE transactions ADD COLUMN recurring_group_id INTEGER REFERENCES recurring_groups(id);
ALTER TABLE transactions
    ADD COLUMN recurring_manual INTEGER NOT NULL DEFAULT 0 CHECK (recurring_manual IN (0, 1));

CREATE INDEX idx_tx_recurring ON transactions (recurring_group_id);
