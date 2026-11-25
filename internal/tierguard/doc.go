// Package tierguard proves that every Tier-1 (regenerated-every-run)
// file forge writes is actually DERIVED from something the user wrote.
//
// # The rule
//
// Tier-1 is legitimate only for files re-derived from a user input. If a
// generated file is statically the same regardless of the user's inputs,
// it is library code sitting in the user's tree behind a "do not edit"
// banner, and it belongs in forge/pkg with a short user-owned scaffold
// instead.
//
// Note what this is NOT. The older framing — "use _gen only when users
// will never edit the file" — is a prediction about human behaviour, and
// no test can check it. This package checks a PROPERTY OF THE FILE: is
// its content a function of the user's inputs, or is it a constant? A
// constant does not need regenerating, so the only thing regeneration
// buys is the ability to change the file under the user, which is
// exactly the harm.
//
// # Why a constant Tier-1 file is harmful even though forge never clobbers
//
// Tier-1 does not destroy edits. checksums.WriteGeneratedFile classifies
// on-disk content and SKIPS anything hand-edited unless --heal or --force
// applies. The harm is quieter: an edit puts the file in PERMANENT DRIFT.
// forge stops updating it, so every future improvement silently stops
// arriving. The user has not gained ownership — they have fallen off the
// upgrade path. A file that never needed to be Tier-1 imposes that risk
// for nothing in return.
//
// # The method: differential rendering
//
// A file is derived if and only if its content responds to the inputs.
// So render the whole project several times against MATERIALLY DIFFERENT
// sets of user inputs and compare the bytes of each Tier-1 file:
//
//   - bytes differ in any pair → the file is a function of the inputs.
//     Derived. Tier-1 is correct.
//   - bytes identical everywhere → the inputs moved and the file did
//     not. Not derived. Reported as a mis-tier candidate.
//
// The four derivation inputs, all treated as first class:
//
//  1. service / entity .proto files (including the config proto, whose
//     per-binary annotations are a declaration in their own right)
//  2. SQL migrations in db/migrations/ — including their ABSENCE
//  3. internal/<pkg>/contract.go — drives mock and middleware generation
//  4. forge.yaml — services, frontends, binaries, features
//
// The fixtures differ across all of them. Project A is one service with
// a 4-column entity and no internal package. Project B is two services
// with two entities spanning more scalar kinds (int32, double, float,
// bytes), a frontend, an internal package carrying a contract.go, and a
// SECOND BINARY with its own binary-bound config message. Project D is
// the shape neither can express: no entity at all — so no migration —
// plus a service whose name shadows a built-in subcommand. See
// projectA / projectB / projectD in fixtures.go.
//
// WHY THE SET GREW BEYOND A PAIR. An input no fixture exercises is
// indistinguishable from an input the file does not track: both render
// byte-identical. Three files looked like constants under the original
// A/B pair purely because A and B happened to agree on the input each
// one projects — both had migrations, and neither named a colliding
// service. Adding a fixture that moves the input is what turns a
// suspected mis-tier into evidence either way, and it is strictly better
// than an allow-list entry, which only records an opinion.
//
// # What is deliberately held CONSTANT, and why
//
// Every input-varied fixture uses the SAME project name and module path.
// This is the load-bearing control, and it holds no matter how many
// fixtures are added. Vary the module path and nearly every Go file
// differs — its import block now spells a different path — and the guard
// would pronounce cmd/<bin>/cmd/server.go "derived" on the strength of a
// string substitution. Project identity is not one of the four inputs;
// holding it fixed is what makes a byte difference mean "responds to the
// user's DECLARATIONS". Identical names also make the project-relative
// paths line up, so the renders can be compared key by key.
//
// Fixture C is the deliberate exception: it carries A's inputs under a
// DIFFERENT module path and is excluded from every verdict. It exists
// only to annotate a constant file with whether it embeds the user's
// module identity, which changes the remedy (a verbatim move to
// forge/pkg, versus a library plus a small user-owned scaffold).
//
// # Where the inventory comes from
//
// Not from a hand-written file list, and not from grepping for banner
// text: from the producer itself. checksums.Tier1TargetSet is populated
// by markTier1Target, called at the head of checksums.WriteGeneratedFile
// — which, since WriteGeneratedFileTier1 delegates to it and
// writeUnstampable is reached through it, is the single chokepoint every
// Tier-1 write in the codebase passes through. Whatever a run of the
// pipeline targets as Tier-1 is in that map, including paths that would
// be skipped as disowned. A new emitter added anywhere in
// internal/codegen or internal/generator therefore enters this guard's
// scope automatically, with no list to update.
//
// Reading it requires running the pipeline IN-PROCESS (see TestMain for
// the protoc-gen-forge dispatch that makes the test binary a working
// stand-in for the forge binary). A subprocess would leave the map in a
// dead address space, and reconstructing the set from disk markers is
// strictly worse: internal/handlers/mocks/<svc>_mock.go carries the
// forge-owned banner with no hash marker at all, so a marker scan misses
// it. That miss is the same family of error the earlier audits made.
//
// # Vacuity
//
// A differential test that renders nothing passes, and a green
// zero-file run is worse than no guard because it reads as evidence. The
// inventory is therefore asserted non-empty and sanity-checked against
// files known to be present in any project shape before any comparison
// runs.
package tierguard
