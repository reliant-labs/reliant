// Package obsout is the agent output layer of the feedback-loop verbs
// (`forge obs …`, `forge env releases …`, `forge db query`): the one place
// that decides what a machine-readable document looks like, so every verb an
// agent calls answers in the same shape.
//
// The contract (research/feedback-loop/DESIGN.md §8.2):
//
//   - ONE document on stdout. It starts with a [Head]: the schema name and the
//     ok / exit_code / error triple, computed from the error the verb returns
//     so the document and the process status cannot disagree. Progress goes to
//     stderr. A streaming verb writes NDJSON ([Stream]) and ends with a line
//     that carries the same head.
//   - [Scope] echoes what was resolved (env, backend, time range, services).
//   - Lists are bounded: [Page] carries truncated / omitted / next_page_token,
//     and [Cap] is how a verb applies its cap.
//   - [Suggest] offers at most three next commands, each complete and
//     runnable. [Command] builds one with correct shell quoting.
//   - --fields ([Schema.ParseFields], [Write]) projects a document with dotted
//     paths, validated against the fields the verb declares, so a typo is an
//     error rather than a silently empty answer.
//   - Absent means unknown: nothing here emits null or empty placeholders.
//
// The package imports nothing from forge's cli, the control plane or reliant.
// A verb embeds [Head] (and [Page]) in its own document struct.
//
//forge:exclude-contract: a document vocabulary (types and pure functions) that verbs embed and call; there is no behaviour to substitute
package obsout
