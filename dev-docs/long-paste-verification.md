# Long-paste release verification and rollback

PR: https://github.com/reliant-labs/reliant/pull/2351

## Packaged Electron check

Before broad desktop rollout, the release verifier must run these checks in the
packaged release candidate on macOS, Windows and Linux, recording the version,
OS and results in the PR or release verification record. Unit/VM tests do not
substitute for these native checks. If this cannot be completed, desktop
verification remains outstanding; do not describe it as passed.

1. Paste plain text above 5,000 characters into an empty composer using both
   Cmd/Ctrl+V and Edit → Paste. Confirm one text attachment, exact preview
   contents (include Unicode, tabs and multiple lines), and no duplicate text.
2. Use Cmd/Ctrl+Shift+V, Edit → Paste as Plain Text, and the context-menu plain
   paste command. Confirm one inline insertion at the selection, no generated
   attachment, and no duplication. Confirm the next ordinary paste converts.
3. Immediately undo an automatically converted paste. Confirm the text returns
   inline. In an ordinary editable field outside the composer, plain-paste
   over a selection and use native undo; confirm both insertion and undo work.
4. Paste an image and attach a real .txt file. Confirm their existing behavior
   is unchanged. Send generated pasted text and confirm its full content reaches
   the conversation/model. Exercise attachment-only queueing during a run.
5. While a send or queue request is delayed, confirm no enabled removal action
   can delete its generated attachment. Navigate away and back; confirm submitted
   content remains available. On a failed request, confirm recoverable text and
   transcript retry retain access to the attachment.
6. Repeat ordinary and plain-text paste in a supported browser as a parity check.

Any lost, empty, duplicated or uneditable paste, failed native insertion/undo,
clipboard action targeting the wrong field/window, or missing submitted
attachment fails verification and stops broad rollout. Do not repeat a failed
check until its cause is understood and addressed.

## Mitigation and rollback

- If only automatic conversion is affected, the user can immediately disable
  **Settings → Chat & editor → Long pastes**. This returns normal paste to inline
  text; it does not disable Electron's newly added plain-text menu command.
  Recover existing pasted drafts using **Insert as text**, or copy the preview
  before switching versions. Never delete attachment rows or persisted drafts
  as a mitigation.
- If native plain-paste/undo fails, do not rely on that preference as a full
  rollback: it does not affect the native bridge. Stop promotion/distribution
  of the candidate. If already distributed, the release owner must pause further
  rollout and ship a patch reverting the long-paste feature changes (including
  the Electron menu/preload bridge), preserving subsequent unrelated changes.
  Releases remain owned by the control-plane release train, not a chat agent.
- Revert feature commit `088680c1c` and ownership fix `461624a56` as one coordinated
  change, plus any later feature-specific fixes, or apply their equivalent
  reverse changes if intervening edits conflict. Do not revert the main merge
  commit `6e4d4c58a`, which would remove unrelated main changes.
- There are no feature migrations or new wire formats to roll back. Existing
  uploaded text attachments remain ordinary attachments and must remain stored
  for conversation replay and retry. Raw pasted drafts use the localStorage
  prefix `reliant:long-paste:v1:`; retain them, do not clear browser/app storage.
  A version without the recovery UI will not expose those drafts, so preserve a
  copy through the current UI when available. Re-enable a compatible recovery
  build before declaring outstanding drafts recovered.
- Validate the rollback patch with the merge gate, ordinary native paste and
  file-upload checks, and conversation replay of an already sent pasted .txt
  attachment. Resume rollout only after those checks pass and the verifier
  records the result.
