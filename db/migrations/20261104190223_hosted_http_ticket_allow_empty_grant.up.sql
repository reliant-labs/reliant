-- forge:backward-compatible — only relaxes a CHECK; the previous release always writes a non-empty grant_id, which still satisfies the table.

-- A ticket issued on the secret access-list path carries no grant: the
-- controller authorizes it against the secret's access list and the ticket's
-- run identity. The empty string is that path; redemption still requires the
-- exact grant_id the ticket was issued with, so a grant-less ticket cannot be
-- redeemed as a grant (nor the reverse).
ALTER TABLE hosted_http_action_tickets DROP CONSTRAINT IF EXISTS hosted_http_action_tickets_grant_id_check;
