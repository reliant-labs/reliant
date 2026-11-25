// Package config is the `.reliant/pipeline.yaml` format, version 1: the one
// file a repository writes to enrol in the Reliant pipeline (review, the merge
// queue and its gate, releases, recurring jobs and the daily sweep).
//
// The smallest valid file is two lines:
//
//	version: 1
//	gate: "npm test"
//
// # Where the file is read from
//
// The pipeline reads pipeline.yaml only from the repository's base branch
// (the tip of `main`, or whatever branch the queue lands on), never from a
// pull request's head. The file decides what gates a change, who must approve
// what and which commands run on gate machines, so a PR that could edit the
// copy that judges it could approve itself. A change to pipeline.yaml is an
// ordinary PR: it is reviewed and gated under the configuration already on the
// base branch, and takes effect only once it has landed. Nothing in this
// package reads a file or a git ref: callers hand Parse the bytes they read
// from the base branch.
//
// # Strict decoding
//
// Every key is checked against the schema. An unknown key, a key in the wrong
// place or a value of the wrong type is an error naming its YAML path and line
// (`queue.batch_max (line 7): …`), never silently ignored: a typo such as
// `batchmax` would otherwise leave the default in force with nothing to say
// so. Parse reports every problem it finds in one pass, as an [Errors]. A
// `version` other than 1 is reported alone, because the rest of such a file is
// written against a schema this build does not know.
//
// Several fields take a string shorthand for their object form: `gate` and
// `check` (a string is `{run: <string>}`) and each `sweep.checks` entry (a
// string names a built-in check).
//
// # Defaults
//
// The defaults are the design's §10.1 "defaults that work with no other
// configuration", and they live in one place: [Defaults] returns them and
// [Config.Resolve] applies them over what the file wrote, producing a
// [Resolved] view with every field filled. Callers read [Resolved] and never
// re-implement defaulting. A few defaults depend on state outside the file and
// are left for the caller to resolve, each documented on its field: the gate
// pool's members, the release env chain (derived from the project's forge
// HealthGate order when `release.envs` is omitted), a release env's soak
// (the env's release_health after window when omitted) and a release env's
// approval (the project's release_approval setting unless the env says
// otherwise).
//
// # Gate detection
//
// [DetectGate] proposes a gate command from a repository's tree for the
// enrolment screen. It only proposes: the user confirms or edits the command,
// and the screen writes pipeline.yaml as a PR.
//
// # Editors
//
// [JSONSchema] returns a JSON Schema (draft 2020-12) of the same format for
// editor completion and validation. Parse is the authority; the schema
// mirrors it as far as JSON Schema can express (it cannot compile a regular
// expression, parse a cron line or a CEL expression, or check uniqueness of a
// field across list entries), and a test holds the two to the same keys.
//
// Nothing imports this package yet: the projector (pipeline build plan task
// R8) is its first reader.
//
//forge:exclude-contract: a pure file format (parse, validate, default) over caller-supplied bytes; no I/O, state or time
package config
