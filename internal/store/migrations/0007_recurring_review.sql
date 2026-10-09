-- +goose Up
-- Reviewing recurring costs: a mandatory group (a loan, the rent) cannot be
-- cancelled and is left out of the review; the verdict records what was
-- decided for the others. Both are set by hand only.
ALTER TABLE recurring_groups
    ADD COLUMN mandatory INTEGER NOT NULL DEFAULT 0 CHECK (mandatory IN (0, 1));
ALTER TABLE recurring_groups
    ADD COLUMN verdict TEXT CHECK (verdict IN ('keep', 'cancel'));  -- NULL = undecided
