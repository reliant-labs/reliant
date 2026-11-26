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
// Every mapping key must be a YAML string written once: a key such as
// `true:` or `1:` is never coerced to text, and a key written twice (at any
// level, in a struct or a free map such as `playbooks`) is an error naming
// both lines rather than letting the later value win. A string field takes
// only a YAML string: `run: 42` is an error that suggests quoting, never the
// command "42". gate.env values are the one exception, taking any scalar but
// null (`PORT: 8080` is "8080").
//
// Several fields take a string shorthand for their object form: `gate` and
// `check` (a string is `{run: <string>}`), each `sweep.checks` entry and each
// release env's `probes` entry (a string names a built-in). The shorthand is
// a YAML string only, so `gate: 42`, `gate: true` and `check: null` are
// errors. For a sweep check or a probe, `{name: <built-in>}` is the built-in,
// the same as the bare name; `{name, run}` is a repo command, whose name may
// not be a built-in's.
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
// # Gate stages
//
// The gate is a fixed sequence of built-in stages (owner decision D13,
// 2026-11-26): lint, test, build, sbom_scan, always in that order, and a
// failed stage stops the later ones. `gate.stages.<name>` overrides one with
// `{run: "<cmd>"}` or turns it off with the exact string `off`; any other
// string is an error, so `off` is never read as a command. `gate: "<cmd>"`
// and `gate.run` are the test stage's command, as they were before stages
// existed; writing gate.run and gate.stages.test both is an error. A stage not
// written keeps its default for the project's kind ([Project.DefaultStages]):
//
//	       lint                test                       build                          sbom_scan
//	forge  reliant forge lint  task ci:full or task test  reliant forge env build <env>  built-in scan, <env>'s vuln_gate
//	Go     go vet ./...        go test ./...              go build ./...                 built-in scan, default policy
//	Node   npm run lint        npm test                   npm run build                  built-in scan, default policy
//
// <env> is the release chain's first env, never pushed to. The default
// sbom_scan is not a command: it is forge's SBOM and scan of the build, run
// by the gate runner (kind builtin_scan); a `run` override of it is a repo
// command under the same 0/1/2 exit contract as every stage.
//
// [Resolved].Gate.Stages lists all four with a State and a Source. off is a
// choice the file made (or runner checks, which runs no stage); turning a
// stage off or back on is a recorded setting change (`gate.stage.<name>`,
// see [StageChanges]), and every report shows it as off, never as passed.
// unavailable is a default with nothing to run in this project, such as a
// Node repository without a lint script: nobody chose it, so it is neither
// off nor passed and reports render it as unavailable with its Reason. The
// stage defaults depend on the repository's tree, so [Config.ResolveFor]
// takes the [Project] that [DetectProject] reads; [Config.Resolve] resolves
// for an undetected project, whose lint and build are unavailable.
//
// # Gate detection
//
// [DetectGate] proposes a gate command from a repository's tree for the
// enrolment screen, and [DetectProject] the facts the stage defaults follow.
// They only propose: the user confirms or edits the command, and the screen
// writes pipeline.yaml as a PR.
//
// # Editors
//
// [JSONSchema] returns a JSON Schema (draft 2020-12) of the same format for
// editor completion and validation. Parse is the authority; the schema
// mirrors it as far as JSON Schema can express (it cannot compile a regular
// expression, parse a cron line or a CEL expression, check uniqueness of a
// field across list entries, or see a duplicate or non-string YAML key).
// Tests hold the two to the same keys and closed sets, and drive one corpus
// of string-or-mapping and key inputs through both, failing on any
// accept/reject disagreement.
//
// Nothing imports this package yet: the projector (pipeline build plan task
// R8) is its first reader.
//
//forge:exclude-contract: a pure file format (parse, validate, default) over caller-supplied bytes; no I/O, state or time
package config
