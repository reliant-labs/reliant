# Workflow drafts: work-in-progress vs complete

Status: implementing (branch `workflow-history`). Pre-launch: migrate freely.

## Product decision (from the user)

Users must be able to store an INVALID workflow as work in progress, and later
mark it complete. So "blocks on validation errors" applies to becoming
complete/usable, not to every save.

## Model

Replace `workflow_drafts.is_valid` (a cached, stale-able validation verdict)
and `validation_errors` (a cached error list) with an explicit lifecycle:

- `status` (TEXT, NOT NULL, CHECK in ('draft','complete')), default 'draft'.
  - `draft`: saved as-is, may be invalid, never runnable, never offered in
    pickers that start runs (spawn presets, routers, workflow refs, the chat
    workflow selector). Visible in the user's workflow list with a "Draft"
    badge and its CURRENT validation findings (computed on read).
  - `complete`: passed validation at the moment it was marked complete;
    runnable. Any later save of a complete workflow must re-validate: invalid
    ⇒ the save is rejected with the errors (a complete workflow is never
    silently made invalid). To make invalid edits, the user explicitly moves it
    back to draft (or saves-as-draft, which does that).
- Validity is NEVER stored. It is computed on read (list/get) and at the two
  gates: mark-complete and load-for-execution. Delete the columns, the
  `is_valid`/`validation_errors` proto fields on stored-draft messages (keep
  the per-request validation result fields on save/validate RESPONSES, which
  are not cached state), and all code that reads/writes them.
- Execution gate: `LoadWorkflowActivity` (and `GetUsableWorkflowBySlug`, the
  scenario loader, any workflow-ref resolution) only loads `status='complete'`
  AND re-validates (already does at run start). A draft referenced by slug is
  "not found / not runnable (draft)" with an actionable message.

## API

- `SaveWorkflow` / `CreateWorkflowDraft` / `ImportWorkflow` gain an explicit
  intent: `complete` (bool, or a `status` enum — pick the one that reads best
  in the proto; enum is more future-proof). Semantics:
  - intent draft: always stores (as long as it parses as a workflow); returns
    current validation findings as warnings/errors in the response;
    status=draft.
  - intent complete: validates; errors ⇒ reject (success=false, errors, nothing
    stored); ok ⇒ status=complete.
  - Saving an existing complete workflow without specifying intent keeps it
    complete ⇒ must validate (see above).
- `SetWorkflowStatus(draft_id, status)` (or MarkWorkflowComplete /
  MoveToDraft): complete ⇒ validate current definition, reject with errors if
  invalid.
- YAML that does not PARSE is still rejected even as a draft (nothing to store
  structurally). Decide whether a raw-text draft is needed; default: no.

## Agent tools

`create_workflow` / `edit_workflow` / `write_workflow`: default intent = draft
(agents iterate), and a `complete: true` parameter that applies the gate. The
tool response always includes current validation errors+warnings and the
resulting status, so the agent knows whether it is done. `list_workflows` /
`get_workflow` show status + computed validity.

## UI (web/src)

- Builder save: saves as draft by default when the workflow is invalid (show
  errors inline, badge "Draft"); a "Mark complete" action (disabled with the
  error list when invalid). Saving a complete workflow with errors: offer
  "Save as draft" instead of failing silently.
- Workflow list: status badge; drafts not selectable where a runnable workflow
  is required.
- Keep changes minimal and consistent with the existing builder UX; follow
  reliant.md's web styling contract (semantic tokens, cn(), no ad-hoc
  selectors).

## Migration

goose migration: add `status` (existing rows: `complete` if they validate
under the CURRENT validator at migration time is impossible in SQL — so set
`complete` where `is_valid=1`, else `draft`; the execution gate re-validates
anyway), drop `is_valid`, `validation_errors`. Update schema.sql (drift test),
sqlc queries + regenerate, repository layer, accountpurge unaffected (rows keep
user_id).

## Tests (fail-first)

- Invalid workflow saved with intent draft ⇒ stored, status draft, errors
  returned.
- Invalid with intent complete ⇒ rejected, nothing stored.
- Mark complete on invalid draft ⇒ rejected with errors; on valid ⇒ complete.
- Save of a complete workflow that introduces an error ⇒ rejected.
- Draft never runnable: CreateChat / LoadWorkflowActivity / spawn / ref /
  router / scenario loader refuse a draft by slug.
- List shows computed validity (no stale flag) + status.
- Agent tools: default draft; complete:true gates; responses carry findings.
- Web: builder save/mark-complete flows (vitest where the repo tests them).
