// File: internal/cli/lint/lint_idempotency.go
//
// idempotency — `forge lint --idempotency`, and a step of every `forge lint`.
//
// protobuf's `option idempotency_level = NO_SIDE_EFFECTS;` is the one
// machine-readable statement that an rpc only reads, and more and more of
// the stack acts on it:
//
//   - a read-only fixture transport (forge-web-runtime's createMockTransport
//     with `readOnly: true`, the demo and preview surfaces built on it)
//     serves an rpc only when it declares NO_SIDE_EFFECTS and refuses every
//     other one. Fail closed: a write must never reach a fixture as a read;
//   - connect-es may send such an rpc as an HTTP GET (cacheable) and retries
//     it freely;
//   - the hook generator makes it a useQuery whatever its name, and the
//     activity interceptor records nothing for it.
//
// So the declaration has to be right in both directions, and this lane keeps
// it right once a project has annotated its rpcs:
//
//   - forge-idempotency-read-undeclared: an rpc named as a read that does
//     not declare NO_SIDE_EFFECTS. A fixture transport refuses it, so a demo
//     page that calls it shows an error. Unary and server-streaming rpcs
//     only: a client-streaming rpc sends a stream of input, which is not a
//     read anything caches or serves from a fixture.
//   - forge-idempotency-write-marked-read: an rpc named as a write, or
//     marked `// forge:mutation`, that declares NO_SIDE_EFFECTS. It becomes a
//     cacheable GET and a fixture transport serves it as a read. Any shape.
//   - forge-idempotency-secret-response-marked-read: a unary rpc whose
//     response carries a secret or a value minted for the call (an access
//     token, a presigned URL, an OAuth state, an invite or claim link, a
//     checkout URL, a one-time code, a nonce, a challenge;
//     lint_idempotency_secret.go) that declares NO_SIDE_EFFECTS, or that
//     rule 1 would otherwise ask to declare it. Connect may send a
//     NO_SIDE_EFFECTS unary rpc as an HTTP GET, which caches and proxies
//     store and replay, so the next caller would get the same credential.
//     On a read-named rpc this finding replaces rule 1's: the fix is
//     `// forge:mutation: <why>` (or the lint-disable below), never the
//     option. A write-named or forge:mutation rpc declaring the option is
//     rule 2's, whose fix (delete it) settles this too. Streaming rpcs are
//     never sent as GET, so the rule judges unary ones only.
//
// ── What a name says ─────────────────────────────────────────────────────
//
// codegen.ReadVerb and codegen.WriteVerb (internal/codegen/rpc_verbs.go),
// which `forge scaffold rpc` reads too. The first word decides, as a whole
// word; no Batch or Bulk prefix is stripped. Read verbs are exactly the
// names forge ALREADY treats as reads, by those classifiers' own rules — the
// hook generator's Get, List, Search, Find, Check, Has, Is, Count, Exists
// and the activity interceptor's BatchGet, Watch, Query, Describe — so the
// rule is "if forge treats it as a read, the proto says so". A name that is
// no verb at all (Health, Version) is not judged: declaring NO_SIDE_EFFECTS
// on it is the documented way to make it a read, not something to demand.
// Write verbs are only those whose contract always changes state: Add,
// Archive, Cancel, Create, Delete, Grant, Insert, Move, Patch, Purge,
// Remove, Rename, Replace, Restore, Revoke, Send, Set, Update, Upsert. A
// verb a read-only rpc can open with (Run, Execute, Ensure, Start, Sync,
// Export, Preview) is not judged.
//
// ── Escape hatches ───────────────────────────────────────────────────────
//
// A read-named rpc that really writes (GetOrCreateSession, FindAndHoldSeat)
// carries the existing proto marker that says so, with the reason:
//
//	// forge:mutation: creates the session on first call
//	rpc GetOrCreateSession(GetOrCreateSessionRequest) returns (GetOrCreateSessionResponse);
//
// The same marker makes its generated hook a useMutation, so one fact is
// declared once. A bare `forge:mutation` on a read-named rpc is still a
// finding: the reason is what a reviewer reads. The generic
// `// forge:lint-disable-next-line <rule>: <why>` works on every rule; on
// the secret-response rule it says why the field is neither a secret nor
// a per-call value.
//
// ── Source of truth ──────────────────────────────────────────────────────
//
// The idempotency level and the streaming shape come from the forge
// descriptor (codegen.ParseDeclaredServices), compiled by buf, so an option
// written in any layout parses exactly as protoc reads it. Unmanaged
// services are in scope: the declaration describes the rpc, whoever serves
// it. The comment markers and each rpc's line come from the source
// (codegen.ScanRPCCommentMarkers, the hook generator's own reader).
//
// ── Severity ─────────────────────────────────────────────────────────────
//
// An error, under the warn-first rollout (lint_rollout_table.go): a project
// pinned below the release after the one that ships the lane sees warnings
// carrying the gate date.

package lint

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"

	"github.com/reliant-labs/forge/internal/codegen"
	"github.com/reliant-labs/forge/internal/linter/finding"
	"github.com/reliant-labs/forge/internal/linter/suppress"
)

// The lane's rule IDs.
const (
	ruleIdempotencyReadUndeclared = "forge-idempotency-read-undeclared"
	ruleIdempotencyWriteAsRead    = "forge-idempotency-write-marked-read"
	ruleIdempotencySecretResponse = "forge-idempotency-secret-response-marked-read"
)

// noSideEffectsOption is the declaration the rules are about.
const noSideEffectsOption = "option idempotency_level = NO_SIDE_EFFECTS;"

// idempotencyRPC is what the rules read about one rpc.
type idempotencyRPC struct {
	Service string
	Name    string
	// File is the declaring .proto, relative to the project root; Line the
	// rpc keyword's line in it (0 when the source could not be read).
	File string
	Line int
	// ClientStreaming / ServerStreaming / NoSideEffects are the compiled
	// descriptor's.
	ClientStreaming bool
	ServerStreaming bool
	NoSideEffects   bool
	// SecretField is the dotted path of the response's shallowest
	// secret-shaped field and SecretWhy what marks it
	// (secretResponseField); "" when the response carries none.
	SecretField string
	SecretWhy   string
	// Mutation / MutationReason: a `// forge:mutation` marker and its prose.
	Mutation       bool
	MutationReason string
}

// idempotencyFindingsFor is the pure rule over rpcs.
func idempotencyFindingsFor(rpcs []idempotencyRPC) []finding.Finding {
	var out []finding.Finding
	add := func(r idempotencyRPC, rule, msg, fix string) {
		out = append(out, finding.Finding{
			Rule: rule, Severity: finding.SeverityError, File: r.File, Line: r.Line,
			Message: msg, Remediation: fix,
		})
	}
	for _, r := range rpcs {
		rpc := r.Service + "." + r.Name
		// Connect sends only a unary rpc as GET.
		secret := r.SecretField != "" && !r.ClientStreaming && !r.ServerStreaming
		carries := fmt.Sprintf("%#q, which %s", r.SecretField, r.SecretWhy)
		if r.NoSideEffects {
			switch verb := codegen.WriteVerb(r.Name); {
			case r.Mutation:
				add(r, ruleIdempotencyWriteAsRead,
					fmt.Sprintf("rpc %s is marked `// %s` but declares idempotency_level = NO_SIDE_EFFECTS: the proto says both that it writes and that it only reads", rpc, codegen.ProtoMarkerMutation),
					"keep the one that is true: delete the NO_SIDE_EFFECTS option if the rpc writes, or the forge:mutation marker if it only reads")
			case verb != "":
				add(r, ruleIdempotencyWriteAsRead,
					fmt.Sprintf("rpc %s is named as a write (%s) but declares idempotency_level = NO_SIDE_EFFECTS: Connect clients may send it as a cacheable HTTP GET and retry it, and a read-only fixture transport serves it as a read", rpc, verb),
					"delete the option (IDEMPOTENT says a repeat is safe without saying it reads); if the rpc truly only reads, rename it, or say why on the line above: `// forge:lint-disable-next-line "+ruleIdempotencyWriteAsRead+": <why>`")
			case secret:
				add(r, ruleIdempotencySecretResponse,
					fmt.Sprintf("rpc %s declares idempotency_level = NO_SIDE_EFFECTS but its response carries %s: Connect clients may send it as an HTTP GET, which browser caches, CDNs and proxies may store and replay, handing the same secret or per-call value to the next caller", rpc, carries),
					secretResponseFix(r))
			}
			continue
		}
		if r.ClientStreaming {
			continue
		}
		verb := codegen.ReadVerb(r.Name)
		if verb == "" {
			continue
		}
		switch {
		case r.Mutation && r.MutationReason != "":
			// The author declared it writes, and why.
		case r.Mutation:
			add(r, ruleIdempotencyReadUndeclared,
				fmt.Sprintf("rpc %s is named as a read (%s) and marked `// %s` with no reason: say why a %s rpc writes", rpc, verb, codegen.ProtoMarkerMutation, verb),
				fmt.Sprintf("write the reason after the marker: `// %s: <what it writes>`", codegen.ProtoMarkerMutation))
		case secret:
			add(r, ruleIdempotencySecretResponse,
				fmt.Sprintf("rpc %s is named as a read (%s) but its response carries %s: it must not declare %#q, because Connect clients may then send it as an HTTP GET that browser caches, CDNs and proxies store and replay", rpc, verb, carries, noSideEffectsOption),
				secretResponseFix(r))
		default:
			add(r, ruleIdempotencyReadUndeclared,
				fmt.Sprintf("rpc %s is named as a read (%s) but does not declare %#q: a read-only fixture transport refuses it, and clients cannot cache or retry it as a read", rpc, verb, noSideEffectsOption),
				fmt.Sprintf("declare %#q in the rpc's body; if it really writes, mark it `// %s: <what it writes>` above the rpc (its generated hook then becomes a useMutation too)", noSideEffectsOption, codegen.ProtoMarkerMutation))
		}
	}
	return out
}

// secretResponseFix is the remediation of a secret-response finding.
func secretResponseFix(r idempotencyRPC) string {
	fix := fmt.Sprintf("say that each call mints a new value: `// %s: <what each call issues>` above the rpc (its generated hook becomes a useMutation)", codegen.ProtoMarkerMutation)
	if r.NoSideEffects {
		fix = "delete the option, and " + fix
	}
	return fix + "; if the field is neither a secret nor a per-call value, say why on the line above: `// forge:lint-disable-next-line " + ruleIdempotencySecretResponse + ": <why>`"
}

// collectIdempotencyFindings reads the declared services and their proto
// sources under root and returns the findings, suppression directives
// applied, sorted by (file, line). A project with no descriptor (nothing
// generated yet) yields nothing; one forge cannot read yields one finding
// saying so.
func collectIdempotencyFindings(root string) ([]finding.Finding, error) {
	services, err := codegen.ParseDeclaredServices(root)
	if err != nil {
		return []finding.Finding{{
			Rule: ruleIdempotencyReadUndeclared, Severity: finding.SeverityError,
			Message:     "cannot check idempotency declarations: " + strings.SplitN(err.Error(), "\n", 2)[0],
			Remediation: "run `forge generate`",
		}}, nil
	}
	markers := map[string]codegen.RPCCommentMarkers{}
	var rpcs []idempotencyRPC
	for _, svc := range services {
		file := protoSourcePath(root, svc.ProtoFile)
		m, ok := markers[file]
		if !ok {
			if content, rerr := os.ReadFile(filepath.Join(root, file)); rerr == nil {
				m = codegen.ScanRPCCommentMarkers(string(content))
			}
			markers[file] = m
		}
		for _, meth := range svc.Methods {
			secretField, secretWhy := secretResponseField(svc.Schemas, meth.OutputTypeFQ)
			rpcs = append(rpcs, idempotencyRPC{
				Service: svc.Name, Name: meth.Name, File: file, Line: m.Lines[meth.Name],
				ClientStreaming: meth.ClientStreaming, ServerStreaming: meth.ServerStreaming,
				NoSideEffects: meth.NoSideEffects,
				Mutation:      m.Mutation[meth.Name], MutationReason: m.MutationReason[meth.Name],
				SecretField: secretField, SecretWhy: secretWhy,
			})
		}
	}
	res := suppress.ApplyFiles(root, idempotencyFindingsFor(rpcs), os.ReadFile)
	out := slices.Concat(res.Kept, res.Violations)
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].File != out[j].File {
			return out[i].File < out[j].File
		}
		return out[i].Line < out[j].Line
	})
	return out, nil
}

// protoSourcePath is the declaring .proto relative to root, slash-separated.
// The descriptor records ProtoFile relative to the buf module root
// (`services/x/v1/x.proto`, the module being proto/), so a path that does not
// resolve from root is taken from proto/.
func protoSourcePath(root, protoFile string) string {
	if _, err := os.Stat(filepath.Join(root, filepath.FromSlash(protoFile))); err == nil {
		return filepath.ToSlash(protoFile)
	}
	return filepath.ToSlash(filepath.Join(protoDirDefault, filepath.FromSlash(protoFile)))
}

// collectIdempotencyJSON maps the findings onto the JSON contract; the bool
// is the gating verdict.
func collectIdempotencyJSON(root string) ([]lintJSONFinding, bool, error) {
	findings, err := collectIdempotencyFindings(root)
	if err != nil {
		return nil, false, fmt.Errorf("idempotency lint failed: %w", err)
	}
	return findingsToJSON(findings), countErrors(findings) > 0, nil
}
