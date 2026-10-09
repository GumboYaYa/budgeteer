-- +goose Up
-- The reserve is for expenses that come less often than every month, so a
-- monthly group can no longer be covered by it.
UPDATE recurring_groups SET covers_reserve = 0 WHERE interval = 'monthly';
