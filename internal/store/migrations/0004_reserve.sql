-- +goose Up
-- Reserve for irregular expenses (taxes, insurances, ...). A transaction
-- marked with is_reserve is a bill that is expected again one year later; the
-- account marked with holds_reserve is where the money for those bills is
-- saved. Both are set by hand and never changed by an import.
ALTER TABLE transactions
    ADD COLUMN is_reserve INTEGER NOT NULL DEFAULT 0 CHECK (is_reserve IN (0, 1));
ALTER TABLE accounts
    ADD COLUMN holds_reserve INTEGER NOT NULL DEFAULT 0 CHECK (holds_reserve IN (0, 1));
