# Frozen: observe blocks on nodes the workflow completes itself, recorded before they were recorded

`workflow_observe.json` was recorded on `5cc17e607` (reliant#3485, before the
change) by `TestGenerateFixture_WorkflowObserve` (`generate_observe_gen_test.go`).
The `replay-observe` user draft runs `plan` and `probe` (compute) into the join
`both`, a two-iteration loop `rounds` whose body is a compute `tick`, and a
`workflow` node `child`; every one of them has an `observe:` block. That release
evaluated `observe` only for activity-backed nodes, so the run completes with
no `observe-workflow-nodes` version marker and no `RecordNodeObservation`
marker: the loop's `WorkflowCheckpoint` markers follow each other directly.

After the change the workflow records each of those nodes through the
`RecordNodeObservation` local activity (`internal/workflow/runtime/workflow_observe.go`),
so the current `fixtures/workflow_observe.json` has the version marker at the
first observed node and one `RecordNodeObservation` marker per observed node.
What keeps this history replaying: `observeWorkflowNodesChangeID`
(`observe-workflow-nodes`), asked only at a node with an observe block. This
history has no marker, so it answers `DefaultVersion` and schedules nothing.
Verified to fail replay (TMPRL1100, "code executed local activity
RecordNodeObservation, but history event found WorkflowCheckpoint") when the
gate is removed; the current fixture fails the other way when the recording
is removed.

Retire this set only together with the gate, once no run started before the
gate's deploy can still be open.
