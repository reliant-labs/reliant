-- forge:backward-compatible — validates existing rows against llm_call_usage_funding_check; every existing row holds the default '', which it admits, and nothing is written.

-- Migration: llm_call_usage_funding_validate
-- Created: 2026-11-13T01:52:05Z
--
-- Validate the CHECK 20261113015141_llm_call_usage_funding added NOT VALID, in
-- its own transaction: VALIDATE CONSTRAINT holds SHARE UPDATE EXCLUSIVE, which
-- does not block usage writes while it scans. lock_timeout bounds the wait.
SET LOCAL lock_timeout = '5s';
ALTER TABLE llm_call_usage VALIDATE CONSTRAINT llm_call_usage_funding_check;
