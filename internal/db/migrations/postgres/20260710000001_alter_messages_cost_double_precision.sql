-- +goose Up
-- messages.cost stores a USD monetary value backed by a Go *float64.
-- REAL (single precision) loses precision on round-trip (e.g. 0.0345 -> 0.03449999...),
-- so widen it to DOUBLE PRECISION to match the application type.
ALTER TABLE messages ALTER COLUMN cost TYPE DOUBLE PRECISION;

-- +goose Down
ALTER TABLE messages ALTER COLUMN cost TYPE REAL;
