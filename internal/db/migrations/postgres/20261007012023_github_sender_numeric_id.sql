-- +goose Up

-- trigger.sender.id on GitHub is now the sender's numeric user id, not their
-- login. A login can be renamed, and the old one registered by someone else,
-- so an allowlist of logins admits whoever claims a name after its owner
-- gives it up; the numeric id is assigned once and never reused. Two places
-- stored logins under the old meaning.

-- 1. A GitHub connection's sender_id, which "Only from: Me" reads, was the
-- login (probe.sender_id: response.login). The same probe wrote
-- external_account_id from the same GET /user as string(response.id), and a
-- GitHub connection always acts as one user, so the id is already on the row:
-- converted here, with no call to GitHub. A row without a numeric account id
-- (none is expected) loses its sender rather than keep a login; testing or
-- reconnecting the connection probes it again.
UPDATE connections
SET sender_id = CASE WHEN external_account_id ~ '^[0-9]+$' THEN external_account_id END
WHERE integration_id = 'github';

-- 2. A GitHub trigger whose filter compares trigger.sender.id compared it to
-- logins, and now matches nobody: it would sit enabled and never fire, with
-- no hint why. Converting a login to an id needs a call to GitHub, and the
-- person a login names today need not be the one it named when the list was
-- written, so these are not converted:
--
--  * an ad hoc trigger's "Only from" clause, in the exact shape the control
--    writes it ("trigger.sender.verified && trigger.sender.id in [...]",
--    alone or after "(<rest>) && "), is removed, keeping <rest>;
--  * every such trigger is DISABLED. Removing an allowlist alone would let
--    anyone's event start a run, so the trigger stays off until its owner
--    looks at it and turns it back on.
--
-- An activation's filter (workflow_trigger set) is only a projection of its
-- workflow's declaration, which is re-read when it fires, so it is left as is
-- and the activation is disabled; the declaration itself lives in the
-- workflow, and the "Only from" control flags its stale logins.
UPDATE triggers
SET filter = CASE
        WHEN workflow_trigger IS NOT NULL THEN filter
        WHEN filter ~ '^trigger\.sender\.verified && trigger\.sender\.id in \[.*\]$' THEN ''
        WHEN filter ~ '^\(.*\) && trigger\.sender\.verified && trigger\.sender\.id in \[.*\]$'
            THEN regexp_replace(filter, '^\((.*)\) && trigger\.sender\.verified && trigger\.sender\.id in \[.*\]$', '\1')
        ELSE filter
    END,
    enabled = false,
    updated_at = now()
WHERE kind = 'integration'
  AND config->>'integration' = 'github'
  AND filter LIKE '%trigger.sender.id%';
