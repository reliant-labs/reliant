-- forge:backward-compatible — adds one nullable column to trigger_events; the previous release never reads it and its inserts leave it NULL.

-- Which workflow definition a firing resolved (core.TriggerEventDefinition
-- JSON): the ref, where it resolved (builtin, the owner's account workflow,
-- or a synced project file), the file's project-relative path, the machine
-- whose copy was read, the git branch/commit/dirty state it was synced from,
-- the hex SHA-256 of its content, and why it could not run when it could not.
-- Written when the firing is decided (an inbound event's again when its fire
-- re-reads the definition at launch); NULL for a firing recorded without one
-- (an ad hoc chat start, or a firing that predates this column).
ALTER TABLE trigger_events ADD COLUMN definition jsonb;
