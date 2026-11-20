# Recover a failed archived-chat index build

Keep a valid `public.idx_chats_user_archived` on application rollback. An extra index does not change query results. A failed concurrent build is different: it can leave an invalid index and a dirty migration record. Dropping the invalid index alone does not make the migrator retry.

Use the authorized database recovery path in the control-plane runbook, **Database recovery (prod)**. Pause deploy retries and make sure no other migration is running. From the exact release that failed, use its `reliant db migrate status` against the same database and inspect the migration logs. A nonzero status is expected for a dirty schema. Confirm that the dirty version is the version of `*_chats_user_archived_index.up.sql` in that release and identify the last successfully applied version from its migration logs and ordered migration list. Do not derive it by subtracting one from the dirty version.

Check whether the build left an index:

```sql
SELECT i.indisvalid, i.indisready, pg_get_indexdef(i.indexrelid)
FROM pg_index i
WHERE i.indexrelid = to_regclass('public.idx_chats_user_archived');
```

If the index is absent, there is nothing to drop. If it is invalid and you have confirmed it is from this failed build, run `DROP INDEX CONCURRENTLY public.idx_chats_user_archived;` outside a transaction via the authorized write session. Do not drop a valid or unexpected index; stop and reconcile the database with the incident lead instead. Recheck that the index is absent and that the schema reflects the last successfully applied version. If either the migration history or the actual schema is uncertain, stop rather than forcing a version.

Using the **failed release's** migration binary against the same database, run `reliant db migrate force <last-successfully-applied-version>`. This records the verified version and clears the dirty flag; it does not run or undo SQL. Run `reliant db migrate status` again and confirm the archived-index migration is pending. Then reapply via the normal migration job. Verify `db migrate status` is clean at the expected version and rerun the query above: the index must exist with `indisvalid` and `indisready` true and the definition matching the migration. Resume deployment only then.
