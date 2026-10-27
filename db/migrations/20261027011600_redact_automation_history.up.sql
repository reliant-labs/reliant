-- forge:backward-compatible — removes historical copies of arbitrary sensitive configuration
-- Earlier development heads copied raw params/message/graph into history. Do not
-- keep a removed secret alive in either version of an audit snapshot.
UPDATE trigger_changes SET before_state = CASE WHEN before_state IS NULL THEN NULL ELSE '{"configuration":"historical values withheld"}'::jsonb END,
 after_state = '{"configuration":"historical values withheld"}'::jsonb;
-- Only automation-management approval cards used these distinctive prefixes.
UPDATE approvals SET title = 'Automation change approval: historical sensitive details removed; request a new approval'
WHERE title LIKE 'Machine:%' OR title LIKE 'Pause activation %';
