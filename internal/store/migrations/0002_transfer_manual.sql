-- +goose Up
-- Marks a transfer flag that was set by hand, so that a forced re-import does
-- not reset it to the value from the source file.
ALTER TABLE transactions
    ADD COLUMN is_transfer_manual INTEGER NOT NULL DEFAULT 0 CHECK (is_transfer_manual IN (0, 1));
