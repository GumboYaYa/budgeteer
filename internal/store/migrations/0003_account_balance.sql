-- +goose Up
-- A known balance of the account: balance_cents is the balance at the end of
-- balance_date, as stated by the bank or the source file. Balances for other
-- dates are derived from it by adding or subtracting transactions, so the
-- history does not have to start at zero.
ALTER TABLE accounts ADD COLUMN balance_cents INTEGER;
ALTER TABLE accounts ADD COLUMN balance_date TEXT;
