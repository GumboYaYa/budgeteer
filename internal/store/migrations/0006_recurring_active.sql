-- +goose Up
-- A recurring group that has ended (a cancelled subscription) is kept with
-- its transactions but marked inactive: it is left out of the monthly cost
-- and of the reserve, and can be filtered out.
ALTER TABLE recurring_groups
    ADD COLUMN active INTEGER NOT NULL DEFAULT 1 CHECK (active IN (0, 1));
