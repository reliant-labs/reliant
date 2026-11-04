-- forge:backward-compatible — additive nullable restart observation; old readers and explicit-column writes ignore it.

-- Kept on the identity, not the attachment: workers must know the drain's
-- deadline while the process is down. A fresh process registration clears it.
ALTER TABLE daemons ADD COLUMN restart_status jsonb;
